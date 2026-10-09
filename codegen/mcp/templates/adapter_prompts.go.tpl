{{- if or .StaticPrompts .MethodPrompts }}
{{ comment "Prompts handling" }}

// PromptsList returns either the fixed catalog or an authenticated page of
// declared prompt names. Descriptions and argument contracts remain generated.
func (a *MCPAdapter) PromptsList(ctx context.Context, p {{ index .PayloadRefs "prompts/list" }}) (*PromptsListResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.prompts/list")
    defer span.End()
    {{- with .PromptCatalog }}
    {{ template "discovery-call" . }}
    prompts := make([]*PromptInfo, 0, len({{ .Endpoint.ResultValue }}.{{ .EntriesField }}))
    seen := make(map[string]struct{}, len({{ .Endpoint.ResultValue }}.{{ .EntriesField }}))
    for _, name := range {{ .Endpoint.ResultValue }}.{{ .EntriesField }} {
        selected := string({{ if .NamePointer }}*{{ end }}name)
        if _, duplicate := seen[selected]; duplicate {
            failure := goa.PermanentError("internal_error", "prompt catalog returned duplicate name %q", selected)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return nil, failure
        }
        seen[selected] = struct{}{}
        switch selected {
        {{- range $.StaticPrompts }}
        case {{ quote .Name }}:
            prompts = append(prompts, &PromptInfo{Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }})})
        {{- end }}
        {{- range $.MethodPrompts }}
        case {{ quote .Name }}:
            prompts = append(prompts, &PromptInfo{{ template "prompt-info" . }})
        {{- end }}
        default:
            failure := goa.PermanentError("internal_error", "prompt catalog returned undeclared name %q", selected)
            span.RecordError(failure)
            span.SetStatus(codes.Error, failure.Error())
            return nil, failure
        }
    }
    {{- else }}
    if p.Cursor != nil {
        failure := goa.PermanentError("invalid_params", "prompts/list does not accept a cursor")
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    prompts := []*PromptInfo{
    {{ range .StaticPrompts }}
        {Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }})},
    {{ end }}
    {{ range .MethodPrompts }}
        {{ template "prompt-info" . }},
    {{ end }}
    }
    {{- end }}
    return &PromptsListResult{
        ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Prompts: prompts,
        {{- if .PromptCatalog }}NextCursor: nextCursor,{{ end }}
    }, nil
}

// PromptsGet returns fixed messages or calls the service with validated arguments.
func (a *MCPAdapter) PromptsGet(ctx context.Context, p {{ index .PayloadRefs "prompts/get" }}) (*PromptsGetResult, error) {
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
        res := &PromptsGetCompleteResult{
 Meta: resultMeta(),
            Description: stringPtr({{ quote .Description }}),
            Messages: msgs,
        }

        return &PromptsGetResult{Outcome: NewPromptsGetOutcomeComplete(res)}, nil
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
        {{- if or .Endpoint.Credentials .Endpoint.Paths .Endpoint.InputExchange }}
        if err := fill{{ .Endpoint.CallName }}Inputs(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "prompts/get" }}{{ end }}{{ range .Endpoint.Paths }}, p.{{ index .Sources "prompts/get" }}{{ end }}{{ if .Endpoint.InputExchange }}, p.RequestState, p.InputResponses{{ end }}); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, err
        }
        {{- end }}
        result, err := a.{{ .Endpoint.CallName }}(ctx{{ if .HasPayload }}, payload{{ end }})
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, a.mapError(err, {{ if .Endpoint.FaultNames }}is{{ .Endpoint.CallName }}Fault{{ else }}isEndpointFault{{ end }}(err)).err
        }
        {{- $endpoint := .Endpoint }}
        {{- with .Endpoint.InputExchange }}
        if err := {{ .OutcomeValue }}.Validate(); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        if pending, ok := {{ .OutcomeValue }}.AsInputRequired(); ok {
            input, err := convert{{ $endpoint.CallName }}Pending(pending, p.Meta)
            if err != nil {
                span.RecordError(err)
                span.SetStatus(codes.Error, err.Error())
                return nil, err
            }
            return &PromptsGetResult{Outcome: NewPromptsGetOutcomeInputRequired(input)}, nil
        }
        completedResult, _ := {{ .OutcomeValue }}.AsComplete()
        {{- end }}
        if err := {{ .Codec.ResultValidate }}({{ if and .Endpoint.InputExchange (not .Endpoint.ExecutionView) }}completedResult{{ else }}result{{ end }}); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        messages := make([]*PromptMessage, 0, len({{ .Endpoint.ResultValue }}.{{ .MessagesField }}))
        for _, message := range {{ .Endpoint.ResultValue }}.{{ .MessagesField }} {
            content, err := {{ .ContentConversion }}(message.{{ .ContentField }})
            if err != nil {
                span.RecordError(err)
                span.SetStatus(codes.Error, err.Error())
                return nil, goa.PermanentError("internal_error", "%s", err.Error())
            }
            messages = append(messages, &PromptMessage{Role: string({{ if .RolePointer }}*{{ end }}message.{{ .RoleField }}), Content: content})
        }
        response := &PromptsGetCompleteResult{Meta: resultMeta(), Messages: messages}
        {{ if .DescriptionField }}
        {{ if .DescriptionPointer }}
        if {{ .Endpoint.ResultValue }}.{{ .DescriptionField }} != nil {
            response.Description = stringPtr(string(*{{ .Endpoint.ResultValue }}.{{ .DescriptionField }}))
        }
        {{ else }}
        response.Description = stringPtr(string({{ .Endpoint.ResultValue }}.{{ .DescriptionField }}))
        {{ end }}
        {{ end }}
        return &PromptsGetResult{Outcome: NewPromptsGetOutcomeComplete(response)}, nil
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
        {{ with .Metadata }}
        {{ if .Optional }}if selected.{{ .Field }} != nil { {{ end }}
            metadata, err := {{ .Encode }}({{ if .Dereference }}*{{ end }}selected.{{ .Field }})
            if err != nil { return nil, err }
            out.{{ .TargetField }} = metadata
        {{ if .Optional }} } {{ end }}
        {{ end }}
        {{ if .CheckSize }}
        if out.Size != nil && (math.IsNaN(*out.Size) || math.IsInf(*out.Size, 0)) {
            return nil, goa.PermanentError("invalid_content", "resource size must be a finite JSON number")
        }
        {{ end }}
        {{ if .CheckPriority }}
        if out.Annotations != nil && out.Annotations.Priority != nil && (math.IsNaN(*out.Annotations.Priority) || math.IsInf(*out.Annotations.Priority, 0)) {
            return nil, goa.PermanentError("invalid_content", "content priority must be a finite JSON number")
        }
        {{ end }}
        {{ if $conversion.HasType }}out.Type = {{ quote .Name }}{{ end }}
        {{ if .BytesField }}
        encoded := base64.StdEncoding.EncodeToString({{ if .BytesPointer }}*{{ end }}selected.{{ .BytesField }})
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

{{- define "prompt-info" -}}
{Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), Arguments: []*PromptArgument{
    {{- range .Arguments }}
    {Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), Required: boolPtr({{ .Required }})},
    {{- end }}
}}
{{- end }}
