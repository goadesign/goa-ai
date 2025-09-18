// NewEndpoints creates endpoints that expose the original service API while
// invoking the MCP transport under the hood for mapped methods. Unmapped
// methods transparently fall back to the original JSON-RPC transport.
func NewEndpoints(
    scheme string,
    host string,
    doer goahttp.Doer,
    enc func(*http.Request) goahttp.Encoder,
    dec func(*http.Response) goahttp.Decoder,
    restore bool,
) *{{ .ServicePkg }}.Endpoints {
    // Transport clients
    {{- $hasTools := gt (len .Tools) 0 -}}
    {{- $hasResources := gt (len .Resources) 0 -}}
    {{- $hasDynamic := gt (len .DynamicPrompts) 0 -}}
    {{- $needsClients := or $hasTools (or $hasResources $hasDynamic) -}}
    {{- if $needsClients }}
    mcpC := {{ .MCPJSONRPCCAlias }}.NewClient(scheme, host, doer, enc, dec, restore)
    origC := {{ .SvcJSONRPCCAlias }}.NewClient(scheme, host, doer, enc, dec, restore)
    {{- end }}

    // Build endpoints matching the original service
    e := &{{ .ServicePkg }}.Endpoints{}

    {{- range .Tools }}
    // Tool: {{ .Name }} -> {{ .OriginalMethodName }}
    e.{{ .OriginalMethodName }} = func(ctx context.Context, v any) (any, error) {
        // Encode original payload to JSON-RPC request using Goa encoder, then extract params
        var args []byte
        {
            req, err := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
            if err != nil {
                return nil, err
            }
            encReq := {{ $.SvcJSONRPCCAlias }}.Encode{{ .OriginalMethodName }}Request(enc)
            var payload any
            {{- if .HasPayload }}
            payload = v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
            {{- else }}
            payload = nil
            {{- end }}
            if err := encReq(req, payload); err != nil {
                return nil, err
            }
            b, err := io.ReadAll(req.Body)
            if err != nil {
                return nil, err
            }
            resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b))}
            var jr jsonrpc.Request
            if err := dec(resp).Decode(&jr); err != nil {
                return nil, err
            }
            // Re-encode params using Goa encoder to get raw bytes
            req2, _ := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
            if err := enc(req2).Encode(jr.Params); err != nil {
                return nil, err
            }
            args, err = io.ReadAll(req2.Body)
            if err != nil {
                return nil, err
            }
        }

        // Call MCP tools/call via transport endpoint
        ires, err := mcpC.ToolsCall()(ctx, &{{ $.MCPPkgAlias }}.ToolsCallPayload{Name: "{{ .Name }}", Arguments: args})
        if err != nil {
            return nil, err
        }
        r := ires.(*{{ $.MCPPkgAlias }}.ToolsCallResult)
        if r == nil || r.Content == nil || len(r.Content) == 0 || r.Content[0] == nil || r.Content[0].Text == nil {
            return nil, fmt.Errorf("empty MCP tool response for {{ .Name }}")
        }
        {{- if .HasResult }}
        // Build JSON-RPC response envelope and decode using Goa-generated decoder
        rr := &jsonrpc.RawResponse{JSONRPC: "2.0", Result: []byte(*r.Content[0].Text)}
        req3, _ := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
        if err := enc(req3).Encode(rr); err != nil {
            return nil, err
        }
        bodyBytes, err := io.ReadAll(req3.Body)
        if err != nil {
            return nil, err
        }
        resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(bodyBytes))}
        decode := {{ $.SvcJSONRPCCAlias }}.Decode{{ .OriginalMethodName }}Response(dec, false)
        return decode(resp)
        {{- else }}
        return nil, nil
        {{- end }}
    }
    {{- end }}

    {{- range .Resources }}
    // Resource: {{ .URI }} -> {{ .OriginalMethodName }}
    e.{{ .OriginalMethodName }} = func(ctx context.Context, v any) (any, error) {
        // Resources in MCP don't support parameters, only URI
        ires, err := mcpC.ResourcesRead()(ctx, &{{ $.MCPPkgAlias }}.ResourcesReadPayload{URI: "{{ .URI }}"})
        if err != nil {
            return nil, err
        }
        rr := ires.(*{{ $.MCPPkgAlias }}.ResourcesReadResult)
        if rr == nil || rr.Contents == nil || len(rr.Contents) == 0 || rr.Contents[0] == nil || rr.Contents[0].Text == nil {
            return nil, fmt.Errorf("empty MCP resource response for {{ .URI }}")
        }
        {{- if .HasResult }}
        // Build JSON-RPC response envelope and decode using Goa-generated decoder
        jrr := &jsonrpc.RawResponse{JSONRPC: "2.0", Result: []byte(*rr.Contents[0].Text)}
        req3, _ := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
        if err := enc(req3).Encode(jrr); err != nil {
            return nil, err
        }
        bodyBytes, err := io.ReadAll(req3.Body)
        if err != nil {
            return nil, err
        }
        resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(bodyBytes))}
        decode := {{ $.SvcJSONRPCCAlias }}.Decode{{ .OriginalMethodName }}Response(dec, false)
        return decode(resp)
        {{- else }}
        return nil, nil
        {{- end }}
    }
    {{- end }}

    {{- range .DynamicPrompts }}
    // Dynamic Prompt: {{ .Name }} -> {{ .OriginalMethodName }}
    e.{{ .OriginalMethodName }} = func(ctx context.Context, v any) (any, error) {
        var args []byte
        {
            req, err := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
            if err != nil {
                return nil, err
            }
            encReq := {{ $.SvcJSONRPCCAlias }}.Encode{{ .OriginalMethodName }}Request(enc)
            var payload any
            {{- if .HasPayload }}
            payload = v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
            {{- else }}
            payload = nil
            {{- end }}
            if err := encReq(req, payload); err != nil {
                return nil, err
            }
            b, err := io.ReadAll(req.Body)
            if err != nil {
                return nil, err
            }
            resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(b))}
            var jr jsonrpc.Request
            if err := dec(resp).Decode(&jr); err != nil {
                return nil, err
            }
            req2, _ := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
            if err := enc(req2).Encode(jr.Params); err != nil {
                return nil, err
            }
            args, err = io.ReadAll(req2.Body)
            if err != nil {
                return nil, err
            }
        }
        ires, err := mcpC.PromptsGet()(ctx, &{{ $.MCPPkgAlias }}.PromptsGetPayload{Name: "{{ .Name }}", Arguments: args})
        if err != nil {
            return nil, err
        }
        r := ires.(*{{ $.MCPPkgAlias }}.PromptsGetResult)
        if r == nil || r.Messages == nil || len(r.Messages) == 0 || r.Messages[0] == nil || r.Messages[0].Content == nil || r.Messages[0].Content.Text == nil {
            return nil, fmt.Errorf("empty MCP prompt response for {{ .Name }}")
        }
        // Build JSON-RPC response envelope and decode using Goa-generated decoder
        jrr := &jsonrpc.RawResponse{JSONRPC: "2.0", Result: []byte(*r.Messages[0].Content.Text)}
        req3, _ := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
        if err := enc(req3).Encode(jrr); err != nil {
            return nil, err
        }
        bodyBytes, err := io.ReadAll(req3.Body)
        if err != nil { return nil, err }
        resp := &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(bodyBytes))}
        decode := {{ $.SvcJSONRPCCAlias }}.Decode{{ .OriginalMethodName }}Response(dec, false)
        return decode(resp)
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
    e := NewEndpoints(scheme, host, doer, enc, dec, restore)
    return {{ .ServicePkg }}.NewClient(
        {{- range $i, $method := .AllMethods }}
        {{- if $method.IsMapped }}
        e.{{ $method.Name }},
        {{- else }}
        {{ $.SvcJSONRPCCAlias }}.NewClient(scheme, host, doer, enc, dec, restore).{{ $method.Name }}(),
        {{- end }}
        {{- end }}
    )
}

