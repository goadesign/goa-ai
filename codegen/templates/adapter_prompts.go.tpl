{{- if or .StaticPrompts .DynamicPrompts }}
{{ comment "Prompts handling" }}

func (a *MCPAdapter) PromptsList(ctx context.Context, p *PromptsListPayload) (*PromptsListResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "prompts/list"})
    prompts := []*PromptInfo{
    {{ range .DynamicPrompts }}
        { Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), Arguments: []*PromptArgument{
            {{ range .Arguments }}
            { Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), Required: {{ .Required }} },
            {{ end }}
        } },
    {{ end }}
    {{ range .StaticPrompts }}
        { Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}) },
    {{ end }}
    }
    res := &PromptsListResult{Prompts: prompts}
    a.log(ctx, "response", map[string]any{"method": "prompts/list"})
    return res, nil
}

func (a *MCPAdapter) PromptsGet(ctx context.Context, p *PromptsGetPayload) (*PromptsGetResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    if p == nil || p.Name == "" {
        return nil, goa.PermanentError("invalid_params", "Missing prompt name")
    }
    a.log(ctx, "request", map[string]any{"method": "prompts/get", "name": p.Name})
    switch p.Name {
    {{ range .StaticPrompts }}
    case "{{ .Name }}":
        if a.promptProvider != nil {
            if res, err := a.promptProvider.Get{{ goify .Name }}Prompt(p.Arguments); err == nil && res != nil {
                a.log(ctx, "response", map[string]any{"method": "prompts/get", "name": p.Name})
                return res, nil
            } else if err != nil {
                return nil, err
            }
        }
        msgs := make([]*PromptMessage, 0, {{ len .Messages }})
        {{ range .Messages }}
        msgs = append(msgs, &PromptMessage{
            Role: {{ quote .Role }},
            Content: &MessageContent{
                Type: "text",
                Text: stringPtr({{ quote .Content }}),
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
    {{ if .DynamicPrompts }}
    switch p.Name {
        {{ range .DynamicPrompts }}
    case "{{ .Name }}":
        {
            {{ $hasRequired := false }}
            {{ range .Arguments }}
                {{ if .Required }}
                    {{ $hasRequired = true }}
                {{ end }}
            {{ end }}
            {{ if $hasRequired }}
            var args map[string]any
            if len(p.Arguments) > 0 {
                if err := json.Unmarshal(p.Arguments, &args); err != nil {
                    return nil, goa.PermanentError("invalid_params", "%s", err.Error())
                }
            }
            {{ range .Arguments }}
                {{ if .Required }}
            if _, ok := args["{{ .Name }}"]; !ok {
                return nil, goa.PermanentError("invalid_params", "Missing required argument: {{ .Name }}")
            }
                {{ end }}
            {{ end }}
            {{ end }}
        }
        if a.promptProvider == nil { return nil, goa.PermanentError("invalid_params", "No prompt provider configured for dynamic prompts") }
        res, err := a.promptProvider.Get{{ goify .Name }}Prompt(ctx, p.Arguments)
        if err != nil {
            return nil, a.mapError(err)
        }
        a.log(ctx, "response", map[string]any{"method": "prompts/get", "name": p.Name})
        return res, nil
        {{ end }}
    }
    {{ end }}
    return nil, goa.PermanentError("method_not_found", "Unknown prompt: %s", p.Name)
}
{{- end }}


