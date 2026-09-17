// {{ .Toolset.AgentToolsRegistrationConstructor }} creates a
// ToolsetRegistration for the {{ .Toolset.Name }} toolset exported by the
// {{ .Toolset.SourceServiceName }} service. It delegates to the provider's
// agenttools.NewRegistration helper so callers can configure system prompts and
// AgentToolOption values while keeping routing metadata centralized with the
// exporting agent.
//
// Example:
//
//	reg, err := {{ .Toolset.AgentToolsRegistrationConstructor }}(
//	    rt,
{{- if .RegistryBindings }}
//	    registryToolsets,
{{- end }}
//	    systemPrompt,
//	    opts...,
//	)
//	if err != nil {
//	    return err
//	}
//	if err := rt.RegisterToolset(reg); err != nil {
//	    return err
//	}
func {{ .Toolset.AgentToolsRegistrationConstructor }}(
    rt *{{ .RuntimeAlias }}.Runtime,
{{- if .RegistryBindings }}
    toolsets {{ .RegistryToolsetsType }},
{{- end }}
    systemPrompt string,
    opts ...{{ .RuntimeAlias }}.AgentToolOption,
) ({{ .RuntimeAlias }}.ToolsetRegistration, error) {
{{- if .RegistryBindings }}
    parent, err := {{ .Definition }}(toolsets)
    if err != nil {
        return {{ .RuntimeAlias }}.ToolsetRegistration{}, err
    }
    definition, ok := parent.ChildDefinition({{ .ProviderAlias }}.AgentID)
{{- else }}
    definition, ok := {{ .Definition }}().ChildDefinition({{ .ProviderAlias }}.AgentID)
{{- end }}
    if !ok {
        panic("generated agent definition is missing its child agent")
    }
    return {{ .ProviderAlias }}.{{ .ProviderRegistrationConstructor }}(rt, definition, systemPrompt, opts...)
}
