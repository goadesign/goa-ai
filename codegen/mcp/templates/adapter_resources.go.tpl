{{- if or .Resources .ResourceTemplates }}
{{ comment "Resources handling" }}

// ResourcesList returns the fixed resources declared in the Goa design.
func (a *MCPAdapter) ResourcesList(ctx context.Context, p *ResourcesListPayload) (*ResourcesListResult, error) {
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
func (a *MCPAdapter) ResourcesRead(ctx context.Context, p *ResourcesReadPayload) (*ResourcesReadResult, error) {
    ctx, span := otel.Tracer("goa-ai/mcp").Start(ctx, "mcp.resources/read")
    defer span.End()

    switch p.URI {
    {{- range .Resources }}
    case {{ quote .URI }}:
        {{- if .Endpoint.PayloadRef }}
        payload := new({{ .Endpoint.PayloadValueRef }})
        {{- if .Endpoint.Credentials }}
        if err := fill{{ .Endpoint.CallName }}Credentials(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "resources/read" }}{{ end }}); err != nil {
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
        {{- if .BinaryResult }}
        blob := base64.StdEncoding.EncodeToString(result)
        {{- else if .TextResult }}
        text := string(result)
        {{- else }}
        encoded, err := {{ .Codec.ResultEncode }}(result)
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, goa.PermanentError("internal_error", "%s", err.Error())
        }
        text := string(encoded)
        {{- end }}
        res := &ResourcesReadResult{
 ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private",
            Contents: []*ResourceContent{
                {URI: p.URI, MimeType: stringPtr({{ quote .MimeType }}), {{ if .BinaryResult }}Blob: &blob{{ else }}Text: &text{{ end }}},
            },
        }

        return res, nil
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
        {{- if .Endpoint.Credentials }}
        if err := fill{{ .Endpoint.CallName }}Credentials(payload{{ range .Endpoint.Credentials }}, p.{{ index .Sources "resources/read" }}{{ end }}); err != nil {
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
        if err := {{ .Codec.ResultValidate }}(result); err != nil {
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
        return &ResourcesReadResult{ResultType: "complete", Meta: resultMeta(), TTLMs: 0, CacheScope: "private", Contents: contents}, nil
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
func (a *MCPAdapter) ResourcesTemplatesList(ctx context.Context, p *ResourceTemplatesListPayload) (*ResourceTemplatesListResult, error) {
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
