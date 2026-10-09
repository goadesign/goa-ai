{{ printf "%s registers the guarded MCP server at its authored HTTP paths." .Transport.MountServerDeclaration.Name | comment }}
func {{ .Transport.MountServerDeclaration.Name }}(mux goahttp.Muxer, h *{{ .Transport.ServerStructDeclaration.Name }}) {
    {{- range (index .Transport.Endpoints 0).Routes }}
    mux.Handle("{{ .Verb }}", "{{ .Path }}", h.ServeHTTP)
    mux.Handle("GET", "{{ .Path }}", h.ServeHTTP)
    mux.Handle("DELETE", "{{ .Path }}", h.ServeHTTP)
    {{- end }}
    {{- with .ResourcePolicy }}
    h.resourceServer.MountMetadata(mux, []string{ {{ range index .BasicScopes 0 }}{{ printf "%q" . }}, {{ end }} })
    {{- end }}
}

{{ printf "%s registers this guarded MCP server at its authored HTTP paths." .Transport.MountServerDeclaration.Name | comment }}
func (s *{{ .Transport.ServerStructDeclaration.Name }}) {{ .Transport.MountServerDeclaration.Name }}(mux goahttp.Muxer) {
    {{ .Transport.MountServerDeclaration.Name }}(mux, s)
}

// mcpResponseWriter records whether the JSON-RPC handler wrote a response.
type mcpResponseWriter struct {
	http.ResponseWriter
	written bool
}

