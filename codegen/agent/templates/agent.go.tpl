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

// Run invokes this agent using the provided runtime and messages.
// sessionID is required and is applied via runtime.WithSessionID.
func Run(ctx context.Context, rt *runtime.Runtime, sessionID string, messages []planner.AgentMessage, opts ...runtime.RunOption) (runtime.RunOutput, error) {
    if strings.TrimSpace(sessionID) == "" {
        return runtime.RunOutput{}, errors.New("session id is required")
    }
    opts = append(opts, runtime.WithSessionID(sessionID))
    return rt.RunAgent(ctx, {{ printf "%q" .ID }}, messages, opts...)
}

// Start begins execution of this agent and returns a workflow handle.
// sessionID is required and is applied via runtime.WithSessionID.
func Start(ctx context.Context, rt *runtime.Runtime, sessionID string, messages []planner.AgentMessage, opts ...runtime.RunOption) (engine.WorkflowHandle, error) {
    if strings.TrimSpace(sessionID) == "" {
        return nil, errors.New("session id is required")
    }
    opts = append(opts, runtime.WithSessionID(sessionID))
    return rt.StartAgent(ctx, {{ printf "%q" .ID }}, messages, opts...)
}
