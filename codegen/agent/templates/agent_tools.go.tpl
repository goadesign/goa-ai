// {{ .ToolsetName }} identifies this exported toolset during registration.
const {{ .ToolsetName }} = {{ printf "%q" .Toolset.QualifiedName }}

// {{ .ServiceName }} identifies the service that exposes the toolset.
const {{ .ServiceName }} = {{ printf "%q" .Toolset.ServiceName }}

// {{ .AgentIDName }} identifies the agent that runs calls to this toolset.
const {{ .AgentIDName }} {{ .AgentAlias }}.Ident = {{ printf "%q" .Toolset.Agent.ID }}

// Tool IDs for this exported toolset (globally unique). Use these typed
// constants as keys for per-tool configuration maps (e.g., SystemPrompts).
const (
{{- range .Tools }}
    // {{ .ConstName }} is the canonical tool identifier for {{ .Name }}.
    // Tool IDs are always the fully-qualified "<toolset>.<tool>" form so they
    // match Specs entries, planner requests, and runtime stream events exactly.
    {{ .ConstName }} {{ $.ToolsAlias }}.Ident = {{ printf "%q" .Name }}
{{- end }}
)

// {{ .SpecsFunc }} returns fresh tool specifications for this exported toolset.
func {{ .SpecsFunc }}() []{{ .ToolsAlias }}.ToolSpec {
    specs := make([]{{ .ToolsAlias }}.ToolSpec, 0, {{ len .Tools }})
    {{- range .Tools }}
    {
        spec := {{ $.SpecsAlias }}.{{ .SpecVar }}()
        spec.IsAgentTool = true
        spec.AgentID = string({{ $.AgentIDName }})
        specs = append(specs, spec)
    }
    {{- end }}
    return specs
}

// Type aliases preserve exact tool payload and result identities.
{{- range .Tools }}
type {{ .PayloadAlias }} = {{ $.SpecsAlias }}.{{ .Payload.TypeName }}
{{- if .ResultAlias }}
type {{ .ResultAlias }}  = {{ $.SpecsAlias }}.{{ .Result.TypeName }}
{{- end }}
{{- end }}

{{- $hasCallHints := false -}}
{{- $hasResultHints := false -}}
{{- range .Toolset.Tools }}
{{- if .CallHintTemplate }}{{- $hasCallHints = true -}}{{- end }}
{{- if .ResultHintTemplate }}{{- $hasResultHints = true -}}{{- end }}
{{- end }}
{{- if or $hasCallHints $hasResultHints }}
func {{ .HintsInstaller }}(reg *{{ .RuntimeAlias }}.ToolsetRegistration) error {
    {{- if $hasCallHints }}
    callHints, err := {{ .HintsAlias }}.CompileHintTemplates(map[{{ .ToolsAlias }}.Ident]string{
    {{- range .Toolset.Tools }}
    {{- if .CallHintTemplate }}
        {{ .ConstName }}: {{ printf "%q" .CallHintTemplate }},
    {{- end }}
    {{- end }}
    }, nil)
    if err != nil {
        return err
    }
    reg.CallHints = callHints
    {{- end }}
    {{- if $hasResultHints }}
    resultHints, err := {{ .HintsAlias }}.CompileHintTemplates(map[{{ .ToolsAlias }}.Ident]string{
    {{- range .Toolset.Tools }}
    {{- if .ResultHintTemplate }}
        {{ .ConstName }}: {{ printf "%q" .ResultHintTemplate }},
    {{- end }}
    {{- end }}
    }, nil)
    if err != nil {
        return err
    }
    reg.ResultHints = resultHints
    {{- end }}
    return nil
}
{{- end }}
// {{ .ProviderConstructor }} creates a toolset registration for the {{ .Toolset.Agent.Name }} agent.
// Pass the returned value to {{ .RuntimeAlias }}.RegisterToolset to let other agents call
// this agent. Each call starts the agent and returns its final response. The
// generated registration also includes the labels and previews declared for
// tool calls and results.
//
// Example usage:
//
//	rt := {{ .RuntimeAlias }}.New(...)
//	reg := {{ .ProviderConstructor }}(rt)
//	if err := rt.RegisterToolset(reg); err != nil {
//		// handle error
//	}
func {{ .ProviderConstructor }}(rt *{{ .RuntimeAlias }}.Runtime) {{ .RuntimeAlias }}.ToolsetRegistration {
    cfg := {{ .RuntimeAlias }}.AgentToolConfig{
        AgentID:   {{ .AgentIDName }},
        Name:      {{ .ToolsetName }},
        TaskQueue: {{ printf "%q" .Toolset.TaskQueue }},
        Route: {{ .RuntimeAlias }}.AgentRoute{
			ID:               {{ .AgentIDName }},
			WorkflowName:     {{ printf "%q" .Toolset.Agent.Runtime.Workflow.Name }},
			DefaultTaskQueue: {{ printf "%q" .Toolset.Agent.Runtime.Workflow.Queue }},
		},
        PlanActivityName:    {{ printf "%q" .Toolset.Agent.Runtime.PlanActivity.Name }},
        ResumeActivityName:  {{ printf "%q" .Toolset.Agent.Runtime.ResumeActivity.Name }},
        ExecuteToolActivity: {{ printf "%q" .Toolset.Agent.Runtime.ExecuteTool.Name }},
    }
    reg := {{ .RuntimeAlias }}.NewAgentToolsetRegistration(rt, cfg)
    reg.Specs = {{ .SpecsFunc }}()
    reg.ToolMetadataLookup = {{ .SpecsAlias }}.MetadataByName
    {{- if or $hasCallHints $hasResultHints }}
    if err := {{ .HintsInstaller }}(&reg); err != nil {
        panic(err)
    }
    {{- end }}
    return reg
}

