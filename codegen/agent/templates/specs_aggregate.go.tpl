var (
    // Specs is the static list of tool specs exported by this agent.
    Specs = []tools.ToolSpec{
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- range .Tools }}
        {{ $pkg }}.{{ .Spec.SpecVar }},
        {{- end }}
    {{- end }}
    }

    // metadata is the static list of policy metadata exported by this agent.
    metadata = []policy.ToolMetadata{
    {{- range .Toolsets }}
        {{- range .Tools }}
        {
            ID:          tools.Ident({{ printf "%q" .Tool.QualifiedName }}),
            Title:       {{ printf "%q" .Tool.Title }},
            Description: {{ printf "%q" .Tool.Description }},
            Tags: []string{
            {{- range .Tool.Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
            BudgetClass: policy.ToolBudgetClass{{ if .Tool.Bookkeeping }}Bookkeeping{{ else }}Budgeted{{ end }},
        },
        {{- end }}
    {{- end }}
    }

    // names is the static list of exported tool identifiers.
    names = []tools.Ident{
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- range .Tools }}
        {{ $pkg }}.{{ .Spec.ConstName }},
        {{- end }}
    {{- end }}
    }

    // RequiredLabels lists, sorted and deduplicated, the run label keys that
    // label-backed Inject() fields require across every toolset this agent
    // uses. Runtime.Start/OneShotRun rejects a run missing any of these keys
    // before scheduling any workflow or activity.
    RequiredLabels = []string{
    {{- range .RequiredLabels }}
        {{ printf "%q" . }},
    {{- end }}
    }
)

// Names returns the tool identifiers exported by this agent.
func Names() []tools.Ident {
    return names
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (*tools.ToolSpec, bool) {
    switch name {
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- range .Tools }}
    case tools.Ident({{ printf "%q" .Tool.QualifiedName }}):
        return &{{ $pkg }}.{{ .Spec.SpecVar }}, true
        {{- end }}
    {{- end }}
    default:
        return nil, false
    }
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name tools.Ident) ([]byte, bool) {
    switch name {
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- range .Tools }}
    case tools.Ident({{ printf "%q" .Tool.QualifiedName }}):
        return {{ $pkg }}.{{ .Spec.SpecVar }}.Payload.Schema, true
        {{- end }}
    {{- end }}
    default:
        return nil, false
    }
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name tools.Ident) ([]byte, bool) {
    switch name {
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- range .Tools }}
    case tools.Ident({{ printf "%q" .Tool.QualifiedName }}):
        return {{ $pkg }}.{{ .Spec.SpecVar }}.Result.Schema, true
        {{- end }}
    {{- end }}
    default:
        return nil, false
    }
}

// AdvertisedSpecs returns the full list of tool specs to advertise to the model.
func AdvertisedSpecs() []tools.ToolSpec {
    return Specs
}

// Metadata exposes policy metadata for the aggregated tools.
func Metadata() []policy.ToolMetadata {
    return metadata
}

// MetadataByName returns policy metadata for the named tool if present.
func MetadataByName(name tools.Ident) (policy.ToolMetadata, bool) {
    switch name {
    {{- range .Toolsets }}
        {{- range .Tools }}
    case tools.Ident({{ printf "%q" .Tool.QualifiedName }}):
        return policy.ToolMetadata{
            ID:          tools.Ident({{ printf "%q" .Tool.QualifiedName }}),
            Title:       {{ printf "%q" .Tool.Title }},
            Description: {{ printf "%q" .Tool.Description }},
            Tags: []string{
            {{- range .Tool.Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
            BudgetClass: policy.ToolBudgetClass{{ if .Tool.Bookkeeping }}Bookkeeping{{ else }}Budgeted{{ end }},
        }, true
        {{- end }}
    {{- end }}
    default:
        return policy.ToolMetadata{}, false
    }
}

