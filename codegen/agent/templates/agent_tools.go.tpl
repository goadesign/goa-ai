// Name is the DSL-declared name for the exported toolset "{{ .Toolset.Name }}".
const Name = {{ printf "%q" .Toolset.Name }}

// Service identifies the service that defined the toolset.
const Service = {{ printf "%q" .Toolset.ServiceName }}

// AgentID is the fully-qualified identifier of the agent exporting this toolset.
const AgentID = {{ printf "%q" .Toolset.Agent.ID }}

// Tool IDs for this exported toolset (fully qualified). Use these typed
// constants as keys for per-tool configuration maps (e.g., SystemPrompts).
const (
{{- range .Toolset.Tools }}
    {{ .ConstName }} tools.ID = {{ printf "%q" .QualifiedName }}
{{- end }}
)

// New{{ .Toolset.Agent.GoName }}ToolsetRegistration creates a toolset registration for the {{ .Toolset.Agent.Name }} agent.
// The returned registration can be used with runtime.RegisterToolset to make the agent
// available as a tool to other agents. When invoked, the agent runs its full planning loop
// and returns the final response as the tool result.
//
// Example usage:
//
//	rt := runtime.New(...)
//	reg := New{{ .Toolset.Agent.GoName }}ToolsetRegistration(rt)
//	if err := rt.RegisterToolset(reg); err != nil {
//		// handle error
//	}
func New{{ .Toolset.Agent.GoName }}ToolsetRegistration(rt *runtime.Runtime) runtime.ToolsetRegistration {
    // Build a default agent-tool registration using runtime helper (no templates).
    return runtime.NewAgentToolsetRegistration(rt, runtime.AgentToolConfig{
        AgentID:   AgentID,
        Name:      {{ printf "%q" .Toolset.QualifiedName }},
        TaskQueue: {{ printf "%q" .Toolset.TaskQueue }},
    })
}

// ToolIDs lists all tools in this toolset for validation.
var ToolIDs = []tools.ID{
{{- range .Toolset.Tools }}
    {{ .ConstName }},
{{- end }}
}

// NewRegistration creates a toolset registration with an optional agent-wide
// system prompt and per-tool content configured via runtime options. Callers
// can mix text and templates; each tool must be configured in exactly one way.
func NewRegistration(
    rt *runtime.Runtime,
    systemPrompt string,
    opts ...runtime.AgentToolOption,
) (runtime.ToolsetRegistration, error) {
    cfg := runtime.AgentToolConfig{
        AgentID:      AgentID,
        Name:         Name,
        TaskQueue:    {{ printf "%q" .Toolset.TaskQueue }},
        SystemPrompt: systemPrompt,
    }
    for _, o := range opts { o(&cfg) }
    if err := runtime.ValidateAgentToolCoverage(cfg.Texts, cfg.Templates, ToolIDs); err != nil {
        return runtime.ToolsetRegistration{}, err
    }
    if len(cfg.Templates) > 0 {
        if err := runtime.ValidateAgentToolTemplates(cfg.Templates, ToolIDs, nil); err != nil {
            return runtime.ToolsetRegistration{}, err
        }
    }
    return runtime.NewAgentToolsetRegistration(rt, cfg), nil
}
