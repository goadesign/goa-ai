// Tool IDs for this toolset.
const (
{{- range .Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
{{- end }}
)

var Specs = []tools.ToolSpec{
{{- range .Tools }}
    Spec{{ .ConstName }},
{{- end }}
}

var (
{{- range .Tools }}
    Spec{{ .ConstName }} = tools.ToolSpec{
        Name:        {{ .ConstName }},
        Service:     {{ printf "%q" .Service }},
        Toolset:     {{ printf "%q" .Toolset }},
        Description: {{ printf "%q" .Description }},
        ArtifactDescription: {{ printf "%q" .ArtifactDescription }},
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
        {{- if .ResultReminder }}
        ResultReminder: {{ printf "%q" .ResultReminder }},
        {{- end }}
        {{- if .Confirmation }}
        Confirmation: &tools.ConfirmationSpec{
            Title: {{ printf "%q" .Confirmation.Title }},
            PromptTemplate: {{ printf "%q" .Confirmation.PromptTemplate }},
            DeniedResultTemplate: {{ printf "%q" .Confirmation.DeniedResultTemplate }},
        },
        {{- end }}
        Payload: tools.TypeSpec{
            Name: {{ if .Payload }}{{ printf "%q" .Payload.TypeName }}{{ else }}""{{ end }},
            {{- if .Payload }}
            Schema: {{- if gt (len .Payload.SchemaJSON) 0 }}[]byte({{ printf "%q" .Payload.SchemaJSON }}){{ else }}nil{{ end }},
            ExampleJSON: {{- if gt (len .Payload.ExampleJSON) 0 }}[]byte({{ printf "%q" .Payload.ExampleJSON }}){{ else }}nil{{ end }},
            ExampleInput: {{- if .Payload.ExampleInputGo }}{{ .Payload.ExampleInputGo }}{{ else }}nil{{ end }},
            Codec:  {{ .Payload.GenericCodec }},
            {{- else }}
            Schema: nil,
            ExampleJSON: nil,
            ExampleInput: nil,
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
    }
{{- end }}
)

var (
    metadata   = []policy.ToolMetadata{
    {{- range .Tools }}
        {
            ID:          {{ .ConstName }},
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
    names = []tools.Ident{
    {{- range .Tools }}
        {{ .ConstName }},
    {{- end }}
    }
)

// Names returns the identifiers of all generated tools.
func Names() []tools.Ident {
    return names
}

// Spec returns the specification for the named tool if present.
func Spec(name tools.Ident) (*tools.ToolSpec, bool) {
    switch name {
    {{- range .Tools }}
    case {{ .ConstName }}:
        return &Spec{{ .ConstName }}, true
    {{- end }}
    default:
        return nil, false
    }
}

// PayloadSchema returns the JSON schema for the named tool payload.
func PayloadSchema(name tools.Ident) ([]byte, bool) {
    switch name {
    {{- range .Tools }}
    case {{ .ConstName }}:
        return Spec{{ .ConstName }}.Payload.Schema, true
    {{- end }}
    default:
        return nil, false
    }
}

// ResultSchema returns the JSON schema for the named tool result.
func ResultSchema(name tools.Ident) ([]byte, bool) {
    switch name {
    {{- range .Tools }}
    case {{ .ConstName }}:
        return Spec{{ .ConstName }}.Result.Schema, true
    {{- end }}
    default:
        return nil, false
    }
}

// Metadata exposes policy metadata for the generated tools.
func Metadata() []policy.ToolMetadata {
    return metadata
}


