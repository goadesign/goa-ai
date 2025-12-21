var (
    // Specs is the static list of tool specs exported by this agent.
    Specs = []tools.ToolSpec{
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- range .Tools }}
        {{ $pkg }}.Spec{{ .ConstName }},
        {{- end }}
    {{- end }}
    }

    // metadata is the static list of policy metadata exported by this agent.
    metadata = []policy.ToolMetadata{
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
        },
        {{- end }}
    {{- end }}
    }

    // names is the static list of exported tool identifiers.
    names = []tools.Ident{
    {{- range .Toolsets }}
        {{- $pkg := .SpecsPackageName }}
        {{- range .Tools }}
        {{ $pkg }}.{{ .ConstName }},
        {{- end }}
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
    case tools.Ident({{ printf "%q" .QualifiedName }}):
        return &{{ $pkg }}.Spec{{ .ConstName }}, true
        {{- end }}
    {{- end }}
    default:
        return nil, false
    }
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name tools.Ident) ([]byte, bool) {
    if s, ok := Spec(name); ok {
        return s.Payload.Schema, true
    }
    return nil, false
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name tools.Ident) ([]byte, bool) {
    if s, ok := Spec(name); ok {
        return s.Result.Schema, true
    }
    return nil, false
}

// AdvertisedSpecs returns the full list of tool specs to advertise to the model.
func AdvertisedSpecs() []tools.ToolSpec {
    return Specs
}

// Metadata exposes policy metadata for the aggregated tools.
func Metadata() []policy.ToolMetadata {
    return metadata
}
