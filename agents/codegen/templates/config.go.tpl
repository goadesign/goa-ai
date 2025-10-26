{{- if .MCPToolsets }}
const (
{{- range .MCPToolsets }}
    // {{ .ConstName }} uniquely identifies the {{ .ServiceName }}.{{ .SuiteName }} MCP toolset.
    {{ .ConstName }} = {{ printf "%q" .QualifiedName }}
{{- end }}
)
{{- end }}

// {{ .ConfigType }} configures the {{ .StructName }} agent package.
type {{ .ConfigType }} struct {
    // Planner provides the concrete planner implementation used by the agent.
    Planner planner.Planner
{{- range .AllToolsets }}
{{- $hasMethod := false }}
{{- range .Tools }}{{- if .IsMethodBacked }}{{- $hasMethod = true }}{{- end }}{{- end }}
{{- if $hasMethod }}
    // {{ .Name | goify true }}Cfg configures method-backed tools for toolset {{ .Name }}.
    {{ .Name | goify true }}Cfg {{ .PackageName }}.{{ .Name | goify true }}Config
{{- end }}
{{- end }}
{{- if .MCPToolsets }}
    // MCPCallers maps MCP toolset IDs to the callers that invoke them. A caller must be
    // provided for every toolset referenced via UseMCPToolset.
    MCPCallers map[string]mcpruntime.Caller
{{- end }}
}

// Validate ensures the configuration is usable.
func (c {{ .ConfigType }}) Validate() error {
    if c.Planner == nil {
        return errors.New("planner is required")
    }
{{- if .MCPToolsets }}
    required := []string{
{{- range .MCPToolsets }}
        {{ .ConstName }},
{{- end }}
    }
    for _, id := range required {
        if c.MCPCallers == nil || c.MCPCallers[id] == nil {
            return fmt.Errorf("mcp caller for %s is required", id)
        }
    }
{{- end }}
    return nil
}
