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
{{- if .RunPolicy.History }}
    {{- if eq .RunPolicy.History.Mode "compress" }}
    // HistoryModel provides the model client used for history compression when a
    // Compress history policy is configured.
    HistoryModel model.Client
    {{- end }}
{{- end }}
{{- if .MCPToolsets }}
    // MCPCallers maps MCP toolset IDs to the callers that invoke them. A caller must be
    // provided for every toolset referenced via MCPToolset/Use.
    MCPCallers map[string]mcpruntime.Caller
{{- end }}
}

// Validate ensures the configuration is usable.
func (c {{ .ConfigType }}) Validate() error {
    if c.Planner == nil {
        return errors.New("planner is required")
    }
{{- if .RunPolicy.History }}
    {{- if eq .RunPolicy.History.Mode "compress" }}
    if c.HistoryModel == nil {
        return errors.New("history model is required when Compress history policy is configured")
    }
    {{- end }}
{{- end }}
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

{{- if .MCPToolsets }}

// WithMCPCaller adds or replaces the caller for the given MCP toolset ID and returns
// the config pointer for chaining in builder-style initialization.
func (c *{{ .ConfigType }}) WithMCPCaller(id string, caller mcpruntime.Caller) *{{ .ConfigType }} {
    if c.MCPCallers == nil {
        c.MCPCallers = make(map[string]mcpruntime.Caller)
    }
    c.MCPCallers[id] = caller
    return c
}
{{- end }}
