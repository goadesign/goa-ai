{{- if .Resources }}
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
        result, err := a.service.{{ .ServiceMethodName }}(ctx)
        if err != nil {
            span.RecordError(err)
            span.SetStatus(codes.Error, err.Error())
            return nil, a.mapError(err)
        }
        {{- if .BinaryResult }}
        blob := base64.StdEncoding.EncodeToString(result)
        {{- else if .TextResult }}
        text := string(result)
        {{- else }}
        encoded, err := {{ $.CodecPackage }}.{{ .Codec.ResultEncode }}(result)
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
        failure := goa.PermanentError("invalid_params", "unknown resource: %s", p.URI)
        span.RecordError(failure)
        span.SetStatus(codes.Error, failure.Error())
        return nil, failure
    }
}

{{- end }}
