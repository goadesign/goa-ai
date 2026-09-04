// Completion IDs for this service.
const (
{{- range .Completions }}
    {{ .ConstName }} completion.Ident = {{ printf "%q" .Name }}
{{- end }}
)

{{- range .Completions }}
// {{ .SpecFunc }} returns a fresh typed completion contract for {{ .Name }}.
func {{ .SpecFunc }}() completion.Spec[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}] {
    return completion.Spec[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}]{
        Name:        {{ .ConstName }},
        Description: {{ printf "%q" .Description }},
        Schema: {{- if and .Result (gt (len .Result.SchemaJSON) 0) }}rawjson.Message({{ printf "%q" .Result.SchemaJSON }}){{ else }}nil{{ end }},
        SchemaWithoutRootExample: {{- if and .Result (gt (len .Result.SchemaWithoutRootExampleJSON) 0) }}rawjson.Message({{ printf "%q" .Result.SchemaWithoutRootExampleJSON }}){{ else }}nil{{ end }},
        ExampleJSON: {{- if and .Result (gt (len .Result.ExampleJSON) 0) }}rawjson.Message({{ printf "%q" .Result.ExampleJSON }}){{ else }}nil{{ end }},
		Fields: {{- if .Result.Fields }}tools.CloneFieldMetadata({{ .Result.FieldsVar }}){{ else }}nil{{ end }},
        Codec: {{ .Result.ExportedCodec }}(),
    }
}

// {{ .Example }} returns an immutable copy of the generated example
// used to demonstrate {{ .Name }} output.
func {{ .Example }}() rawjson.Message {
    return slices.Clone({{ .SpecFunc }}().ExampleJSON)
}
{{- end }}

{{- range .Completions }}
// {{ .Complete }} runs the unary typed completion for {{ .Name }}.
func {{ .Complete }}(ctx context.Context, client model.Client, req *model.Request) (*completion.Response[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}], error) {
    return completion.Complete(ctx, client, req, {{ .SpecFunc }}())
}

// {{ .Stream }} starts the typed completion stream for {{ .Name }}.
func {{ .Stream }}(ctx context.Context, client model.Client, req *model.Request) (*completion.Streamer[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}], error) {
    return completion.Stream(ctx, client, req, {{ .SpecFunc }}())
}
{{- end }}
