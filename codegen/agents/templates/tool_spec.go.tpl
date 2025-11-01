var Specs = []tools.ToolSpec{
{{- range .Tools }}
    {
        Name:        {{ printf "%q" .Name }},
        Service:     {{ printf "%q" .Service }},
        Toolset:     {{ printf "%q" .Toolset }},
        Description: {{ printf "%q" .Description }},
        Tags: []string{
        {{- range .Tags }}
            {{ printf "%q" . }},
        {{- end }}
        },
        {{- if .IsExportedByAgent }}
        IsAgentTool: true,
        AgentID:     {{ printf "%q" .ExportingAgentID }},
        {{- end }}
        Payload: tools.TypeSpec{
            Name:   {{ if .Payload }}{{ printf "%q" .Payload.TypeName }}{{ else }}""{{ end }},
            {{- if and .Payload .Payload.SchemaVar }}
            Schema: {{ .Payload.SchemaVar }},
            {{- else }}
            Schema: nil,
            {{- end }}
            {{- if .Payload }}
            Codec:  {{ .Payload.GenericCodec }},
            {{- else }}
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
        Result: tools.TypeSpec{
            Name:   {{ if .Result }}{{ printf "%q" .Result.TypeName }}{{ else }}""{{ end }},
            {{- if and .Result .Result.SchemaVar }}
            Schema: {{ .Result.SchemaVar }},
            {{- else }}
            Schema: nil,
            {{- end }}
            {{- if .Result }}
            Codec:  {{ .Result.GenericCodec }},
            {{- else }}
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
    },
{{- end }}
}

{{- range .Types }}
{{- if .SchemaVar }}
var {{ .SchemaVar }} = {{ .SchemaLiteral }}
{{- end }}
{{- end }}

var (
    specIndex = make(map[string]*tools.ToolSpec, len(Specs))
    metadata   = []policy.ToolMetadata{
    {{- range .Tools }}
        {
            ID:          {{ printf "%q" .Name }},
            Name:        {{ printf "%q" .DisplayName }},
            Description: {{ printf "%q" .Description }},
            Tags: []string{
            {{- range .Tags }}
                {{ printf "%q" . }},
            {{- end }}
            },
        },
    {{- end }}
    }
)

func init() {
    for i := range Specs {
        spec := &Specs[i]
        specIndex[spec.Name] = spec
    }
}

// Names returns the identifiers of all generated tools.
func Names() []string {
    names := make([]string, 0, len(specIndex))
    for name := range specIndex {
        names = append(names, name)
    }
    sort.Strings(names)
    return names
}

// Spec returns the specification for the named tool if present.
func Spec(name string) (*tools.ToolSpec, bool) {
    spec, ok := specIndex[name]
    return spec, ok
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name string) ([]byte, bool) {
    spec, ok := specIndex[name]
    if !ok {
        return nil, false
    }
    return spec.Payload.Schema, true
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name string) ([]byte, bool) {
    spec, ok := specIndex[name]
    if !ok {
        return nil, false
    }
    return spec.Result.Schema, true
}

// Metadata exposes policy metadata for the generated tools.
func Metadata() []policy.ToolMetadata {
    out := make([]policy.ToolMetadata, len(metadata))
    copy(out, metadata)
    return out
}
