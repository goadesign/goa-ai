// NewEndpoints returns the original service endpoints backed by an MCP client.
func NewEndpoints(
    scheme string,
    host string,
    doer goahttp.Doer,
    enc func(*http.Request) goahttp.Encoder,
    dec func(*http.Response) goahttp.Decoder,
    restore bool,
) *{{ .ServicePkg }}.Endpoints {
    // Transport clients
    {{- if .NeedsMCPClient }}
    mcpC := {{ .MCPJSONRPCCAlias }}.NewClient(scheme, host, doer, enc, dec, restore)
    {{- end }}
    // Build endpoints matching the original service
    e := &{{ .ServicePkg }}.Endpoints{}

    {{- range .Tools }}
    // Tool: {{ .Name }} -> {{ .ServiceMethodName }}
    e.{{ .ServiceMethodName }} = func(ctx context.Context, v any) (any, error) {
        {{- if .HasPayload }}
        args, err := {{ $.CodecPackage }}.{{ .Codec.PayloadEncode }}(v.({{ .PayloadType }}))
        if err != nil {
            return nil, err
        }
        {{- else }}
        args := []byte("{}")
        {{- end }}

        // Call the MCP tool and read its single result.
        ires, err := mcpC.ToolsCall()(ctx, &{{ $.MCPPkgAlias }}.ToolsCallPayload{Name: "{{ .Name }}", Arguments: args})
        if err != nil {
            return nil, err
        }
        r := ires.(*{{ $.MCPPkgAlias }}.ToolsCallResult)
        if r == nil || r.Content == nil || len(r.Content) == 0 || r.Content[0] == nil || r.Content[0].Text == nil {
            return nil, fmt.Errorf("empty MCP tool response for {{ .Name }}")
        }
        {{- if .HasResult }}
        return {{ $.CodecPackage }}.{{ .Codec.ResultDecode }}([]byte(*r.Content[0].Text))
        {{- else }}
        return nil, nil
        {{- end }}
    }
    {{- end }}

    {{- range .Resources }}
    // Resource: {{ .URI }} -> {{ .ServiceMethodName }}
    e.{{ .ServiceMethodName }} = func(ctx context.Context, v any) (any, error) {
        // Forward original payload parameters via URI query string when applicable
        uri := "{{ .URI }}"
        {{- if .HasPayload }}
        payload := v.({{ .PayloadType }})
        query := url.Values{}
        {{- range .QueryFields }}
        {{- if .Repeated }}
        for _, value := range payload.{{ .ClientSelector }} {
            query.Add({{ printf "%q" .QueryKey }}, {{ queryValueExpr .FormatKind "value" }})
        }
        {{- else if .ClientPointer }}
        if payload.{{ .ClientSelector }} != nil {
            query.Add({{ printf "%q" .QueryKey }}, {{ queryValueExpr .FormatKind (printf "*payload.%s" .ClientSelector) }})
        }
        {{- else if .Optional }}
        {{- if eq .FormatKind "string" }}
        if payload.{{ .ClientSelector }} != "" {
        {{- else if eq .FormatKind "bool" }}
        if payload.{{ .ClientSelector }} {
        {{- else }}
        if payload.{{ .ClientSelector }} != 0 {
        {{- end }}
            query.Add({{ printf "%q" .QueryKey }}, {{ queryValueExpr .FormatKind (printf "payload.%s" .ClientSelector) }})
        }
        {{- else }}
        query.Add({{ printf "%q" .QueryKey }}, {{ queryValueExpr .FormatKind (printf "payload.%s" .ClientSelector) }})
        {{- end }}
        {{- end }}
        if encoded := query.Encode(); encoded != "" {
            uri = uri + "?" + encoded
        }
        {{- end }}
        ires, err := mcpC.ResourcesRead()(ctx, &{{ $.MCPPkgAlias }}.ResourcesReadPayload{URI: uri})
        if err != nil {
            return nil, err
        }
        rr := ires.(*{{ $.MCPPkgAlias }}.ResourcesReadResult)
        if rr == nil || rr.Contents == nil || len(rr.Contents) == 0 || rr.Contents[0] == nil || rr.Contents[0].Text == nil {
            return nil, fmt.Errorf("empty MCP resource response for {{ .URI }}")
        }
        {{- if .HasResult }}
        return {{ $.CodecPackage }}.{{ .Codec.ResultDecode }}([]byte(*rr.Contents[0].Text))
        {{- else }}
        return nil, nil
        {{- end }}
    }
    {{- end }}

    {{- range .DynamicPrompts }}
    // Dynamic Prompt: {{ .Name }} -> {{ .ServiceMethodName }}
    e.{{ .ServiceMethodName }} = func(ctx context.Context, v any) (any, error) {
        {{- if .HasPayload }}
        args, err := {{ $.CodecPackage }}.{{ .Codec.PayloadEncode }}(v.({{ .PayloadType }}))
        if err != nil {
            return nil, err
        }
        {{- else }}
        args := []byte("{}")
        {{- end }}
        ires, err := mcpC.PromptsGet()(ctx, &{{ $.MCPPkgAlias }}.PromptsGetPayload{Name: "{{ .Name }}", Arguments: args})
        if err != nil {
            return nil, err
        }
        r := ires.(*{{ $.MCPPkgAlias }}.PromptsGetResult)
        if r == nil || r.Messages == nil || len(r.Messages) == 0 || r.Messages[0] == nil || r.Messages[0].Content == nil || r.Messages[0].Content.Text == nil {
            return nil, fmt.Errorf("empty MCP prompt response for {{ .Name }}")
        }
        return {{ $.CodecPackage }}.{{ .Codec.ResultDecode }}([]byte(*r.Messages[0].Content.Text))
    }
    {{- end }}

    {{- range .Notifications }}
    // Notification: {{ .Name }} -> {{ .ServiceMethodName }}
    e.{{ .ServiceMethodName }} = func(ctx context.Context, v any) (any, error) {
        // The generated codec checks the original service payload before it is sent.
        params, err := {{ $.CodecPackage }}.{{ .Codec.PayloadEncode }}(v.({{ .PayloadType }}))
        if err != nil {
            return nil, err
        }

        req, err := mcpC.{{ .RequestBuilderName }}(ctx, nil)
        if err != nil {
            return nil, err
        }
        body := &jsonrpc.Request{
            JSONRPC: "2.0",
            Method: "{{ .WireMethodName }}",
            Params:  json.RawMessage(params),
            ID:      uuid.New().String(),
        }
        if err := enc(req).Encode(body); err != nil {
            return nil, goahttp.ErrEncodingError("{{ $.MCPPackage }}", "{{ .WireMethodName }}", err)
        }
        resp, err := doer.Do(req)
        if err != nil {
            return nil, goahttp.ErrRequestError("{{ $.MCPPackage }}", "{{ .WireMethodName }}", err)
        }
        _, err = {{ $.MCPJSONRPCCAlias }}.{{ .ResponseDecoderName }}(dec, false)(resp)
        return nil, err
    }
    {{- end }}

    return e
}

// NewClient returns *{{ .ServicePkg }}.Client using MCP-backed endpoints.
func NewClient(
    scheme string,
    host string,
    doer goahttp.Doer,
    enc func(*http.Request) goahttp.Encoder,
    dec func(*http.Response) goahttp.Decoder,
    restore bool,
) *{{ .ServicePkg }}.Client {
    {{- if .AllMethods }}
    e := NewEndpoints(scheme, host, doer, enc, dec, restore)
    return {{ .ServicePkg }}.NewClient(
        {{- range $i, $method := .AllMethods }}
        e.{{ $method }},
        {{- end }}
    )
    {{- else }}
    return {{ .ServicePkg }}.NewClient()
    {{- end }}
}
