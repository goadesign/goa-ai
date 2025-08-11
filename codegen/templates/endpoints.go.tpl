{{ comment "NewEndpoints creates MCP endpoints from the adapter" }}
func NewEndpoints(s {{ .MCPPackage }}.Service) *{{ .MCPPackage }}.Endpoints {
	return {{ .MCPPackage }}.NewEndpoints(s)
}