// {{ .SpecsFunc }} returns fresh specifications for every tool available to this agent.
func {{ .SpecsFunc }}() []{{ .ToolsPackageName }}.ToolSpec {
    specs := make([]{{ .ToolsPackageName }}.ToolSpec, 0)
{{- range .Toolsets }}
    {{- if .AgentID }}
    {{- $pkg := .SpecsPackageName }}
    {{- $agentID := .AgentID }}
    {{- range .Tools }}
    {
        spec := {{ $pkg }}.{{ .Spec.SpecVar }}()
        spec.IsAgentTool = true
        spec.AgentID = {{ printf "%q" $agentID }}
        specs = append(specs, spec)
    }
    {{- end }}
    {{- else }}
    specs = append(specs, {{ .SpecsPackageName }}.Specs()...)
    {{- end }}
{{- end }}
    return specs
}

// {{ .NamesFunc }} returns the identifiers for every tool available to this agent.
func {{ .NamesFunc }}() []{{ .ToolsPackageName }}.Ident {
    return []{{ .ToolsPackageName }}.Ident{
{{- range .Toolsets }}
    {{- $pkg := .SpecsPackageName }}
    {{- range .Tools }}
        {{ $pkg }}.{{ .Spec.ConstName }},
    {{- end }}
{{- end }}
    }
}

// {{ .RequiredLabelsFunc }} returns the sorted run labels required by this
// agent's generated tool payloads. Each label appears once.
func {{ .RequiredLabelsFunc }}() []string {
    return []string{
{{- range .RequiredLabels }}
        {{ printf "%q" . }},
{{- end }}
    }
}

// {{ .SpecFunc }} returns the specification for the named tool if present.
func {{ .SpecFunc }}(name {{ .ToolsPackageName }}.Ident) ({{ .ToolsPackageName }}.ToolSpec, bool) {
    switch name {
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- $agentID := .AgentID }}
        {{- range .Tools }}
    case {{ $.ToolsPackageName }}.Ident({{ printf "%q" .Tool.QualifiedName }}):
        {{- if $agentID }}
        spec := {{ $pkg }}.{{ .Spec.SpecVar }}()
        spec.IsAgentTool = true
        spec.AgentID = {{ printf "%q" $agentID }}
        return spec, true
        {{- else }}
        return {{ $pkg }}.Spec({{ $pkg }}.{{ .Spec.ConstName }})
        {{- end }}
        {{- end }}
    {{- end }}
    default:
        return {{ .ToolsPackageName }}.ToolSpec{}, false
    }
}

// {{ .MetadataFunc }} returns policy details for every available tool.
func {{ .MetadataFunc }}() []{{ .PolicyPackageName }}.ToolMetadata {
    return []{{ .PolicyPackageName }}.ToolMetadata{
    {{- range .Toolsets }}
        {{- range .Tools }}
        {
            ID:          {{ $.ToolsPackageName }}.Ident({{ printf "%q" .Tool.QualifiedName }}),
            Title:       {{ printf "%q" .Tool.Title }},
            Description: {{ printf "%q" .Tool.Description }},
            Tags: []string{
            {{- range .Tool.Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
            BudgetClass: {{ $.PolicyPackageName }}.ToolBudgetClass{{ if .Tool.Bookkeeping }}Bookkeeping{{ else }}Budgeted{{ end }},
        },
        {{- end }}
    {{- end }}
    }
}

// {{ .MetadataByNameFunc }} returns policy metadata for the named tool if present.
func {{ .MetadataByNameFunc }}(name {{ .ToolsPackageName }}.Ident) ({{ .PolicyPackageName }}.ToolMetadata, bool) {
    switch name {
    {{- range .Toolsets }}
        {{- range .Tools }}
    case {{ $.ToolsPackageName }}.Ident({{ printf "%q" .Tool.QualifiedName }}):
        return {{ $.PolicyPackageName }}.ToolMetadata{
            ID:          {{ $.ToolsPackageName }}.Ident({{ printf "%q" .Tool.QualifiedName }}),
            Title:       {{ printf "%q" .Tool.Title }},
            Description: {{ printf "%q" .Tool.Description }},
            Tags: []string{
            {{- range .Tool.Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
            BudgetClass: {{ $.PolicyPackageName }}.ToolBudgetClass{{ if .Tool.Bookkeeping }}Bookkeeping{{ else }}Budgeted{{ end }},
        }, true
        {{- end }}
    {{- end }}
    default:
        return {{ .PolicyPackageName }}.ToolMetadata{}, false
    }
}
