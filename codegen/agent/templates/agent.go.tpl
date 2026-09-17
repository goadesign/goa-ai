{{- define "agentDefinitionTools" -}}
{{- if .Tools }}
    {{ .ToolSpecsAlias }}.Specs(),
    {{ .ToolSpecsAlias }}.MetadataByName,
    {{ .ToolSpecsAlias }}.RequiredLabels(),
    []{{ .ToolsAlias }}.Ident{
{{- range .UsedToolsets }}
{{- range .Tools }}
        {{ $.ToolsAlias }}.Ident({{ printf "%q" .QualifiedName }}),
{{- end }}
{{- end }}
    },
{{- else }}
    nil,
    nil,
    nil,
    nil,
{{- end }}
{{- end }}

{{- define "agentDefinitionDeferral" -}}
{{- if .DeferredTools }}
    []{{ .ToolsAlias }}.Ident{
    {{- range .DeferredTools }}
        {{ printf "%q" . }},
    {{- end }}
    },
{{- else }}
    nil,
{{- end }}
{{- end }}

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

var {{ .PackageNames.DefinitionValue }} = {{ .RuntimeAlias }}.NewAgentDefinition(
    {{ .RuntimeAlias }}.AgentRoute{
        ID:               {{ .PackageNames.AgentID }},
        WorkflowName:     {{ .PackageNames.WorkflowName }},
        DefaultTaskQueue: {{ .PackageNames.DefaultTaskQueue }},
    },
{{- template "agentDefinitionTools" .RootDefinition }}
{{- if .ChildDefinitions }}
    []{{ .RuntimeAlias }}.AgentDefinition{
{{- range .ChildDefinitions }}
        {{ $.RuntimeAlias }}.NewAgentDefinition(
            {{ $.RuntimeAlias }}.AgentRoute{
                ID:               {{ $.AgentAlias }}.Ident({{ printf "%q" .ID }}),
                WorkflowName:     {{ printf "%q" .Runtime.Workflow.Name }},
                DefaultTaskQueue: {{ printf "%q" .Runtime.Workflow.Queue }},
            },
{{- template "agentDefinitionTools" . }}
            nil,
{{- template "agentDefinitionDeferral" . }}
        ){{ if .RegistrySources }}.WithRegistryTools({{ .RegistrySources.TypeName }}{}){{ end }},
{{- end }}
    },
{{- else }}
    nil,
{{- end }}
{{- template "agentDefinitionDeferral" .RootDefinition }}
){{ if .RootDefinition.RegistrySources }}.WithRegistryTools({{ .RootDefinition.RegistrySources.TypeName }}{}){{ end }}

// {{ .PackageNames.Definition }} returns the immutable generated contract shared by callers and workers.
func {{ .PackageNames.Definition }}() {{ .RuntimeAlias }}.AgentDefinition {
	return {{ .PackageNames.DefinitionValue }}
}

// {{ .PackageNames.NewClient }} returns a runtime.AgentClient bound to this agent. In caller
// processes that do not register the agent locally, it still validates starts
// against the same generated contract as the worker.
func {{ .PackageNames.NewClient }}(rt *{{ .RuntimeAlias }}.Runtime) {{ .RuntimeAlias }}.AgentClient {
    return rt.MustClientFor({{ .PackageNames.Definition }}())
}

{{- range .RegistrySources }}
// {{ .TypeName }} contains only source operations known from the design.
type {{ .TypeName }} struct{}

// Resolve reads the declared sources once for the current planning activity.
func ({{ .TypeName }}) Resolve(ctx {{ $.ContextAlias }}.Context, catalog *{{ $.RuntimeAlias }}.RegistryCatalog) error {
{{- range .Sources }}
    {{- if .ToolsetName }}
    if err := catalog.IncludeToolset(ctx, {{ printf "%q" .RegistryName }}, {{ printf "%q" .ToolsetName }}, {{ printf "%q" .Version }}, {{ .Deferred }}); err != nil {
    {{- else }}
    if err := catalog.IncludeRegistry(ctx, {{ printf "%q" .RegistryName }}, {{ .Deferred }}); err != nil {
    {{- end }}
        return err
    }
{{- end }}
    return nil
}
{{- $named := false }}
{{- $versioned := false }}
{{- range .Sources }}
    {{- if .ToolsetName }}{{- $named = true }}{{- end }}
    {{- if .Version }}{{- $versioned = true }}{{- end }}
{{- end }}

// Allows checks the saved source against this agent's generated consumption.
func ({{ .TypeName }}) Allows(registry, {{ if $named }}toolset{{ else }}_{{ end }}, {{ if $versioned }}version{{ else }}_{{ end }} string) bool {
    return {{ range $index, $source := .Sources }}{{ if $index }} || {{ end }}(registry == {{ printf "%q" .RegistryName }}{{ if .ToolsetName }} && toolset == {{ printf "%q" .ToolsetName }}{{ end }}{{ if .Version }} && version == {{ printf "%q" .Version }}{{ end }}){{ end }}
}
{{- end }}
