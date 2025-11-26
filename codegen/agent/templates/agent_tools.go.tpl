// Name is the DSL-declared name for the exported toolset "{{ .Toolset.Name }}".
const Name = {{ printf "%q" .Toolset.Name }}

// Service identifies the service that defined the toolset.
const Service = {{ printf "%q" .Toolset.ServiceName }}

// AgentID is the fully-qualified identifier of the agent exporting this toolset.
const AgentID agent.Ident = {{ printf "%q" .Toolset.Agent.ID }}

// Tool IDs for this exported toolset (globally unique). Use these typed
// constants as keys for per-tool configuration maps (e.g., SystemPrompts).
const (
{{- range .Toolset.Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
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
// and returns the final response as the tool result. DSL-authored CallHintTemplate and
// ResultHintTemplate declarations are compiled into hint templates so sinks can render
// concise labels and previews without heuristics.
//
// Example usage:
//
//	rt := runtime.New(...)
//	reg := New{{ .Toolset.Agent.GoName }}ToolsetRegistration(rt)
//	if err := rt.RegisterToolset(reg); err != nil {
//		// handle error
//	}
func New{{ .Toolset.Agent.GoName }}ToolsetRegistration(rt *runtime.Runtime) runtime.ToolsetRegistration {
    cfg := runtime.AgentToolConfig{
        AgentID:   AgentID,
        Name:      {{ printf "%q" .Toolset.QualifiedName }},
        TaskQueue: {{ printf "%q" .Toolset.TaskQueue }},
        Route: runtime.AgentRoute{
			ID:               AgentID,
			WorkflowName:     {{ printf "%q" .Toolset.Agent.Runtime.Workflow.Name }},
			DefaultTaskQueue: {{ printf "%q" .Toolset.Agent.Runtime.Workflow.Queue }},
		},
        PlanActivityName:    {{ printf "%q" .Toolset.Agent.Runtime.PlanActivity.Name }},
        ResumeActivityName:  {{ printf "%q" .Toolset.Agent.Runtime.ResumeActivity.Name }},
        ExecuteToolActivity: {{ printf "%q" .Toolset.Agent.Runtime.ExecuteTool.Name }},
    }
    reg := runtime.NewAgentToolsetRegistration(rt, cfg)
    // Install DSL-provided hint templates when present.
    {
        // Build maps only when at least one template exists to avoid overhead.
        var callRaw map[tools.Ident]string
        var resultRaw map[tools.Ident]string
        {{- range .Toolset.Tools }}
        {{- if .CallHintTemplate }}
        if callRaw == nil {
            callRaw = make(map[tools.Ident]string)
        }
        callRaw[{{ .ConstName }}] = {{ printf "%q" .CallHintTemplate }}
        {{- end }}
        {{- if .ResultHintTemplate }}
        if resultRaw == nil {
            resultRaw = make(map[tools.Ident]string)
        }
        resultRaw[{{ .ConstName }}] = {{ printf "%q" .ResultHintTemplate }}
        {{- end }}
        {{- end }}
        if len(callRaw) > 0 {
            compiled, err := hints.CompileHintTemplates(callRaw, nil)
            if err != nil {
                panic(err)
            }
            reg.CallHints = compiled
        }
        if len(resultRaw) > 0 {
            compiled, err := hints.CompileHintTemplates(resultRaw, nil)
            if err != nil {
                panic(err)
            }
            reg.ResultHints = compiled
        }
    }
    return reg
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
        Name:         {{ printf "%q" .Toolset.QualifiedName }},
        TaskQueue:    {{ printf "%q" .Toolset.TaskQueue }},
        SystemPrompt: systemPrompt,
        // Strong-contract routing for cross-process inline composition
        Route: runtime.AgentRoute{
			ID:              AgentID,
			WorkflowName:    {{ printf "%q" .Toolset.Agent.Runtime.Workflow.Name }},
			DefaultTaskQueue: {{ printf "%q" .Toolset.Agent.Runtime.Workflow.Queue }},
		},
        PlanActivityName:    {{ printf "%q" .Toolset.Agent.Runtime.PlanActivity.Name }},
        ResumeActivityName:  {{ printf "%q" .Toolset.Agent.Runtime.ResumeActivity.Name }},
        ExecuteToolActivity: {{ printf "%q" .Toolset.Agent.Runtime.ExecuteTool.Name }},
    }
    for _, o := range opts {
        o(&cfg)
    }
    // Validate only for the templates explicitly provided (optional)
    if len(cfg.Templates) > 0 {
        ids := make([]tools.Ident, 0, len(cfg.Templates))
        for id := range cfg.Templates {
            ids = append(ids, id)
        }
        if err := runtime.ValidateAgentToolTemplates(cfg.Templates, ids, nil); err != nil {
            return runtime.ToolsetRegistration{}, err
        }
    }
    reg := runtime.NewAgentToolsetRegistration(rt, cfg)
    // Install DSL-provided hint templates when present.
    {
        // Build maps only when at least one template exists to avoid overhead.
        var callRaw map[tools.Ident]string
        var resultRaw map[tools.Ident]string
        {{- range .Toolset.Tools }}
        {{- if .CallHintTemplate }}
        if callRaw == nil {
            callRaw = make(map[tools.Ident]string)
        }
        callRaw[{{ .ConstName }}] = {{ printf "%q" .CallHintTemplate }}
        {{- end }}
        {{- if .ResultHintTemplate }}
        if resultRaw == nil {
            resultRaw = make(map[tools.Ident]string)
        }
        resultRaw[{{ .ConstName }}] = {{ printf "%q" .ResultHintTemplate }}
        {{- end }}
        {{- end }}
        if len(callRaw) > 0 {
            compiled, err := hints.CompileHintTemplates(callRaw, nil)
            if err != nil {
                panic(err)
            }
            reg.CallHints = compiled
        }
        if len(resultRaw) > 0 {
            compiled, err := hints.CompileHintTemplates(resultRaw, nil)
            if err != nil {
                panic(err)
            }
            reg.ResultHints = compiled
        }
    }
    return reg, nil
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
    var payload []byte
    if args != nil {
        // Encode typed payloads into canonical JSON using the generated codec.
        b, err := {{ goify .Name true }}PayloadCodec.ToJSON(args)
        if err != nil {
            panic(err)
        }
        payload = b
    }
    req := planner.ToolRequest{
        Name:    {{ .ConstName }},
        Payload: payload,
    }
    for _, o := range opts {
        if o != nil {
            o(&req)
        }
    }
    return req
}
{{- end }}
