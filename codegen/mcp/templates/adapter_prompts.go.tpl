{{- if or .StaticPrompts .MethodPrompts }}
{{ comment "Prompts handling" }}

// PromptsList describes the fixed prompts and service-owned prompt operations.
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
    {{ range .MethodPrompts }}
        {Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), Arguments: []*PromptArgument{
            {{ range .Arguments }}
            {Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), Required: boolPtr({{ .Required }})},
            {{ end }}
        }},
    {{ end }}
    }
    res := &PromptsListResult{ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Prompts: prompts}

    return res, nil
}

// PromptsGet returns fixed messages or calls the service with validated arguments.
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
            Content: &ContentItem{
                Type: "text",
                Text: stringPtr({{ quote .Content }}),
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
    {{ range .MethodPrompts }}
    case {{ quote .Name }}:
        {{ if .HasPayload }}
        for name := range p.Arguments {
            switch name {
            {{ range .Arguments }}
            case {{ quote .Name }}:
            {{ end }}
            default:
                failure := goa.PermanentError("invalid_params", "unknown argument %q for prompt %q", name, p.Name)
                span.RecordError(failure)
                span.SetStatus(codes.Error, failure.Error())
                return nil, failure
            }
        }
        body := &{{ .PayloadTransportRef }}{}
        {{ range .Arguments }}
        if value, present := p.Arguments[{{ quote .Name }}]; present {
            typed := {{ .TypeRef }}(value)
            body.{{ .Selector }} = &typed
        }
        {{ end }}
        payload, err := {{ $.CodecPackage }}.{{ .PayloadConstructor }}(body)
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{ else }}
        if len(p.Arguments) > 0 {
            failure := goa.PermanentError("invalid_params", "prompt %q does not accept arguments", p.Name)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return nil, failure
        }
        {{ end }}
        result, err := a.service.{{ .ServiceMethodName }}(ctx{{ if .HasPayload }}, payload{{ end }})
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, a.mapError(err)
        }
        if err := {{ $.CodecPackage }}.{{ .Codec.ResultValidate }}(result); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        messages := make([]*PromptMessage, 0, len(result.{{ .MessagesField }}))
        for _, message := range result.{{ .MessagesField }} {
            content, err := {{ .ContentConversion }}(message.{{ .ContentField }})
            if err != nil {
                span.RecordError(err)
                span.SetStatus(codes.Error, err.Error())
                return nil, goa.PermanentError("internal_error", "%s", err.Error())
            }
            messages = append(messages, &PromptMessage{Role: string(message.{{ .RoleField }}), Content: content})
        }
        response := &PromptsGetResult{ResultType: "complete", Meta: resultMeta(), Messages: messages}
        {{ if .DescriptionField }}
        {{ if .DescriptionPointer }}
        if result.{{ .DescriptionField }} != nil {
            response.Description = stringPtr(string(*result.{{ .DescriptionField }}))
        }
        {{ else }}
        response.Description = stringPtr(string(result.{{ .DescriptionField }}))
        {{ end }}
        {{ end }}
        return response, nil
    {{ end }}
    }
    failure := goa.PermanentError("invalid_params", "unknown prompt: %s", p.Name)
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
}
{{- end }}

{{ if .NeedsContentMeta }}
// validateContentMeta accepts absent metadata or a JSON object. An authored
// scalar, array, null, or invalid JSON result fails before the response is sent.
func validateContentMeta(raw json.RawMessage) error {
    if len(raw) == 0 { return nil }
    var fields map[string]json.RawMessage
    if err := json.Unmarshal(raw, &fields); err != nil {
        return fmt.Errorf("content metadata must be a JSON object: %w", err)
    }
    if fields == nil { return fmt.Errorf("content metadata must be a JSON object") }
    return nil
}
{{ end }}

{{ range .ContentConversions }}
// {{ .Name }} checks the selected service content branch and converts its fields
// to MCP. An unset branch or missing selected value returns a validation error.
func {{ .Name }}(value {{ .SourceRef }}) ({{ .TargetRef }}, error) {
    {{ if .UnionRef }}selectedContent := {{ .UnionRef }}(value){{ end }}
    if err := {{ .Value }}.Validate(); err != nil {
        return nil, err
    }
    switch {{ .Value }}.Kind() {
    {{ $conversion := . }}
    {{ range .Branches }}
    case {{ .Kind }}:
        selected, _ := {{ $conversion.Value }}.{{ .Getter }}()
        {{ if .MetaField }}
        if err := validateContentMeta(selected.{{ .MetaField }}); err != nil { return nil, err }
        {{ end }}
        {{ .Transform }}
        {{ if .CheckSize }}
        if out.Size != nil && (math.IsNaN(*out.Size) || math.IsInf(*out.Size, 0)) {
            return nil, goa.PermanentError("invalid_content", "prompt resource size must be a finite JSON number")
        }
        {{ end }}
        {{ if .CheckPriority }}
        if out.Annotations != nil && out.Annotations.Priority != nil && (math.IsNaN(*out.Annotations.Priority) || math.IsInf(*out.Annotations.Priority, 0)) {
            return nil, goa.PermanentError("invalid_content", "prompt content priority must be a finite JSON number")
        }
        {{ end }}
        {{ if $conversion.HasType }}out.Type = {{ quote .Name }}{{ end }}
        {{ if .BytesField }}
        encoded := base64.StdEncoding.EncodeToString(selected.{{ .BytesField }})
        out.{{ .TargetBytesField }} = &encoded
        {{ end }}
        {{ if .NestedConversion }}
        resource, err := {{ .NestedConversion }}(selected.{{ .ResourceField }})
        if err != nil { return nil, err }
        out.Resource = resource
        {{ end }}
        return out, nil
    {{ end }}
    default:
        panic("content reached conversion without validation")
    }
}
{{ range .Helpers }}
func {{ .Declaration.Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
    {{ .Code }}
    return res
}
{{ end }}
{{ end }}
