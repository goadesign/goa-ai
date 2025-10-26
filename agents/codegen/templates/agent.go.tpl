// {{ .StructName }} wraps the planner implementation for agent "{{ .Name }}".
type {{ .StructName }} struct {
    Planner planner.Planner
}

// New{{ .StructName }} validates the configuration and constructs a {{ .StructName }}.
func New{{ .StructName }}(cfg {{ .ConfigType }}) (*{{ .StructName }}, error) {
    if err := cfg.Validate(); err != nil {
        return nil, err
    }
    return &{{ .StructName }}{Planner: cfg.Planner}, nil
}

// Run is a high-level helper that invokes this agent using the provided runtime
// and messages. It forwards options to the runtime helper.
func Run(ctx context.Context, rt *runtime.Runtime, messages []planner.AgentMessage, opts ...runtime.RunOption) (runtime.RunOutput, error) {
    return rt.RunAgent(ctx, {{ printf "%q" .ID }}, messages, opts...)
}

// Start is a high-level helper that starts this agent and returns a workflow handle.
// It forwards options to the runtime helper.
func Start(ctx context.Context, rt *runtime.Runtime, messages []planner.AgentMessage, opts ...runtime.RunOption) (engine.WorkflowHandle, error) {
    return rt.StartAgent(ctx, {{ printf "%q" .ID }}, messages, opts...)
}
