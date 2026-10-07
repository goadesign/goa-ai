{{- if or .Resources .ResourceTemplates }}
{{ comment "Resources handling" }}

// ResourcesList returns the fixed resources declared in the Goa design.
func (a *MCPAdapter) ResourcesList(ctx context.Context, p {{ index .PayloadRefs "resources/list" }}) (*ResourcesListResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.resources/list")
    defer span.End()

    if p.Cursor != nil {
        failure := goa.PermanentError("invalid_params", "resources/list does not accept a cursor")
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    resources := []*ResourceInfo{
        {{- range .Resources }}
        { URI: {{ quote .URI }}, Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), MimeType: stringPtr({{ quote .MimeType }}) },
        {{- end }}
    }
    res := &ResourcesListResult{ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Resources: resources}

    return res, nil
}

// ResourcesRead calls the Goa method that owns the requested resource.
func (a *MCPAdapter) ResourcesRead(ctx context.Context, p {{ index .PayloadRefs "resources/read" }}) (*ResourcesReadResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.resources/read")
    defer span.End()

    switch p.URI {
    {{- range .Resources }}
    case {{ quote .URI }}:
        {{- if .Endpoint.PayloadRef }}
        payload := new({{ .Endpoint.PayloadValueRef }})
        {{- if or .Endpoint.Credentials .Endpoint.Paths .Endpoint.InputExchange }}
        if err := fill{{ .Endpoint.CallName }}Inputs(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "resources/read" }}{{ end }}{{ range .Endpoint.Paths }}, p.{{ index .Sources "resources/read" }}{{ end }}{{ if .Endpoint.InputExchange }}, p.RequestState, p.InputResponses{{ end }}); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, err
        }
        {{- end }}
        {{- end }}
        result, err := a.{{ .Endpoint.CallName }}(ctx{{ if .Endpoint.PayloadRef }}, payload{{ end }})
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
            return &ResourcesReadResult{Outcome: NewResourcesReadOutcomeInputRequired(input)}, nil
        }
        completedResult, _ := {{ .OutcomeValue }}.AsComplete()
        {{- end }}
        {{- if .BinaryResult }}
        blob := base64.StdEncoding.EncodeToString({{ .Endpoint.ResultValue }})
        {{- else if .TextResult }}
        text := string({{ .Endpoint.ResultValue }})
        {{- else }}
        encoded, err := {{ .Codec.ResultEncode }}({{ if and .Endpoint.InputExchange (not .Endpoint.ExecutionView) }}completedResult{{ else }}result{{ end }})
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        text := string(encoded)
        {{- end }}
        res := &ResourcesReadCompleteResult{
 Meta: resultMeta(), TTLMs: 0, CacheScope: "private",
            Contents: []*ResourceContent{
                {URI: p.URI, MimeType: stringPtr({{ quote .MimeType }}), {{ if .BinaryResult }}Blob: &blob{{ else }}Text: &text{{ end }}},
            },
        }

        return &ResourcesReadResult{Outcome: NewResourcesReadOutcomeComplete(res)}, nil
    {{- end }}
    default:
        {{- if .ResourceReader }}
        {{- with .ResourceReader }}
        body := &{{ .PayloadTransportRef }}{}
        uri := {{ .URI.ValueTypeRef }}(p.URI)
        body.{{ .URI.Selector }} = &uri
        payload, err := {{ $.CodecPackage }}.{{ .PayloadConstructor }}(body)
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- if or .Endpoint.Credentials .Endpoint.Paths .Endpoint.InputExchange }}
        if err := fill{{ .Endpoint.CallName }}Inputs(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "resources/read" }}{{ end }}{{ range .Endpoint.Paths }}, p.{{ index .Sources "resources/read" }}{{ end }}{{ if .Endpoint.InputExchange }}, p.RequestState, p.InputResponses{{ end }}); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, err
        }
        {{- end }}
        result, err := a.{{ .Endpoint.CallName }}(ctx, payload)
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
            return &ResourcesReadResult{Outcome: NewResourcesReadOutcomeInputRequired(input)}, nil
        }
        completedResult, _ := {{ .OutcomeValue }}.AsComplete()
        {{- end }}
        if err := {{ .Codec.ResultValidate }}({{ if and .Endpoint.InputExchange (not .Endpoint.ExecutionView) }}completedResult{{ else }}result{{ end }}); err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        contents := make([]*ResourceContent, 0, len({{ .Endpoint.ResultValue }}.{{ .ContentsField }}))
        for _, item := range {{ .Endpoint.ResultValue }}.{{ .ContentsField }} {
            content, err := {{ .ContentConversion }}(item.{{ .ContentField }})
            if err != nil {
                span.RecordError(err)
                span.SetStatus(codes.Error, err.Error())
                return nil, goa.PermanentError("internal_error", "%s", err.Error())
            }
            contents = append(contents, content)
        }
        return &ResourcesReadResult{Outcome: NewResourcesReadOutcomeComplete(&ResourcesReadCompleteResult{Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Contents: contents})}, nil
        {{- end }}
        {{- else }}
        failure := goa.PermanentError("invalid_params", "unknown resource: %s", p.URI)
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
        {{- end }}
    }
}

{{- end }}

{{- if or .Resources .ResourceTemplates }}
// ResourcesTemplatesList returns the URI templates advertised by the service.
// Templates guide discovery; the typed reader owns URI interpretation and access.
func (a *MCPAdapter) ResourcesTemplatesList(ctx context.Context, p {{ index .PayloadRefs "resources/templates/list" }}) (*ResourceTemplatesListResult, error) {
    _, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.resources/templates/list")
    defer span.End()
    if p.Cursor != nil {
        failure := goa.PermanentError("invalid_params", "resources/templates/list does not accept a cursor")
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
    templates := []*ResourceTemplateInfo{
        {{- range .ResourceTemplates }}
        {URITemplate: {{ quote .URI }}, Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), MimeType: stringPtr({{ quote .MimeType }})},
        {{- end }}
    }
    return &ResourceTemplatesListResult{ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", ResourceTemplates: templates}, nil
}
{{- end }}
