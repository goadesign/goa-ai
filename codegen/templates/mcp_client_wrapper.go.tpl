// NewEndpoints creates endpoints that expose the original service API while
// invoking the MCP transport under the hood for mapped methods. Unmapped
// methods transparently fall back to the original JSON-RPC transport.
// NewEndpoints creates an Endpoints set that routes mapped methods through
// the MCP transport while leaving unmapped methods on the original transport.
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
        // Encode original payload to raw JSON using Goa encoder (no JSON-RPC envelope)
        var args []byte
        {
            var payload any
            {{- if .HasPayload }}
            payload = v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
            {{- else }}
            payload = struct{}{}
            {{- end }}
            reqArgs, _ := http.NewRequestWithContext(ctx, http.MethodPost, "", nil)
            if err := enc(reqArgs).Encode(payload); err != nil { return nil, err }
            b, err := io.ReadAll(reqArgs.Body)
            if err != nil { return nil, err }
            args = b
        }

        // Call MCP tools/call via transport endpoint (SSE stream)
        streamAny, err := mcpC.ToolsCall()(ctx, &{{ $.MCPPkgAlias }}.ToolsCallPayload{Name: "{{ .Name }}", Arguments: args})
        if err != nil {
            prompt := retry.BuildRepairPrompt("tools/call:{{ .Name }}", err.Error(), {{ printf "%q" .ExampleArguments }}, {{ printf "%q" .InputSchema }})
            return nil, &retry.RetryableError{Prompt: prompt, Cause: err}
        }
        stream, ok := streamAny.(*{{ $.MCPJSONRPCCAlias }}.ToolsCallClientStream)
        if !ok {
            return nil, fmt.Errorf("unexpected stream type for {{ .Name }}")
        }
        var r *{{ $.MCPPkgAlias }}.ToolsCallResult
        for {
            ev, recvErr := stream.Recv(ctx)
            if recvErr == io.EOF {
                break
            }
            if recvErr != nil {
                prompt := retry.BuildRepairPrompt("tools/call:{{ .Name }}", recvErr.Error(), {{ printf "%q" .ExampleArguments }}, {{ printf "%q" .InputSchema }})
                return nil, &retry.RetryableError{Prompt: prompt, Cause: recvErr}
            }
            r = ev
        }
        if r == nil || r.Content == nil || len(r.Content) == 0 || r.Content[0] == nil || r.Content[0].Text == nil {
            prompt := retry.BuildRepairPrompt("tools/call:{{ .Name }}", "empty MCP tool response", {{ printf "%q" .ExampleArguments }}, {{ printf "%q" .InputSchema }})
            return nil, &retry.RetryableError{Prompt: prompt, Cause: fmt.Errorf("empty MCP tool response for {{ .Name }}")}
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
        // Forward original payload parameters via URI query string when applicable
        uri := "{{ .URI }}"
        {
            var payload any
            {{- if .HasPayload }}
            payload = v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
            {{- else }}
            payload = struct{}{}
            {{- end }}
            reqArgs, _ := http.NewRequestWithContext(ctx, http.MethodPost, "", nil)
            if err := enc(reqArgs).Encode(payload); err == nil {
                b, rerr := io.ReadAll(reqArgs.Body)
                if rerr == nil && len(b) > 0 {
                    var m map[string]any
                    if jerr := json.Unmarshal(b, &m); jerr == nil && len(m) > 0 {
                        var keys []string
                        for k := range m { keys = append(keys, k) }
                        sort.Strings(keys)
                        var q []string
                        for _, k := range keys {
                            switch vv := m[k].(type) {
                            case []any:
                                for _, e := range vv { q = append(q, fmt.Sprintf("%s=%v", url.QueryEscape(k), url.QueryEscape(fmt.Sprint(e)))) }
                            default:
                                q = append(q, fmt.Sprintf("%s=%v", url.QueryEscape(k), url.QueryEscape(fmt.Sprint(vv))))
                            }
                        }
                        if len(q) > 0 { uri = uri + "?" + strings.Join(q, "&") }
                    }
                }
            }
        }
        ires, err := mcpC.ResourcesRead()(ctx, &{{ $.MCPPkgAlias }}.ResourcesReadPayload{URI: uri})
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
            var payload any
            {{- if .HasPayload }}
            payload = v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
            {{- else }}
            payload = struct{}{}
            {{- end }}
            reqArgs, _ := http.NewRequestWithContext(ctx, http.MethodPost, "", nil)
            if err := enc(reqArgs).Encode(payload); err != nil { return nil, err }
            b, err := io.ReadAll(reqArgs.Body)
            if err != nil { return nil, err }
            args = b
        }
        ires, err := mcpC.PromptsGet()(ctx, &{{ $.MCPPkgAlias }}.PromptsGetPayload{Name: "{{ .Name }}", Arguments: args})
        if err != nil {
            prompt := retry.BuildRepairPrompt("prompts/get:{{ .Name }}", err.Error(), {{ printf "%q" .ExampleArguments }}, "")
            return nil, &retry.RetryableError{Prompt: prompt, Cause: err}
        }
        r := ires.(*{{ $.MCPPkgAlias }}.PromptsGetResult)
        if r == nil || r.Messages == nil || len(r.Messages) == 0 || r.Messages[0] == nil || r.Messages[0].Content == nil || r.Messages[0].Content.Text == nil {
            prompt := retry.BuildRepairPrompt("prompts/get:{{ .Name }}", "empty MCP prompt response", {{ printf "%q" .ExampleArguments }}, "")
            return nil, &retry.RetryableError{Prompt: prompt, Cause: fmt.Errorf("empty MCP prompt response for {{ .Name }}")}
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
    origClient := {{ $.SvcJSONRPCCAlias }}.NewClient(scheme, host, doer, enc, dec, restore)
    return {{ .ServicePkg }}.NewClient(
        {{- range $i, $method := .AllMethods }}
        {{- if $method.IsMapped }}
        e.{{ $method.Name }},
        {{- else }}
        origClient.{{ $method.Name }}(),
        {{- end }}
        {{- end }}
    )
}


