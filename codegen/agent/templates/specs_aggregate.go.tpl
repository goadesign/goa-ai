// Specs returns fresh specifications for every tool exported by this agent.
func Specs() []tools.ToolSpec {
    specs := make([]tools.ToolSpec, 0)
{{- range .Toolsets }}
    specs = append(specs, {{ .SpecsPackageName }}.Specs()...)
{{- end }}
    return specs
}

// Names returns fresh tool identifiers exported by this agent.
func Names() []tools.Ident {
    return []tools.Ident{
{{- range .Toolsets }}
    {{- $pkg := .SpecsPackageName }}
    {{- range .Tools }}
        {{ $pkg }}.{{ .ConstName }},
    {{- end }}
{{- end }}
    }
}

// RequiredLabels returns the sorted, deduplicated run label keys required by
// label-backed Inject fields across every toolset this agent uses.
func RequiredLabels() []string {
    return []string{
{{- range .RequiredLabels }}
        {{ printf "%q" . }},
{{- end }}
    }
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (tools.ToolSpec, bool) {
    switch name {
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- range .Tools }}
    case tools.Ident({{ printf "%q" .QualifiedName }}):
        return {{ $pkg }}.Spec({{ $pkg }}.{{ .ConstName }})
        {{- end }}
    {{- end }}
    default:
        return tools.ToolSpec{}, false
    }
}

// Metadata returns fresh policy metadata for the aggregated tools.
func Metadata() []policy.ToolMetadata {
    return []policy.ToolMetadata{
    {{- range .Toolsets }}
        {{- range .Tools }}
        {
            ID:          tools.Ident({{ printf "%q" .QualifiedName }}),
            Title:       {{ printf "%q" .Title }},
            Description: {{ printf "%q" .Description }},
            Tags: []string{
            {{- range .Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
            BudgetClass: policy.ToolBudgetClass{{ if .Bookkeeping }}Bookkeeping{{ else }}Budgeted{{ end }},
        },
        {{- end }}
    {{- end }}
    }
}

// MetadataByName returns policy metadata for the named tool if present.
func MetadataByName(name tools.Ident) (policy.ToolMetadata, bool) {
    switch name {
    {{- range .Toolsets }}
        {{- range .Tools }}
    case tools.Ident({{ printf "%q" .QualifiedName }}):
        return policy.ToolMetadata{
            ID:          tools.Ident({{ printf "%q" .QualifiedName }}),
            Title:       {{ printf "%q" .Title }},
            Description: {{ printf "%q" .Description }},
            Tags: []string{
            {{- range .Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
            BudgetClass: policy.ToolBudgetClass{{ if .Bookkeeping }}Bookkeeping{{ else }}Budgeted{{ end }},
        }, true
        {{- end }}
    {{- end }}
    default:
        return policy.ToolMetadata{}, false
    }
}
