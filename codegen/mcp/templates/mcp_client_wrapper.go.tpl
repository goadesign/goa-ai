// encodeOriginalPayload serializes an original-service payload without a
// JSON-RPC envelope so MCP tool and prompt calls can forward raw arguments.
func encodeOriginalPayload(
    ctx context.Context,
    enc func(*http.Request) goahttp.Encoder,
    payload any,
) ([]byte, error) {
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, "", nil)
    if err != nil {
        return nil, err
    }
    if err := enc(req).Encode(payload); err != nil {
        return nil, err
    }
    return io.ReadAll(req.Body)
}

{{- if .NeedsOriginalClient }}
// decodeOriginalJSONRPCResult rehydrates one MCP result as the original
// service's JSON-RPC response shape and decodes it with Goa's generated client.
func decodeOriginalJSONRPCResult(
    enc func(*http.Request) goahttp.Encoder,
    req *http.Request,
    result []byte,
    decode func(*http.Response) (any, error),
) (any, error) {
    raw := &jsonrpc.RawResponse{
        JSONRPC: "2.0",
        Result:  result,
    }
    if err := enc(req).Encode(raw); err != nil {
        return nil, err
    }
    bodyBytes, err := io.ReadAll(req.Body)
    if err != nil {
        return nil, err
    }
    resp := &http.Response{
        StatusCode: http.StatusOK,
        Body:       io.NopCloser(bytes.NewReader(bodyBytes)),
    }
    return decode(resp)
}
{{- end }}

// NewEndpoints creates endpoints that expose the original service API while
// invoking the MCP transport under the hood.
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
    {{- if .NeedsOriginalClient }}
    origC := {{ .SvcJSONRPCCAlias }}.NewClient(scheme, host, doer, enc, dec, restore)
    {{- end }}

    // Build endpoints matching the original service
    e := &{{ .ServicePkg }}.Endpoints{}

    {{- range .Tools }}
    // Tool: {{ .Name }} -> {{ .OriginalMethodName }}
    e.{{ .OriginalMethodName }} = func(ctx context.Context, v any) (any, error) {
        // Encode original payload to raw JSON using Goa encoder (no JSON-RPC envelope)
        var payload any
        {{- if .HasPayload }}
        payload = v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
        {{- else }}
        payload = struct{}{}
        {{- end }}
        args, err := encodeOriginalPayload(ctx, enc, payload)
        if err != nil {
            return nil, err
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
        req3, err := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
        if err != nil {
            return nil, err
        }
        decode := {{ $.SvcJSONRPCCAlias }}.Decode{{ .OriginalMethodName }}Response(dec, false)
        return decodeOriginalJSONRPCResult(enc, req3, []byte(*r.Content[0].Text), decode)
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
        {{- if .HasPayload }}
        payload := v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
        query := url.Values{}
        {{- range .QueryFields }}
        {{- if .GuardExpr }}
        if {{ .GuardExpr }} {
        {{- end }}
        {{- if .Repeated }}
            for _, value := range {{ .CollectionExpr }} {
                query.Add({{ printf "%q" .QueryKey }}, {{ queryValueExpr .FormatKind .ValueExpr }})
            }
        {{- else }}
            query.Add({{ printf "%q" .QueryKey }}, {{ queryValueExpr .FormatKind .ValueExpr }})
        {{- end }}
        {{- if .GuardExpr }}
        }
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
        // Build JSON-RPC response envelope and decode using Goa-generated decoder
        req3, err := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
        if err != nil {
            return nil, err
        }
        decode := {{ $.SvcJSONRPCCAlias }}.Decode{{ .OriginalMethodName }}Response(dec, false)
        return decodeOriginalJSONRPCResult(enc, req3, []byte(*rr.Contents[0].Text), decode)
        {{- else }}
        return nil, nil
        {{- end }}
    }
    {{- end }}

    {{- range .DynamicPrompts }}
    // Dynamic Prompt: {{ .Name }} -> {{ .OriginalMethodName }}
    e.{{ .OriginalMethodName }} = func(ctx context.Context, v any) (any, error) {
        var payload any
        {{- if .HasPayload }}
        payload = v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
        {{- else }}
        payload = struct{}{}
        {{- end }}
        args, err := encodeOriginalPayload(ctx, enc, payload)
        if err != nil {
            return nil, err
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
        req3, err := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
        if err != nil {
            return nil, err
        }
        decode := {{ $.SvcJSONRPCCAlias }}.Decode{{ .OriginalMethodName }}Response(dec, false)
        return decodeOriginalJSONRPCResult(enc, req3, []byte(*r.Messages[0].Content.Text), decode)
    }
    {{- end }}

    {{- range .Notifications }}
    // Notification: {{ .Name }} -> {{ .OriginalMethodName }}
    e.{{ .OriginalMethodName }} = func(ctx context.Context, v any) (any, error) {
        // Pure-MCP validation guarantees the original payload shape matches
        // the generated SendNotificationPayload contract.
        params, err := encodeOriginalPayload(ctx, enc, v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload))
        if err != nil {
            return nil, err
        }

        req, err := mcpC.Build{{ printf "notify_%s" .Name | goify }}Request(ctx, nil)
        if err != nil {
            return nil, err
        }
        body := &jsonrpc.Request{
            JSONRPC: "2.0",
            Method: "{{ printf "notify_%s" .Name }}",
            Params:  json.RawMessage(params),
            ID:      uuid.New().String(),
        }
        if err := enc(req).Encode(body); err != nil {
            return nil, goahttp.ErrEncodingError("{{ $.MCPPackage }}", "{{ printf "notify_%s" .Name }}", err)
        }
        resp, err := doer.Do(req)
        if err != nil {
            return nil, goahttp.ErrRequestError("{{ $.MCPPackage }}", "{{ printf "notify_%s" .Name }}", err)
        }
        _, err = {{ $.MCPJSONRPCCAlias }}.Decode{{ printf "notify_%s" .Name | goify }}Response(dec, false)(resp)
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
    e := NewEndpoints(scheme, host, doer, enc, dec, restore)
    return {{ .ServicePkg }}.NewClient(
        {{- range $i, $method := .AllMethods }}
        e.{{ $method }},
        {{- end }}
    )
}