// {{ .RegistrationConstructor }} creates a toolset registration with an optional
// system prompt and content for individual tools. Callers can mix text and
// templates, but each tool must use exactly one form.
func {{ .RegistrationConstructor }}(
    rt *{{ .RuntimeAlias }}.Runtime,
    systemPrompt string,
    opts ...{{ .RuntimeAlias }}.AgentToolOption,
) ({{ .RuntimeAlias }}.ToolsetRegistration, error) {
    cfg := {{ .RuntimeAlias }}.AgentToolConfig{
        AgentID:      {{ .AgentIDName }},
        Name:         {{ .ToolsetName }},
        TaskQueue:    {{ printf "%q" .Toolset.TaskQueue }},
        SystemPrompt: systemPrompt,
        // Route identifies the workflow and queue that run the child agent.
        Route: {{ .RuntimeAlias }}.AgentRoute{
			ID:              {{ .AgentIDName }},
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
    // Check only the templates provided by the caller.
    if len(cfg.Templates) > 0 {
        ids := make([]{{ .ToolsAlias }}.Ident, 0, len(cfg.Templates))
        for id := range cfg.Templates {
            ids = append(ids, id)
        }
        if err := {{ .RuntimeAlias }}.ValidateAgentToolTemplates(cfg.Templates, ids, nil); err != nil {
            return {{ .RuntimeAlias }}.ToolsetRegistration{}, err
        }
    }
    reg := {{ .RuntimeAlias }}.NewAgentToolsetRegistration(rt, cfg)
    reg.Specs = {{ .SpecsFunc }}()
    reg.ToolMetadataLookup = {{ .SpecsAlias }}.MetadataByName
    {{- if or $hasCallHints $hasResultHints }}
    if err := {{ .HintsInstaller }}(&reg); err != nil {
        return {{ .RuntimeAlias }}.ToolsetRegistration{}, err
    }
    {{- end }}
    return reg, nil
}

// Typed call helpers use the generated tool identifier and accept the generated
// payload type for each tool.
{{- range .Tools }}
// {{ .CallFunc }} builds a planner-authored request for the
// {{ .Name }} tool. The runtime assigns its execution ID.
func {{ .CallFunc }}(args {{ if .Payload.Pointer }}*{{ end }}{{ .PayloadAlias }}) ({{ $.PlannerAlias }}.ToolRequest, error) {
    return {{ $.PlannerAlias }}.NewToolRequest({{ $.SpecsAlias }}.{{ .TypedToolVar }}(), args)
}
{{- end }}
