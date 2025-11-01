// Example MCP stub: ensure NewMcp{{ .ServiceGo }} returns the adapter-wrapped service.
func NewMcp{{ .ServiceGo }}() {{ .MCPAlias }}.Service {
    {{- if .HasPrompts }}
    return {{ .MCPAlias }}.NewMCPAdapter(New{{ .ServiceGo }}(), nil, nil)
    {{- else }}
    return {{ .MCPAlias }}.NewMCPAdapter(New{{ .ServiceGo }}(), nil)
    {{- end }}
}