// withMCPTransport enforces the HTTP rules that MCP adds to JSON-RPC.
func withMCPTransport(h *{{ .Transport.ServerStructDeclaration.Name }}, {{ if and .ResourcePolicy .ResourcePolicy.Operations }}mux goahttp.Muxer, {{ end }}origins []string, next http.HandlerFunc) http.HandlerFunc {
    allowedOrigins := make(map[string]struct{}, len(origins))
    for _, origin := range origins {
        allowedOrigins[origin] = struct{}{}
    }
    bindings := map[string][]mcpruntime.HeaderBinding{
        {{- range .Tools }}
        {{- if .Headers }}
        {{ printf "%q" .Name }}: {
            {{- range .Headers }}
            {Name: {{ printf "%q" .Name }}, Type: {{ printf "%q" .Type }}, Path: []string{ {{ range .Path }}{{ printf "%q" . }}, {{ end }} }},
            {{- end }}
        },
        {{- end }}
        {{- end }}
    }
	return func(w http.ResponseWriter, r *http.Request) {
		if !mcpOriginAllowed(r, allowedOrigins) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

        if r.Method != http.MethodPost {
            {{- with .ResourcePolicy }}
            r = h.resourceServer.AuthorizeHTTP(w, r, [][]string{ {{ range .BasicScopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} })
            if r == nil {
                return
            }
            {{- end }}
            w.WriteHeader(http.StatusMethodNotAllowed)
            return
        }

		originalBody := r.Body
		body, readErr := io.ReadAll(originalBody)
		closeErr := originalBody.Close()
		if err := errors.Join(readErr, closeErr); err != nil {
			h.errhandler(r.Context(), w, fmt.Errorf("read MCP request body: %w", err))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(body))
        if failure := mcpruntime.ValidateHTTPRequest(r, body, bindings); failure != nil {
            {{- with .ResourcePolicy }}
            r = h.resourceServer.AuthorizeHTTP(w, r, [][]string{ {{ range .BasicScopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} })
            if r == nil {
                return
            }
            {{- end }}
            if err := mcpruntime.WriteProtocolError(w, body, failure); err != nil {
                h.errhandler(r.Context(), w, err)
            }
            return
        }
        var request jsonrpc.RawRequest
        if err := request.UnmarshalJSON(body); err != nil {
            h.errhandler(r.Context(), w, err)
            return
        }
        {{- with .ResourcePolicy }}
        scopes := [][]string{ {{ range .BasicScopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }
        {{- if .Operations }}
        if request.HasID {
            selected, err := {{ .SelectScopesDeclaration.Name }}(r, &request, mux, h.decoder)
            if err != nil {
                r = h.resourceServer.AuthorizeHTTP(w, r, scopes)
                if r == nil {
                    return
                }
                failure := &mcpruntime.Error{Code: mcpruntime.JSONRPCInvalidParams, Message: "Invalid params"}
                if err := mcpruntime.WriteProtocolError(w, body, failure); err != nil {
                    h.errhandler(r.Context(), w, err)
                }
                return
            }
            scopes = selected
        }
        {{- end }}
        {{- if .Catalogs }}
        if request.HasID {
            switch request.Method {
            {{- range $method, $scopes := .Catalogs }}
            case {{ printf "%q" $method }}:
                scopes = [][]string{ {{ range $scopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }
            {{- end }}
            }
        }
        {{- end }}
        {{- with .Subscription }}
        if request.HasID && request.Method == "subscriptions/listen" {
            scopes = [][]string{ {{ range . }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }
        }
        {{- end }}
        r = h.resourceServer.AuthorizeHTTP(w, r, scopes)
        if r == nil {
            return
        }
        {{- end }}
        if !request.HasID {
            w.WriteHeader(http.StatusAccepted)
            return
        }
        switch request.Method {
        {{- range .Transport.Endpoints }}
        case {{ printf "%q" .Method.Name }}:
        {{- end }}
        default:
            failure := &mcpruntime.Error{Code: mcpruntime.JSONRPCMethodNotFound, Message: "Method not found"}
            if err := mcpruntime.WriteProtocolError(w, body, failure); err != nil {
                h.errhandler(r.Context(), w, err)
            }
            return
        }

		response := &mcpResponseWriter{ResponseWriter: w}
        {{- if .SubscriptionSource }}
        if request.Method == "subscriptions/listen" {
            id, err := json.Marshal(request.ID)
            if err != nil {
                h.errhandler(r.Context(), response, err)
                return
            }
            if err := mcpruntime.ServeSubscriptions(response, r, id, request.Params, next); err != nil {
                var failure *mcpruntime.Error
                if !response.written && errors.As(err, &failure) {
                    if err := mcpruntime.WriteProtocolError(response, body, failure); err != nil {
                        h.errhandler(r.Context(), response, err)
                    }
                } else {
                    h.errhandler(r.Context(), response, err)
                }
            }
            return
        }
        {{- end }}
		if err := mcpruntime.ServeProgress(response, r, request.Params, next); err != nil {
			h.errhandler(r.Context(), response, err)
		}
		if !response.written {
			w.WriteHeader(http.StatusAccepted)
		}
	}
}

{{- if and .ResourcePolicy .ResourcePolicy.Operations }}
// {{ .ResourcePolicy.SelectScopesDeclaration.Name }} decodes the same native
// request that the endpoint receives and selects its authored scope alternatives.
// It does not call middleware, authentication callbacks or service methods.
func {{ .ResourcePolicy.SelectScopesDeclaration.Name }}(r *http.Request, request *jsonrpc.RawRequest, mux goahttp.Muxer, decoder func(*http.Request) goahttp.Decoder) ([][]string, error) {
    switch request.Method {
    {{- range .Transport.Endpoints }}
    {{- if index $.ResourcePolicy.Operations .Method.Name }}
    case {{ printf "%q" .Method.Name }}:
        p, err := {{ .RequestDecoderDeclaration.Name }}(mux, decoder)(r.Clone(r.Context()), request)
        if err != nil {
            return nil, err
        }
        {{- if eq .Method.Name "tools/call" }}
        switch p.Name {
        {{- range $name, $scopes := $.ResourcePolicy.Tools }}
        case {{ printf "%q" $name }}:
            return [][]string{ {{ range $scopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }, nil
        {{- end }}
        }
        {{- else if eq .Method.Name "prompts/get" }}
        switch p.Name {
        {{- range $name, $scopes := $.ResourcePolicy.Prompts }}
        case {{ printf "%q" $name }}:
            return [][]string{ {{ range $scopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }, nil
        {{- end }}
        }
        {{- else if eq .Method.Name "resources/read" }}
        switch p.URI {
        {{- range $name, $scopes := $.ResourcePolicy.Resources }}
        case {{ printf "%q" $name }}:
            return [][]string{ {{ range $scopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }, nil
        {{- end }}
        {{- with $.ResourcePolicy.ResourceReader }}
        default:
            return [][]string{ {{ range . }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }, nil
        {{- end }}
        }
        {{- else if eq .Method.Name "completion/complete" }}
        switch {
        {{- range $.ResourcePolicy.Completions }}
        {{- if eq .Type "ref/prompt" }}
        case p.Ref.Type == "ref/prompt" && p.Ref.Name != nil && *p.Ref.Name == {{ printf "%q" .Reference }} && p.Argument.Name == {{ printf "%q" .Argument }}:
        {{- else }}
        case p.Ref.Type == "ref/resource" && p.Ref.URI != nil && *p.Ref.URI == {{ printf "%q" .Reference }} && p.Argument.Name == {{ printf "%q" .Argument }}:
        {{- end }}
            return [][]string{ {{ range .Scopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }, nil
        {{- end }}
        }
        {{- end }}
    {{- end }}
    {{- end }}
    }
    return [][]string{ {{ range .ResourcePolicy.BasicScopes }}{ {{ range . }}{{ printf "%q" . }}, {{ end }} }, {{ end }} }, nil
}
{{- end }}

// mcpOriginAllowed reports whether the request omits Origin or names an origin
// the application allowed when it constructed the server.
func mcpOriginAllowed(r *http.Request, allowedOrigins map[string]struct{}) bool {
	origins := r.Header.Values("Origin")
	if len(origins) == 0 {
		return true
	}
	if len(origins) != 1 || origins[0] == "" {
		return false
	}
	_, ok := allowedOrigins[origins[0]]
	return ok
}

// WriteHeader records that the JSON-RPC handler selected an HTTP status.
func (w *mcpResponseWriter) WriteHeader(statusCode int) {
	w.written = true
	w.ResponseWriter.WriteHeader(statusCode)
}

// Write records that the JSON-RPC handler wrote a response body.
func (w *mcpResponseWriter) Write(data []byte) (int, error) {
	w.written = true
	return w.ResponseWriter.Write(data)
}

// Unwrap gives HTTP response control access to the underlying network writer.
func (w *mcpResponseWriter) Unwrap() http.ResponseWriter {
    return w.ResponseWriter
}
