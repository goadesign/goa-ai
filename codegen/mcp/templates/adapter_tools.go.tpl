{{- if .Tools }}
{{ comment "Tools handling" }}

// ToolsList returns the tools declared in the Goa design.
func (a *MCPAdapter) ToolsList(ctx context.Context, p *ToolsListPayload) (*ToolsListResult, error) {
    a.log(ctx, "request", map[string]any{"method": "tools/list"})
    if p.Cursor != nil {
        return nil, goa.PermanentError("invalid_params", "tools/list does not accept a cursor")
    }
    tools := []*ToolInfo{
        {{- range .Tools }}
        {
            Name: {{ quote .Name }},
            Description: stringPtr({{ quote .Description }}),
            InputSchema: json.RawMessage({{ quote .InputSchema }}),
            {{- if .OutputSchema }}
            OutputSchema: json.RawMessage({{ quote .OutputSchema }}),
            {{- end }}
        },
        {{- end }}
    }
    res := &ToolsListResult{Tools: tools}
    a.log(ctx, "response", map[string]any{"method": "tools/list"})
    return res, nil
}

// toolCallError returns a service failure as an MCP tool result.
func toolCallError(message string) *ToolsCallResult {
    return &ToolsCallResult{
        Content: []*ContentItem{
            {Type: "text", Text: message},
        },
        IsError: boolPtr(true),
    }
}

// ToolsCall validates the arguments and calls the Goa method for the named tool.
func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload) (*ToolsCallResult, error) {
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
            return nil, goa.PermanentError("invalid_params", "invalid arguments for tool %s: %s", p.Name, err.Error())
        }
        {{- else }}
        if err := validateNoArguments(p.Arguments); err != nil {
            return nil, goa.PermanentError("invalid_params", "invalid arguments for tool %s: %s", p.Name, err.Error())
        }
        {{- end }}
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.service.{{ .ServiceMethodName }}(ctx, payload)
        {{- else }}
        result, err := a.service.{{ .ServiceMethodName }}(ctx)
        {{- end }}
        if err != nil {
            return toolCallError(a.mapError(err).Error()), nil
        }
        {{- if .TextResult }}
        text := string(result)
        {{- else }}
        encoded, err := {{ $.CodecPackage }}.{{ .Codec.ResultEncode }}(result)
        if err != nil {
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        text := string(encoded)
        {{- end }}
        final := &ToolsCallResult{
            Content: []*ContentItem{
                {Type: "text", Text: text},
            },
            {{- if .HasStructuredResult }}
            StructuredContent: json.RawMessage(encoded),
            {{- end }}
        }
        a.log(ctx, "response", map[string]any{"method": "tools/call", "name": p.Name})
        return final, nil
        {{- else }}
        {{- if .HasPayload }}
        if err := a.service.{{ .ServiceMethodName }}(ctx, payload); err != nil {
            return toolCallError(a.mapError(err).Error()), nil
        }
        {{- else }}
        if err := a.service.{{ .ServiceMethodName }}(ctx); err != nil {
            return toolCallError(a.mapError(err).Error()), nil
        }
        {{- end }}
        a.log(ctx, "response", map[string]any{"method": "tools/call", "name": p.Name})
        return &ToolsCallResult{
            Content: []*ContentItem{},
        }, nil
        {{- end }}
    {{- end }}
    default:
        return nil, goa.PermanentError("invalid_params", "unknown tool: %s", p.Name)
    }
}

{{- end }}
