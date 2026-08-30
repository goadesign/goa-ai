// {{ .PackageNames.AgentID }} is the fully-qualified identifier for this agent.
const {{ .PackageNames.AgentID }} {{ .AgentAlias }}.Ident = {{ printf "%q" .ID }}

// Workflow and activity identifiers for this agent.
const (
    // {{ .PackageNames.WorkflowName }} is the fully-qualified workflow identifier registered with the engine.
    {{ .PackageNames.WorkflowName }} = {{ printf "%q" .Runtime.Workflow.Name }}
    // {{ .PackageNames.DefaultTaskQueue }} is the engine queue this agent polls for workflow and activity tasks.
    {{ .PackageNames.DefaultTaskQueue }} = {{ printf "%q" .Runtime.Workflow.Queue }}
    // {{ .PackageNames.PlanActivity }} is the activity name that runs the initial planning turn.
    {{ .PackageNames.PlanActivity }} = {{ printf "%q" .Runtime.PlanActivity.Name }}
    // {{ .PackageNames.ResumeActivity }} is the activity name that runs the resume turn after tool execution.
    {{ .PackageNames.ResumeActivity }} = {{ printf "%q" .Runtime.ResumeActivity.Name }}
    // {{ .PackageNames.ExecuteToolActivity }} is the activity name used to execute tools via the engine.
    {{ .PackageNames.ExecuteToolActivity }} = {{ printf "%q" .Runtime.ExecuteTool.Name }}
)

// {{ .StructName }} wraps the planner implementation for agent "{{ .Name }}".
type {{ .StructName }} struct {
    Planner {{ .PlannerAlias }}.Planner
}

// {{ .PackageNames.Constructor }} validates the configuration and constructs a {{ .StructName }}.
func {{ .PackageNames.Constructor }}(cfg {{ .ConfigType }}) (*{{ .StructName }}, error) {
    if err := cfg.Validate(); err != nil {
        return nil, err
    }
    return &{{ .StructName }}{Planner: cfg.Planner}, nil
}

// {{ .PackageNames.NewWorker }} returns a per-agent worker configuration. Engines that support
// workers (e.g., Temporal) use this to bind the agent's workflow and activities
// to a specific queue. Supplying no options uses the generated default queue.
func {{ .PackageNames.NewWorker }}(opts ...{{ .RuntimeAlias }}.WorkerOption) {{ .RuntimeAlias }}.WorkerConfig {
    var cfg {{ .RuntimeAlias }}.WorkerConfig
    for _, o := range opts {
        if o != nil {
            o(&cfg)
        }
    }
    return cfg
}

// {{ .PackageNames.Route }} returns the minimal route required to construct a client in a
// caller process without registering the agent locally.
func {{ .PackageNames.Route }}() {{ .RuntimeAlias }}.AgentRoute {
    return {{ .RuntimeAlias }}.AgentRoute{
        ID:               {{ .PackageNames.AgentID }},
        WorkflowName:     {{ .PackageNames.WorkflowName }},
        DefaultTaskQueue: {{ .PackageNames.DefaultTaskQueue }},
    }
}

// {{ .PackageNames.NewClient }} returns a runtime.AgentClient bound to this agent. In caller
// processes that do not register the agent locally, this uses ClientMeta to
// construct a client that can start workflows against remote workers.
func {{ .PackageNames.NewClient }}(rt *{{ .RuntimeAlias }}.Runtime) {{ .RuntimeAlias }}.AgentClient {
    return rt.MustClientFor({{ .PackageNames.Route }}())
}
