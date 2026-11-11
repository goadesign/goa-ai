{{- $t := .Transport -}}
{{- $pathParams := (index $t.Endpoints 0).Payload.Request.PathParams -}}
// ServeStdio reads newline-delimited MCP JSON-RPC messages from r, dispatches
// requests through endpoints, and writes newline-delimited responses and
// notifications to w. ServeStdio takes ownership of r and w and closes both
// streams before returning.
func ServeStdio(
	ctx context.Context,
	r, w *os.File,
	endpoints *{{ $t.Service.PkgName }}.{{ $t.Service.EndpointsDeclaration.Name }}{{ range $pathParams }},
	{{ .VarName }} {{ .TypeRef }}{{ end }},
) error {
	if endpoints == nil {
		return errors.New("mcp: endpoints are required")
	}
	return mcpruntime.ServeStdio(ctx, r, w, func(ctx context.Context, req *jsonrpc.RawRequest, legacy bool) *jsonrpc.Response {
		return dispatchStdio(ctx, endpoints, req, legacy{{ range $pathParams }}, {{ .VarName }}{{ end }})
	})
}

func dispatchStdio(
	ctx context.Context,
	endpoints *{{ $t.Service.PkgName }}.{{ $t.Service.EndpointsDeclaration.Name }},
	req *jsonrpc.RawRequest,
	legacy bool{{ range $pathParams }},
	{{ .VarName }} {{ .TypeRef }}{{ end }},
) *jsonrpc.Response {
	switch req.Method {
	case "initialize":
		if !legacy {
			return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.MethodNotFound, "Method not found", nil)
		}
		req.Method = "server/discover"
	case "ping":
		if !legacy {
			return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.MethodNotFound, "Method not found", nil)
		}
		return jsonrpc.MakeSuccessResponse(req.ID, struct{}{})
	}
	var mode mcpLegacyMode
	{{- if and .Legacy .Legacy.OutputSchemas }}
	if legacy && req.Method == "tools/call" {
		var params struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InvalidParams, err.Error(), nil)
		}
		switch params.Name {
		{{- range $name, $schema := .Legacy.OutputSchemas }}
		case {{ printf "%q" $name }}:
			mode.wrapOutput = true
		{{- end }}
		}
	}
	{{- end }}
	switch req.Method {
	{{- range $t.Endpoints }}
	case {{ printf "%q" .Method.Name }}:
		{{- if and $.SubscriptionSource (eq .Method.Name "subscriptions/listen") }}
		return mcpruntime.ServeStdioSubscriptions(ctx, req, func(ctx context.Context) *jsonrpc.Response {
			return callStdio{{ .Method.VarName }}(ctx, endpoints, req, legacy, mode{{ range $pathParams }}, {{ .VarName }}{{ end }})
		})
		{{- else }}
		return callStdio{{ .Method.VarName }}(ctx, endpoints, req, legacy, mode{{ range $pathParams }}, {{ .VarName }}{{ end }})
		{{- end }}
	{{- end }}
	default:
		return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.MethodNotFound, "Method not found", nil)
	}
}

{{- range $t.Endpoints }}

func callStdio{{ .Method.VarName }}(
	ctx context.Context,
	endpoints *{{ $t.Service.PkgName }}.{{ $t.Service.EndpointsDeclaration.Name }},
	req *jsonrpc.RawRequest,
	legacy bool,
	mode mcpLegacyMode{{ range $pathParams }},
	{{ .VarName }} {{ .TypeRef }}{{ end }},
) *jsonrpc.Response {
	ctx = context.WithValue(ctx, goa.MethodKey, {{ printf "%q" .Method.Name }})
	ctx = context.WithValue(ctx, goa.ServiceKey, {{ printf "%q" .ServiceName }})
	var body {{ if .Payload.Request.ServerBody.Declaration }}{{ .Payload.Request.ServerBody.Declaration.Name }}{{ else }}{{ .Payload.Request.ServerBody.VarName }}{{ end }}
	if err := json.NewDecoder(bytes.NewReader(req.Params)).Decode(&body); err != nil {
		if errors.Is(err, io.EOF) {
			err = goa.MissingPayloadError()
		} else {
			var gerr *goa.ServiceError
			if !errors.As(err, &gerr) {
				err = goa.DecodePayloadError(err.Error())
			}
		}
		return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InvalidParams, err.Error(), nil)
	}
	{{- if and .Payload.Request.ServerBody.ValidatorDeclaration .Payload.Request.ServerBody.ValidationTarget }}
	if err := {{ .Payload.Request.ServerBody.ValidatorDeclaration.Name }}({{ .Payload.Request.ServerBody.ValidationTarget }}); err != nil {
		return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InvalidParams, err.Error(), nil)
	}
	{{- end }}
	{{- range $i, $arg := .Payload.Request.PayloadInit.ServerArgs }}
	{{- if gt $i (len $pathParams) }}
	var {{ $arg.VarName }} {{ $arg.TypeRef }}
	{{- end }}
	{{- end }}
	params := {{ .Payload.Request.PayloadInit.Declaration.Name }}({{ range $i, $arg := .Payload.Request.PayloadInit.ServerArgs }}{{ if $i }}, {{ end }}{{ $arg.Ref }}{{ end }})
	res, err := endpoints.{{ .Method.VarName }}(ctx, params)
	if err != nil {
		{{- if .Errors }}
		var en goa.GoaErrorNamer
		if errors.As(err, &en) {
			switch en.GoaErrorName() {
			{{- range $gerr := .Errors }}
			{{- range $err := $gerr.Errors }}
			case {{ printf "%q" $err.Name }}:
				var res {{ $err.Ref }}
				if !errors.As(err, &res) {
					panic("JSON-RPC error name does not match its generated service error type")
				}
				body := {{ (index $err.Response.ServerBody 0).Init.Declaration.Name }}({{ range $i, $arg := (index $err.Response.ServerBody 0).Init.ServerArgs }}{{ if $i }}, {{ end }}{{ $arg.Ref }}{{ end }})
				{{- if eq $err.Name "missing_client_capability" }}
				return jsonrpc.MakeErrorResponse(req.ID, {{ $err.Response.Code }}, err.Error(), body)
				{{- else }}
				data := struct {
					Name string `json:"name"`
					Body {{ (index $err.Response.ServerBody 0).Ref }} `json:"body"`
				}{
					Name: {{ printf "%q" $err.Name }},
					Body: body,
				}
				return jsonrpc.MakeErrorResponse(req.ID, {{ $err.Response.Code }}, err.Error(), data)
				{{- end }}
			{{- end }}
			{{- end }}
			}
		}
		{{- end }}
		return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, err.Error(), nil)
	}
	bodyOut := {{ (index (index .Result.Responses 0).ServerBody 0).Init.Declaration.Name }}(res.({{ .Result.Ref }}))
	var result any = bodyOut
	if legacy {
		var legacyErr error
		result, legacyErr = mcpLegacyResult(bodyOut, mode)
		if legacyErr != nil {
			var failure *mcpruntime.Error
			if errors.As(legacyErr, &failure) {
				return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.Code(failure.Code), failure.Message, failure.Data)
			}
			return jsonrpc.MakeErrorResponse(req.ID, jsonrpc.InternalError, legacyErr.Error(), nil)
		}
	}
	return jsonrpc.MakeSuccessResponse(req.ID, result)
}
{{- end }}
