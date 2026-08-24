{{- if .Tools }}
{{ comment "Tools handling" }}

func (a *MCPAdapter) ToolsList(ctx context.Context, p *ToolsListPayload) (*ToolsListResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "tools/list"})
    tools := []*ToolInfo{
        {{- range .Tools }}
        {
            Name: {{ quote .Name }},
            Description: stringPtr({{ quote .Description }}),
            {{- if .InputSchema }}
            InputSchema: json.RawMessage(`{{ .InputSchema }}`),
            {{- else }}
            InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
            {{- end }}
        },
        {{- end }}
    }
    res := &ToolsListResult{Tools: tools}
    a.log(ctx, "response", map[string]any{"method": "tools/list"})
    return res, nil
}

func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload) (*ToolsCallResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "tools/call", "name": p.Name})
    switch p.Name {
    {{- range .Tools }}
    case {{ quote .Name }}:
        {{- if .HasPayload }}
        arguments := p.Arguments
        if len(arguments) == 0 {
            arguments = json.RawMessage("{}")
        }
        payload, err := {{ $.CodecPackage }}.{{ .Codec.PayloadDecode }}(arguments)
        if err != nil {
            return nil, goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- else }}
        if err := validateNoArguments(p.Arguments); err != nil {
            return nil, goa.PermanentError("invalid_params", "tool %q does not accept arguments: %s", p.Name, err)
        }
        {{- end }}
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.service.{{ .ServiceMethodName }}(ctx, payload)
        {{- else }}
        result, err := a.service.{{ .ServiceMethodName }}(ctx)
        {{- end }}
        if err != nil {
            return nil, a.mapError(err)
        }
        encoded, err := {{ $.CodecPackage }}.{{ .Codec.ResultEncode }}(result)
        if err != nil {
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        s := string(encoded)
        final := &ToolsCallResult{
            Content: []*ContentItem{
                buildContentItem(a, s),
            },
        }
        a.log(ctx, "response", map[string]any{"method": "tools/call", "name": p.Name})
        return final, nil
        {{- else }}
        {{- if .HasPayload }}
        if err := a.service.{{ .ServiceMethodName }}(ctx, payload); err != nil {
            return nil, a.mapError(err)
        }
        {{- else }}
        if err := a.service.{{ .ServiceMethodName }}(ctx); err != nil {
            return nil, a.mapError(err)
        }
        {{- end }}
        ok := stringPtr("{\"status\":\"success\"}")
        a.log(ctx, "response", map[string]any{"method": "tools/call", "name": p.Name})
        return &ToolsCallResult{
            Content: []*ContentItem{
                &ContentItem{ Type: "text", Text: ok },
            },
        }, nil
        {{- end }}
    {{- end }}
    default:
        return nil, goa.PermanentError("invalid_params", "Unknown tool: %s", p.Name)
    }
}

{{- end }}
