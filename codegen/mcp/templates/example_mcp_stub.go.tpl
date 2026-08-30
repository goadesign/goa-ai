// {{ .MCPConstructorName }} returns the MCP service backed by the user service.
func {{ .MCPConstructorName }}() {{ .MCPAlias }}.{{ .MCPServiceInterface }} {
    return {{ .MCPAlias }}.NewMCPAdapter({{ .UserConstructorName }}(), nil)
}
