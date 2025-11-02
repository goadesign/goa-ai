// AgentID is the fully-qualified identifier for this agent.
const AgentID agent.Ident = {{ printf "%q" .ID }}

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

// NewWorker returns a per-agent worker configuration. Engines that support
// workers (e.g., Temporal) use this to bind the agent's workflow and activities
// to a specific queue. Supplying no options uses the generated default queue.
func NewWorker(opts ...runtime.WorkerOption) runtime.WorkerConfig {
    var cfg runtime.WorkerConfig
    for _, o := range opts {
        if o != nil {
            o(&cfg)
        }
    }
    return cfg
}

// Route returns the minimal route required to construct a client in a
// caller process without registering the agent locally.
func Route() runtime.AgentRoute {
    return runtime.AgentRoute{
        ID:              AgentID,
        WorkflowName:    {{ printf "%q" .Runtime.Workflow.Name }},
        DefaultTaskQueue: {{ printf "%q" .Runtime.Workflow.Queue }},
    }
}

// NewClient returns a runtime.AgentClient bound to this agent. In caller
// processes that do not register the agent locally, this uses ClientMeta to
// construct a client that can start workflows against remote workers.
func NewClient(rt *runtime.Runtime) runtime.AgentClient {
    return rt.MustClientFor(Route())
}
