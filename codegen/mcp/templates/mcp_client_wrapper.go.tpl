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
// invoking the MCP transport under the hood for mapped methods.
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
    {{- if .NeedsMCPClient }}
    mcpC := {{ .MCPJSONRPCCAlias }}.NewClient(scheme, host, doer, enc, dec, restore)
    {{- end }}
    {{- if .Tools }}
    mcpCaller := {{ .MCPJSONRPCCAlias }}.NewCaller(mcpC, "")
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

        toolResp, err := mcpCaller.CallTool(ctx, mcpruntime.CallRequest{
            Tool: "{{ .Name }}",
            Payload: args,
        })
        if err != nil {
            prompt := retry.BuildRepairPrompt("tools/call:{{ .Name }}", err.Error(), {{ printf "%q" .ExampleArguments }}, {{ printf "%q" .InputSchema }})
            return nil, &retry.RetryableError{Prompt: prompt, Cause: err}
        }

        {{- if .HasResult }}
        if len(toolResp.Result) == 0 {
            prompt := retry.BuildRepairPrompt("tools/call:{{ .Name }}", "empty MCP tool response", {{ printf "%q" .ExampleArguments }}, {{ printf "%q" .InputSchema }})
            return nil, &retry.RetryableError{Prompt: prompt, Cause: fmt.Errorf("empty MCP tool response for {{ .Name }}")}
        }
        // Build JSON-RPC response envelope and decode using Goa-generated decoder
        req3, err := origC.Build{{ .OriginalMethodName }}Request(ctx, v)
        if err != nil {
            return nil, err
        }
        decode := {{ $.SvcJSONRPCCAlias }}.Decode{{ .OriginalMethodName }}Response(dec, false)
        return decodeOriginalJSONRPCResult(enc, req3, toolResp.Result, decode)
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
        payload := v.(*{{ $.ServicePkg }}.{{ .OriginalMethodName }}Payload)
        notificationPayload := &{{ $.MCPPkgAlias }}.SendNotificationPayload{
            Type: payload.Type,
            {{- if .HasData }}
            Data: payload.Data,
            {{- end }}
        }
        {{- if .HasMessage }}
        {{- if .MessagePointer }}
        notificationPayload.Message = payload.Message
        {{- else }}
        message := payload.Message
        notificationPayload.Message = &message
        {{- end }}
        {{- end }}
        _, err := mcpC.Notify{{ goify .Name }}()(ctx, notificationPayload)
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
    {{- $hasUnmapped := false -}}
    {{- range $i, $method := .AllMethods }}
    {{- if (not $method.IsMapped) }}
    {{- $hasUnmapped = true }}
    {{- end }}
    {{- end -}}
    {{- if $hasUnmapped }}
    origClient := {{ $.SvcJSONRPCCAlias }}.NewClient(scheme, host, doer, enc, dec, restore)
    {{- end }}
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
