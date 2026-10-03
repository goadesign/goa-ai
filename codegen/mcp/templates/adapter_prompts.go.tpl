{{- if .StaticPrompts }}
{{ comment "Prompts handling" }}

// PromptsList returns the fixed prompts declared in the Goa design.
func (a *MCPAdapter) PromptsList(ctx context.Context, p *PromptsListPayload) (*PromptsListResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.prompts/list")
    defer span.End()

    if p.Cursor != nil {
        failure := goa.PermanentError("invalid_params", "prompts/list does not accept a cursor")
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    prompts := []*PromptInfo{
    {{ range .StaticPrompts }}
        { Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}) },
    {{ end }}
    }
    res := &PromptsListResult{ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Prompts: prompts}

    return res, nil
}

// PromptsGet returns the fixed messages for the named prompt.
func (a *MCPAdapter) PromptsGet(ctx context.Context, p *PromptsGetPayload) (*PromptsGetResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.prompts/get")
    defer span.End()

    switch p.Name {
    {{ range .StaticPrompts }}
    case {{ quote .Name }}:
        if len(p.Arguments) > 0 {
            failure := goa.PermanentError("invalid_params", "prompt %q does not accept arguments", p.Name)
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
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
 ResultType: "complete", Meta: resultMeta(),
            Description: stringPtr({{ quote .Description }}),
            Messages: msgs,
        }

        return res, nil
    {{ end }}
    }
    failure := goa.PermanentError("invalid_params", "unknown prompt: %s", p.Name)
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
}
{{- end }}
