// {{ .MCPConstructorName }} returns the MCP service backed by the user service.
func {{ .MCPConstructorName }}() {{ .MCPAlias }}.{{ .MCPServiceInterface }} {
    {{- if .HasPrompts }}
    return {{ .MCPAlias }}.NewMCPAdapter({{ .UserConstructorName }}(), nil, nil)
    {{- else }}
    return {{ .MCPAlias }}.NewMCPAdapter({{ .UserConstructorName }}(), nil)
    {{- end }}
}
