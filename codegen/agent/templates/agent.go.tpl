{{- define "agentDefinitionTools" -}}
{{- if .RegistryBindings }}
    func() []{{ .ToolsAlias }}.ToolSpec {
        {{- if .Tools }}
        specs := {{ .ToolSpecsAlias }}.Specs()
        {{- else }}
        var specs []{{ .ToolsAlias }}.ToolSpec
        {{- end }}
        {{- range .RegistryBindings }}
        specs = append(specs, toolsets.{{ .FieldName }}.Specs()...)
        {{- end }}
        return specs
    }(),
    func(name {{ .ToolsAlias }}.Ident) ({{ .PolicyAlias }}.ToolMetadata, bool) {
        {{- if .Tools }}
        if metadata, ok := {{ .ToolSpecsAlias }}.MetadataByName(name); ok {
            return metadata, true
        }
        {{- end }}
        {{- range .RegistryBindings }}
        if metadata, ok := toolsets.{{ .FieldName }}.MetadataByName(name); ok {
            return metadata, true
        }
        {{- end }}
        return {{ .PolicyAlias }}.ToolMetadata{}, false
    },
    {{- if .Tools }}
    {{ .ToolSpecsAlias }}.RequiredLabels(),
    {{- else }}
    nil,
    {{- end }}
    func() []{{ .ToolsAlias }}.Ident {
        names := []{{ .ToolsAlias }}.Ident{
        {{- range .UsedToolsets }}
        {{- range .Tools }}
            {{ printf "%q" .QualifiedName }},
        {{- end }}
        {{- end }}
        }
        {{- range .ExecutableRegistryBindings }}
        names = append(names, toolsets.{{ .FieldName }}.Names()...)
        {{- end }}
        return names
    }(),
{{- else if .Tools }}
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

{{- if .RegistryBindings }}
// {{ .RegistryToolsetsType }} supplies the toolsets discovered before this
// agent and its child agents are constructed. Each value is immutable.
type {{ .RegistryToolsetsType }} struct {
{{- range .RegistryBindings }}
    // {{ .FieldName }} contains {{ .ToolsetName }} from registry {{ .RegistryName }}.
    {{ .FieldName }} *{{ $.RegistryAlias }}.Toolset
{{- end }}
}

// Validate checks required toolsets, their names and versions, and unique tool names.
func (toolsets {{ .RegistryToolsetsType }}) Validate() error {
    names := map[{{ .ToolsAlias }}.Ident]string{
{{- range .StaticToolNames }}
        {{ printf "%q" . }}: "generated tool contract",
{{- end }}
    }
{{- range .RegistryBindings }}
    if toolsets.{{ .FieldName }} == nil {
        return {{ $.FmtAlias }}.Errorf("registry toolset %q is required", {{ printf "%q" .QualifiedName }})
    }
    if toolsets.{{ .FieldName }}.Name() != {{ printf "%q" .ToolsetName }} {
        return {{ $.FmtAlias }}.Errorf("registry toolset %q requires %q, got %q", {{ printf "%q" .QualifiedName }}, {{ printf "%q" .ToolsetName }}, toolsets.{{ .FieldName }}.Name())
    }
    {{- if .Version }}
    if toolsets.{{ .FieldName }}.Version() != {{ printf "%q" .Version }} {
        return {{ $.FmtAlias }}.Errorf("registry toolset %q requires version %q, got %q", {{ printf "%q" .QualifiedName }}, {{ printf "%q" .Version }}, toolsets.{{ .FieldName }}.Version())
    }
    {{- end }}
    for _, name := range toolsets.{{ .FieldName }}.Names() {
        if owner, exists := names[name]; exists {
            return {{ $.FmtAlias }}.Errorf("registry toolset %q repeats tool %q already supplied by %s", {{ printf "%q" .QualifiedName }}, name, owner)
        }
        names[name] = {{ printf "%q" .QualifiedName }}
    }
{{- end }}
    return nil
}

// {{ .PackageNames.Definition }} combines the generated contracts with the
// supplied registry toolsets and returns an immutable agent definition.
func {{ .PackageNames.Definition }}(toolsets {{ .RegistryToolsetsType }}) ({{ .RuntimeAlias }}.AgentDefinition, error) {
    if err := toolsets.Validate(); err != nil {
        return {{ .RuntimeAlias }}.AgentDefinition{}, err
    }
    return {{ .RuntimeAlias }}.NewAgentDefinition(
{{- else }}
var {{ .PackageNames.DefinitionValue }} = {{ .RuntimeAlias }}.NewAgentDefinition(
{{- end }}
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
        ),
{{- end }}
    },
{{- else }}
    nil,
{{- end }}
{{- if .RegistryBindings }}
    ), nil
}
{{- else }}
)

// {{ .PackageNames.Definition }} returns the immutable generated contract shared by callers and workers.
func {{ .PackageNames.Definition }}() {{ .RuntimeAlias }}.AgentDefinition {
	return {{ .PackageNames.DefinitionValue }}
}
{{- end }}

// {{ .PackageNames.NewClient }} returns a runtime.AgentClient bound to this agent. In caller
// processes that do not register the agent locally, it still validates starts
// against the same generated contract as the worker.
{{- if .RegistryBindings }}
func {{ .PackageNames.NewClient }}(rt *{{ .RuntimeAlias }}.Runtime, toolsets {{ .RegistryToolsetsType }}) ({{ .RuntimeAlias }}.AgentClient, error) {
    definition, err := {{ .PackageNames.Definition }}(toolsets)
    if err != nil {
        return nil, err
    }
    return rt.ClientFor(definition)
}
{{- else }}
func {{ .PackageNames.NewClient }}(rt *{{ .RuntimeAlias }}.Runtime) {{ .RuntimeAlias }}.AgentClient {
    return rt.MustClientFor({{ .PackageNames.Definition }}())
}
{{- end }}
