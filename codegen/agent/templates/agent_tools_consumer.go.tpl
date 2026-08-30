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
    systemPrompt string,
    opts ...{{ .RuntimeAlias }}.AgentToolOption,
) ({{ .RuntimeAlias }}.ToolsetRegistration, error) {
    definition, ok := {{ .Definition }}().ChildDefinition({{ .ProviderAlias }}.AgentID)
    if !ok {
        panic("generated agent definition is missing its child agent")
    }
    return {{ .ProviderAlias }}.{{ .ProviderRegistrationConstructor }}(rt, definition, systemPrompt, opts...)
}
