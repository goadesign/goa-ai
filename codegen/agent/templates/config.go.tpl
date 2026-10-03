// {{ .ConfigType }} configures the {{ .StructName }} agent package.
type {{ .ConfigType }} struct {
    // Planner provides the concrete planner implementation used by the agent.
    Planner {{ .PlannerAlias }}.Planner
{{- if .RunPolicy.History }}
    {{- if eq .RunPolicy.History.Mode "compress" }}
    // HistoryModel writes summaries of older messages. The destination client
    // for each actual model request supplies its token counts separately.
    HistoryModel {{ .ModelAlias }}.Client

    // HistoryCompression overrides the DSL compression defaults for this
    // deployment. Leave nil to use the generated defaults. Set this when the
    // configured destination model has a different context window or operational
    // budget than the design-time default.
    HistoryCompression *{{ .RuntimeAlias }}.HistoryCompressionConfig
    {{- end }}
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
    return nil
}
