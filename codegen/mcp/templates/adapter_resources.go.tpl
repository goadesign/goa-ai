{{- if .Resources }}
{{ comment "Resources handling" }}

func (a *MCPAdapter) ResourcesList(ctx context.Context, p *ResourcesListPayload) (*ResourcesListResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "resources/list"})
    resources := []*ResourceInfo{
        {{- range .Resources }}
        { URI: {{ quote .URI }}, Name: stringPtr({{ quote .Name }}), Description: stringPtr({{ quote .Description }}), MimeType: stringPtr({{ quote .MimeType }}) },
        {{- end }}
    }
    res := &ResourcesListResult{Resources: resources}
    a.log(ctx, "response", map[string]any{"method": "resources/list"})
    return res, nil
}

func (a *MCPAdapter) ResourcesRead(ctx context.Context, p *ResourcesReadPayload) (*ResourcesReadResult, error) {
    if !a.isInitialized() {
        return nil, goa.PermanentError("invalid_params", "Not initialized")
    }
    a.log(ctx, "request", map[string]any{"method": "resources/read", "uri": p.URI})
    baseURI := p.URI
    if i := strings.Index(baseURI, "?"); i >= 0 {
        baseURI = baseURI[:i]
    }
    switch baseURI {
    {{- range .Resources }}
    case {{ quote .URI }}:
        if err := a.assertResourceURIAllowed(ctx, p.URI); err != nil {
            return nil, goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- if .HasPayload }}
        args, aerr := parseQueryParamsToJSON(p.URI)
        if aerr != nil {
            return nil, goa.PermanentError("invalid_params", "%s", aerr.Error())
        }
        req := &http.Request{ Body: io.NopCloser(bytes.NewReader(args)), Header: http.Header{"Content-Type": []string{"application/json"}}, }
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil {
            return nil, goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- end }}
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
        {{- else }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx)
        {{- end }}
        if err != nil {
            return nil, a.mapError(err)
        }
        s, serr := mcpruntime.EncodeJSONToString(ctx, goahttp.ResponseEncoder, result)
        if serr != nil {
            return nil, goa.PermanentError("invalid_params", "%s", serr.Error())
        }
        res := &ResourcesReadResult{ Contents: []*ResourceContent{ { URI: baseURI, MimeType: stringPtr({{ quote .MimeType }}), Text: &s } } }
        a.log(ctx, "response", map[string]any{"method": "resources/read", "uri": baseURI})
        return res, nil
        {{- else }}
        {{- if .HasPayload }}
        if err := a.service.{{ .OriginalMethodName }}(ctx, payload); err != nil {
            return nil, a.mapError(err)
        }
        {{- else }}
        if err := a.service.{{ .OriginalMethodName }}(ctx); err != nil {
            return nil, a.mapError(err)
        }
        {{- end }}
        res := &ResourcesReadResult{ Contents: []*ResourceContent{ { URI: baseURI, MimeType: stringPtr({{ quote .MimeType }}), Text: stringPtr("{\"status\":\"success\"}") } } }
        a.log(ctx, "response", map[string]any{"method": "resources/read", "uri": baseURI})
        return res, nil
        {{- end }}
    {{- end }}
    default:
        return nil, goa.PermanentError("method_not_found", "Unknown resource: %s", p.URI)
    }
}

// assertResourceURIAllowed verifies pURI passes allow/deny filters when configured.
func (a *MCPAdapter) assertResourceURIAllowed(ctx context.Context, pURI string) error {
    base := pURI; if i := strings.Index(base, "?"); i >= 0 { base = base[:i] }
    // Merge header-driven allow/deny lists from context (CSV of names)
    var extraAllowURIs, extraDenyURIs []string
    if ctx != nil {
        if v := ctx.Value("mcp_allow_names"); v != nil {
            if s, ok := v.(string); ok {
                for _, n := range strings.Split(s, ",") {
                    n = strings.TrimSpace(n)
                    if u, ok2 := a.resourceNameToURI[n]; ok2 { extraAllowURIs = append(extraAllowURIs, u) }
                }
            }
        }
        if v := ctx.Value("mcp_deny_names"); v != nil {
            if s, ok := v.(string); ok {
                for _, n := range strings.Split(s, ",") {
                    n = strings.TrimSpace(n)
                    if u, ok2 := a.resourceNameToURI[n]; ok2 { extraDenyURIs = append(extraDenyURIs, u) }
                }
            }
        }
    }
    for _, allow := range extraAllowURIs {
        if allow == base {
            return nil
        }
    }
    var denied []string
    if a.opts != nil { denied = a.opts.DeniedResourceURIs }
    for _, d := range append(denied, extraDenyURIs...) {
        if d == base {
            return fmt.Errorf("resource URI denied: %s", pURI)
        }
    }
    var allowed []string
    if a.opts != nil { allowed = a.opts.AllowedResourceURIs }
    if len(allowed) == 0 && len(extraAllowURIs) == 0 {
        return fmt.Errorf("resource URI not allowed: %s", pURI)
    }
    for _, allow := range append(allowed, extraAllowURIs...) {
        if allow == base {
            return nil
        }
    }
    return fmt.Errorf("resource URI not allowed: %s", pURI)
}

func (a *MCPAdapter) ResourcesSubscribe(ctx context.Context, p *ResourcesSubscribePayload) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    switch p.URI {
    {{- range .Resources }}
    {{- if .Watchable }}
    case {{ quote .URI }}:
        a.subsMu.Lock()
        a.subs[p.URI] = a.subs[p.URI] + 1
        a.subsMu.Unlock()
        return nil
    {{- end }}
    {{- end }}
    default: return goa.PermanentError("method_not_found", "Unknown resource: %s", p.URI)
    }
}

func (a *MCPAdapter) ResourcesUnsubscribe(ctx context.Context, p *ResourcesUnsubscribePayload) error {
    if !a.isInitialized() {
        return goa.PermanentError("invalid_params", "Not initialized")
    }
    switch p.URI {
    {{- range .Resources }}
    {{- if .Watchable }}
    case {{ quote .URI }}:
        a.subsMu.Lock()
        if n, ok := a.subs[p.URI]; ok {
            if n > 1 {
                a.subs[p.URI] = n - 1
            } else {
                delete(a.subs, p.URI)
            }
        }
        a.subsMu.Unlock()
        return nil
        {{- end }}
    {{- end }}
    default:
        return goa.PermanentError("method_not_found", "Unknown resource: %s", p.URI)
    }
}

{{- end }}
