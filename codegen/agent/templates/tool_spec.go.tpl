// Tool IDs for this toolset.
const (
{{- range .Tools }}
    {{- if .Payload }}
    {{ trimSuffix .Payload.TypeName "Payload" }} tools.Ident = {{ printf "%q" .Name }}
    {{- else if .Result }}
    {{ trimSuffix .Result.TypeName "Result" }} tools.Ident = {{ printf "%q" .Name }}
    {{- else }}
    {{ goify .Name true }} tools.Ident = {{ printf "%q" .Name }}
    {{- end }}
{{- end }}
)

var Specs = []tools.ToolSpec{
{{- range .Tools }}
    {
        Name:        {{ if .Payload }}{{ trimSuffix .Payload.TypeName "Payload" }}{{ else if .Result }}{{ trimSuffix .Result.TypeName "Result" }}{{ else }}{{ goify .Name true }}{{ end }},
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
        BoundedResult: {{ if .BoundedResult }}true{{ else }}false{{ end }},
        Payload: tools.TypeSpec{
            Name: {{ if .Payload }}{{ printf "%q" .Payload.TypeName }}{{ else }}""{{ end }},
            {{- if .Payload }}
            Schema: {{- if gt (len .Payload.SchemaJSON) 0 }}[]byte({{ printf "%q" .Payload.SchemaJSON }}){{ else }}nil{{ end }},
            Codec:  {{ .Payload.GenericCodec }},
            {{- else }}
            Schema: nil,
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
        Result: tools.TypeSpec{
            Name: {{ if .Result }}{{ printf "%q" .Result.TypeName }}{{ else }}""{{ end }},
            Schema: {{- if and .Result (gt (len .Result.SchemaJSON) 0) }}[]byte({{ printf "%q" .Result.SchemaJSON }}){{ else }}nil{{ end }},
            {{- if .Result }}
            Codec:  {{ .Result.GenericCodec }},
            {{- else }}
            Codec:  tools.JSONCodec[any]{},
            {{- end }}
        },
        Sidecar: {{- if .Sidecar }} &tools.TypeSpec{
            Name: {{ printf "%q" .Sidecar.TypeName }},
            Schema: {{- if gt (len .Sidecar.SchemaJSON) 0 }}[]byte({{ printf "%q" .Sidecar.SchemaJSON }}){{ else }}nil{{ end }},
            Codec:  {{ .Sidecar.GenericCodec }},
        },{{ else }}nil,{{ end }}
    },
{{- end }}
}

var (
    specIndex = make(map[tools.Ident]*tools.ToolSpec, len(Specs))
    metadata   = []policy.ToolMetadata{
    {{- range .Tools }}
        {
            ID:          {{ if .Payload }}{{ trimSuffix .Payload.TypeName "Payload" }}{{ else if .Result }}{{ trimSuffix .Result.TypeName "Result" }}{{ else }}{{ goify .Name true }}{{ end }},
            Title:       {{ printf "%q" .Title }},
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
func Names() []tools.Ident {
    names := make([]tools.Ident, 0, len(specIndex))
    for name := range specIndex {
        names = append(names, name)
    }
    sort.Slice(names, func(i, j int) bool { return string(names[i]) < string(names[j]) })
    return names
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (*tools.ToolSpec, bool) {
    spec, ok := specIndex[name]
    return spec, ok
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name tools.Ident) ([]byte, bool) {
    spec, ok := specIndex[name]
    if !ok {
        return nil, false
    }
    return spec.Payload.Schema, true
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name tools.Ident) ([]byte, bool) {
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


