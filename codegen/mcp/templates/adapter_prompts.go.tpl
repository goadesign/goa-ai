{{- if .StaticPrompts }}
{{ comment "Prompts handling" }}

// PromptsList returns the fixed prompts declared in the Goa design.
func (a *MCPAdapter) PromptsList(ctx context.Context, p *PromptsListPayload) (*PromptsListResult, error) {
    a.log(ctx, "request", map[string]any{"method": "prompts/list"})
    if p.Params != nil && p.Params.Cursor != nil {
        return nil, goa.PermanentError("invalid_params", "prompts/list does not accept a cursor")
    }
    prompts := []*PromptInfo{
    {{ range .StaticPrompts }}
        { Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}) },
    {{ end }}
    }
    res := &PromptsListResult{Prompts: prompts}
    a.log(ctx, "response", map[string]any{"method": "prompts/list"})
    return res, nil
}

// PromptsGet returns the fixed messages for the named prompt.
func (a *MCPAdapter) PromptsGet(ctx context.Context, p *PromptsGetPayload) (*PromptsGetResult, error) {
    a.log(ctx, "request", map[string]any{"method": "prompts/get", "name": p.Name})
    switch p.Name {
    {{ range .StaticPrompts }}
    case {{ quote .Name }}:
        if len(p.Arguments) > 0 {
            return nil, goa.PermanentError("invalid_params", "prompt %q does not accept arguments", p.Name)
        }
        msgs := make([]*PromptMessage, 0, {{ len .Messages }})
        {{ range .Messages }}
        msgs = append(msgs, &PromptMessage{
            Role: {{ quote .Role }},
            Content: &MessageContent{
                Type: "text",
                Text: {{ quote .Content }},
            },
        })
        {{ end }}
        res := &PromptsGetResult{
            Description: stringPtr({{ quote .Description }}),
            Messages: msgs,
        }
        a.log(ctx, "response", map[string]any{"method": "prompts/get", "name": p.Name})
        return res, nil
    {{ end }}
    }
    return nil, goa.PermanentError("invalid_params", "Unknown prompt: %s", p.Name)
}
{{- end }}
