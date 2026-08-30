{{- if .MCPToolsets }}
const (
{{- range .MCPToolsets }}
    // {{ .ConstName }} uniquely identifies the {{ .QualifiedName }} MCP toolset binding.
    {{ .ConstName }} = {{ printf "%q" .QualifiedName }}
{{- end }}
)
{{- end }}

// {{ .ConfigType }} configures the {{ .StructName }} agent package.
type {{ .ConfigType }} struct {
    // Planner provides the concrete planner implementation used by the agent.
    Planner {{ .PlannerAlias }}.Planner
{{- if .RunPolicy.History }}
    {{- if eq .RunPolicy.History.Mode "compress" }}
    // HistoryModel provides the model client used for history compression when a
    // compression history policy is configured. Token-budget compression counts
    // tokens through this client at runtime because tokenization is model-specific.
    HistoryModel {{ .ModelAlias }}.Client

    // HistoryCompression overrides the DSL compression defaults for this
    // deployment. Leave nil to use the generated defaults. Set this when the
    // configured HistoryModel has a different context window or operational
    // budget than the design-time default.
    HistoryCompression *{{ .RuntimeAlias }}.HistoryCompressionConfig
    {{- end }}
{{- end }}
{{- if .MCPToolsets }}
    // MCPCallers maps MCP toolset IDs to the callers that invoke them. A caller must be
    // provided for every toolset referenced via MCPToolset/Use.
    MCPCallers map[string]{{ .MCPRuntimeAlias }}.Caller
{{- end }}
}

// Validate ensures the configuration is usable.
func (c {{ .ConfigType }}) Validate() error {
    if c.Planner == nil {
        return {{ .ErrorsAlias }}.New("planner is required")
    }
{{- if .RunPolicy.History }}
    {{- if eq .RunPolicy.History.Mode "compress" }}
    if c.HistoryModel == nil {
        return {{ .ErrorsAlias }}.New("history model is required when Compress history policy is configured")
    }
    if c.HistoryCompression != nil {
        if err := c.HistoryCompression.Validate(); err != nil {
            return err
        }
    }
    {{- end }}
{{- end }}
{{- if .MCPToolsets }}
    if c.MCPCallers == nil {
        return {{ .FmtAlias }}.Errorf("mcp caller for %s is required", {{ (index .MCPToolsets 0).ConstName }})
    }
{{- range .MCPToolsets }}
    if c.MCPCallers[{{ .ConstName }}] == nil {
        return {{ $.FmtAlias }}.Errorf("mcp caller for %s is required", {{ .ConstName }})
    }
{{- end }}
{{- end }}
    return nil
}

{{- if .MCPToolsets }}

// WithMCPCaller adds or replaces the caller for the given MCP toolset ID and returns
// the config pointer for chaining in builder-style initialization.
func (c *{{ .ConfigType }}) WithMCPCaller(id string, caller {{ .MCPRuntimeAlias }}.Caller) *{{ .ConfigType }} {
    if c.MCPCallers == nil {
        c.MCPCallers = make(map[string]{{ .MCPRuntimeAlias }}.Caller)
    }
    c.MCPCallers[id] = caller
    return c
}
{{- end }}
