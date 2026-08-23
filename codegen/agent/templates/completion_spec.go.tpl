// Completion IDs for this service.
const (
{{- range .Completions }}
    {{ .ConstName }} completion.Ident = {{ printf "%q" .Name }}
{{- end }}
)

{{- range .Completions }}
// spec{{ .ConstName }} returns a fresh typed completion contract for {{ .Name }}.
func spec{{ .ConstName }}() completion.Spec[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}] {
    return completion.Spec[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}]{
        Name:        {{ .ConstName }},
        Description: {{ printf "%q" .Description }},
        Schema: {{- if and .Result (gt (len .Result.SchemaJSON) 0) }}rawjson.Message({{ printf "%q" .Result.SchemaJSON }}){{ else }}nil{{ end }},
        SchemaWithoutRootExample: {{- if and .Result (gt (len .Result.SchemaWithoutRootExampleJSON) 0) }}rawjson.Message({{ printf "%q" .Result.SchemaWithoutRootExampleJSON }}){{ else }}nil{{ end }},
        ExampleJSON: {{- if and .Result (gt (len .Result.ExampleJSON) 0) }}rawjson.Message({{ printf "%q" .Result.ExampleJSON }}){{ else }}nil{{ end }},
        Codec: {{ .Result.ExportedCodec }},
    }
}

// {{ .ConstName }}Example returns an immutable copy of the generated example
// used to demonstrate {{ .Name }} output.
func {{ .ConstName }}Example() rawjson.Message {
    return slices.Clone(spec{{ .ConstName }}().ExampleJSON)
}
{{- end }}

{{- range .Completions }}
// Complete{{ .ConstName }} runs the unary typed completion for {{ .Name }}.
func Complete{{ .ConstName }}(ctx context.Context, client model.Client, req *model.Request) (*completion.Response[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}], error) {
    return completion.Complete(ctx, client, req, spec{{ .ConstName }}())
}

// StreamComplete{{ .ConstName }} starts the typed completion stream for {{ .Name }}.
func StreamComplete{{ .ConstName }}(ctx context.Context, client model.Client, req *model.Request) (*completion.Streamer[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}], error) {
    return completion.Stream(ctx, client, req, spec{{ .ConstName }}())
}
{{- end }}
