// New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}AgentToolsetRegistration creates a
// ToolsetRegistration for the {{ .Toolset.Name }} toolset exported by the
// {{ .Toolset.SourceServiceName }} service. It delegates to the provider's
// agenttools.NewRegistration helper so callers can configure system prompts and
// AgentToolOption values while keeping routing metadata centralized with the
// exporting agent.
//
// Example:
//
//	reg, err := New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}AgentToolsetRegistration(
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
func New{{ .Agent.GoName }}{{ goify .Toolset.PathName true }}AgentToolsetRegistration(
    rt *runtime.Runtime,
    systemPrompt string,
    opts ...runtime.AgentToolOption,
) (runtime.ToolsetRegistration, error) {
    return {{ .ProviderAlias }}.NewRegistration(rt, systemPrompt, opts...)
}

