// Name is the DSL-declared name for the exported toolset "{{ .Toolset.Name }}".
const Name = {{ printf "%q" .Toolset.Name }}

// Service identifies the service that defined the toolset.
const Service = {{ printf "%q" .Toolset.ServiceName }}

// AgentID is the fully-qualified identifier of the agent exporting this toolset.
const AgentID agent.Ident = {{ printf "%q" .Toolset.Agent.ID }}

// Tool IDs for this exported toolset (fully qualified). Use these typed
// constants as keys for per-tool configuration maps (e.g., SystemPrompts).
const (
{{- range .Toolset.Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .QualifiedName }}
{{- end }}
)

// Type aliases and codec re-exports for convenience. These aliases preserve exact
// type identity while allowing callers to avoid importing the specs package.
{{- range .Toolset.Tools }}
type {{ goify .Name true }}Payload = {{ $.Toolset.SpecsPackageName }}specs.{{ goify .Name true }}Payload
type {{ goify .Name true }}Result  = {{ $.Toolset.SpecsPackageName }}specs.{{ goify .Name true }}Result

var {{ goify .Name true }}PayloadCodec = {{ $.Toolset.SpecsPackageName }}specs.{{ goify .Name true }}PayloadCodec
var {{ goify .Name true }}ResultCodec  = {{ $.Toolset.SpecsPackageName }}specs.{{ goify .Name true }}ResultCodec
{{- end }}

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
var ToolIDs = []tools.Ident{
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
    // Validate only for the templates explicitly provided (optional)
    if len(cfg.Templates) > 0 {
        ids := make([]tools.Ident, 0, len(cfg.Templates))
        for id := range cfg.Templates { ids = append(ids, id) }
        if err := runtime.ValidateAgentToolTemplates(cfg.Templates, ids, nil); err != nil {
            return runtime.ToolsetRegistration{}, err
        }
    }
    return runtime.NewAgentToolsetRegistration(rt, cfg), nil
}

// CallOption customizes planner.ToolRequest values built by the typed helpers
// below (e.g., setting parent/tool-call IDs for correlation with model calls).
type CallOption func(*planner.ToolRequest)

// WithParentToolCallID sets the ParentToolCallID on the constructed request.
func WithParentToolCallID(id string) CallOption {
    return func(r *planner.ToolRequest) { r.ParentToolCallID = id }
}

// WithToolCallID sets a model/tool-call identifier on the request. The runtime
// preserves this ID and echoes it in ToolResult.ToolCallID for correlation.
func WithToolCallID(id string) CallOption {
    return func(r *planner.ToolRequest) { r.ToolCallID = id }
}

// Typed tool-call helpers for each tool in this exported toolset. These helpers
// enforce use of the generated tool identifier and accept a typed payload that
// matches the tool schema.
{{- range .Toolset.Tools }}
// New{{ goify .Name true }}Call builds a planner.ToolRequest for the {{ .QualifiedName }} tool.
func New{{ goify .Name true }}Call(args *{{ goify .Name true }}Payload, opts ...CallOption) planner.ToolRequest {
    req := planner.ToolRequest{
        Name:    {{ .ConstName }},
        Payload: args,
    }
    for _, o := range opts {
        if o != nil {
            o(&req)
        }
    }
    return req
}
{{- end }}
