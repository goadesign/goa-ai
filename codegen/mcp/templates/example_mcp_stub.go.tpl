// {{ .MCPConstructorName }} returns the MCP service backed by configured Goa endpoints.
func {{ .MCPConstructorName }}() {{ .MCPAlias }}.{{ .MCPServiceInterface }} {
    return {{ .MCPAlias }}.NewMCPAdapter({{ .UserAlias }}.{{ .UserEndpointsConstructor }}({{ .UserConstructorName }}(){{ if .UserInterceptorsConstructor }}, {{ .UserInterceptorsAlias }}.{{ .UserInterceptorsConstructor }}(){{ end }}), nil)
}
