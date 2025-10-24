{{- if .Tools }}
{{ comment "Tools handling" }}

func (a *MCPAdapter) ToolsList(ctx context.Context, p *ToolsListPayload) (*ToolsListResult, error) {
    if !a.isInitialized() { return nil, goa.PermanentError("invalid_params", "Not initialized") }
    a.log(ctx, "request", map[string]any{"method": "tools/list"})
    tools := []*ToolInfo{
        {{- range .Tools }}
        {
            Name: {{ quote .Name }},
            Description: stringPtr({{ quote .Description }}),
            {{- if .InputSchema }}
            InputSchema: json.RawMessage(`{{ .InputSchema }}`),
            {{- else }}
            InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
            {{- end }}
        },
        {{- end }}
    }
    res := &ToolsListResult{Tools: tools}
    a.log(ctx, "response", map[string]any{"method": "tools/list"})
    return res, nil
}

{{ if .ToolsCallStreaming }}
{{- range .Tools }}
{{- if .IsStreaming }}
type {{ goify .OriginalMethodName }}StreamBridge struct {
    out ToolsCallServerStream
    adapter *MCPAdapter
}
func (b *{{ goify .OriginalMethodName }}StreamBridge) Send(ctx context.Context, ev {{ $.Package }}.{{ .StreamEventType }}) error {
    s, e := mcpruntime.EncodeJSONToString(ctx, goahttp.ResponseEncoder, ev)
    if e != nil {
        return e
    }
    return b.out.Send(ctx, &ToolsCallResult{
        Content: []*ContentItem{
            buildContentItem(b.adapter, s),
        },
    })
}
func (b *{{ goify .OriginalMethodName }}StreamBridge) SendAndClose(ctx context.Context, ev {{ $.Package }}.{{ .StreamEventType }}) error {
    s, e := mcpruntime.EncodeJSONToString(ctx, goahttp.ResponseEncoder, ev)
    if e != nil {
        return e
    }
    return b.out.SendAndClose(ctx, &ToolsCallResult{
        Content: []*ContentItem{
            buildContentItem(b.adapter, s),
        },
    })
}
func (b *{{ goify .OriginalMethodName }}StreamBridge) SendError(ctx context.Context, id string, err error) error {
    return b.out.SendError(ctx, id, err)
}
{{- end }}
{{- end }}

func (a *MCPAdapter) ToolsCall(ctx context.Context, p *ToolsCallPayload, stream ToolsCallServerStream) error {
    if !a.isInitialized() { return goa.PermanentError("invalid_params", "Not initialized") }
    a.log(ctx, "request", map[string]any{"method": "tools/call", "name": p.Name})
    switch p.Name {
    {{- range .Tools }}
    case {{ quote .Name }}:
        {{- if .HasPayload }}
        req := &http.Request{ Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(bytes.NewReader(p.Arguments)) }
        {{- if .IsStreaming }}
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil {
            return goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- if .RequiredFields }}
        {
            {{- range .RequiredFields }}
            if payload.{{ goify . }} == "" {
                return goa.PermanentError("invalid_params", "Missing required field: {{ . }}")
            }
            {{- end }}
        }
        {{- end }}
        {{- if .EnumFields }}
        {
            {{- $tool := . }}
            {{- range $fname, $vals := .EnumFields }}
            {
                var __val string
                {{- if (index $tool.EnumFieldsPtr $fname) }}
                if payload.{{ goify $fname }} != nil { __val = *payload.{{ goify $fname }} }
                {{- else }}
                __val = payload.{{ goify $fname }}
                {{- end }}
                ok := false
                switch __val {
                {{- range $vals }}
                case {{ printf "%q" . }}:
                    ok = true
                {{- end }}
                }
                if !ok && __val != "" {
                    return goa.PermanentError("invalid_params", "Invalid value for {{ $fname }}")
                }
            }
            {{- end }}
        }
        {{- end }}
        bridge := &{{ goify .OriginalMethodName }}StreamBridge{ out: stream, adapter: a }
        if err := a.service.{{ .OriginalMethodName }}(ctx, payload, bridge); err != nil { return a.mapError(err) }
        return nil
        {{- else }}
        var payload {{ .PayloadType }}
        if err := goahttp.RequestDecoder(req).Decode(&payload); err != nil {
            return goa.PermanentError("invalid_params", "%s", err.Error())
        }
        {{- if .RequiredFields }}
        {
            {{- range .RequiredFields }}
            if payload.{{ goify . }} == "" {
                return goa.PermanentError("invalid_params", "Missing required field: {{ . }}")
            }
            {{- end }}
        }
        {{- end }}
        {{- if .EnumFields }}
        {
            {{- $tool := . }}
            {{- range $fname, $vals := .EnumFields }}
            {
                var __val string
                {{- if (index $tool.EnumFieldsPtr $fname) }}
                if payload.{{ goify $fname }} != nil { __val = *payload.{{ goify $fname }} }
                {{- else }}
                __val = payload.{{ goify $fname }}
                {{- end }}
                ok := false
                switch __val {
                {{- range $vals }}
                case {{ printf "%q" . }}:
                    ok = true
                {{- end }}
                }
                if !ok && __val != "" {
                    return goa.PermanentError("invalid_params", "Invalid value for {{ $fname }}")
                }
            }
            {{- end }}
        }
        {{- end }}
        {{- end }}
        {{- end }}
        {{- if not .IsStreaming }}
        {{- if .HasResult }}
        {{- if .HasPayload }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx, payload)
        {{- else }}
        result, err := a.service.{{ .OriginalMethodName }}(ctx)
        {{- end }}
        if err != nil { return a.mapError(err) }
        s, serr := mcpruntime.EncodeJSONToString(ctx, goahttp.ResponseEncoder, result)
        if serr != nil { return serr }
        final := &ToolsCallResult{
            Content: []*ContentItem{
                buildContentItem(a, s),
            },
        }
        a.log(ctx, "response", map[string]any{"method": "tools/call", "name": p.Name})
        return stream.SendAndClose(ctx, final)
        {{- else }}
        {{- if .HasPayload }}
        if err := a.service.{{ .OriginalMethodName }}(ctx, payload); err != nil { return a.mapError(err) }
        {{- else }}
        if err := a.service.{{ .OriginalMethodName }}(ctx); err != nil { return a.mapError(err) }
        {{- end }}
        ok := stringPtr("{\"status\":\"success\"}")
        a.log(ctx, "response", map[string]any{"method": "tools/call", "name": p.Name})
        return stream.SendAndClose(ctx, &ToolsCallResult{
            Content: []*ContentItem{
                &ContentItem{ Type: "text", Text: ok },
            },
        })
        {{- end }}
        {{- end }}
    {{- end }}
    default:
        return goa.PermanentError("method_not_found", "Unknown tool: %s", p.Name)
    }
}
{{- end }}


{{- end }}
