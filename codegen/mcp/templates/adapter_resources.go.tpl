{{- if .Resources }}
{{ comment "Resources handling" }}

// ResourcesList returns the fixed resources declared in the Goa design.
func (a *MCPAdapter) ResourcesList(ctx context.Context, p *ResourcesListPayload) (*ResourcesListResult, error) {
    a.log(ctx, "request", map[string]any{"method": "resources/list"})
    if p.Cursor != nil {
        return nil, goa.PermanentError("invalid_params", "resources/list does not accept a cursor")
    }
    resources := []*ResourceInfo{
        {{- range .Resources }}
        { URI: {{ quote .URI }}, Name: {{ quote .Name }}, Description: stringPtr({{ quote .Description }}), MimeType: stringPtr({{ quote .MimeType }}) },
        {{- end }}
    }
    res := &ResourcesListResult{Resources: resources}
    a.log(ctx, "response", map[string]any{"method": "resources/list"})
    return res, nil
}

// ResourcesRead calls the Goa method that owns the requested resource.
func (a *MCPAdapter) ResourcesRead(ctx context.Context, p *ResourcesReadPayload) (*ResourcesReadResult, error) {
    a.log(ctx, "request", map[string]any{"method": "resources/read", "uri": p.URI})
    switch p.URI {
    {{- range .Resources }}
    case {{ quote .URI }}:
        result, err := a.service.{{ .ServiceMethodName }}(ctx)
        if err != nil {
            return nil, a.mapError(err)
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
        res := &ResourcesReadResult{
            Contents: []*ResourceContent{
                {URI: p.URI, MimeType: stringPtr({{ quote .MimeType }}), Text: text},
            },
        }
        a.log(ctx, "response", map[string]any{"method": "resources/read", "uri": p.URI})
        return res, nil
    {{- end }}
    default:
        return nil, goa.PermanentError("invalid_params", "Unknown resource: %s", p.URI)
    }
}

{{- end }}
