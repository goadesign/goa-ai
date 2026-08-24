// Completion IDs for this service.
const (
{{- range .Completions }}
    {{ .ConstName }} completion.Ident = {{ printf "%q" .Name }}
{{- end }}
)

var (
{{- range .Completions }}
    {{ .SpecVar }} = completion.Spec[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}]{
        Name:        {{ .ConstName }},
        Description: {{ printf "%q" .Description }},
        Result: tools.TypeSpec{
            Name: {{ if .Result }}{{ printf "%q" .Result.TypeName }}{{ else }}""{{ end }},
            Schema: {{- if and .Result (gt (len .Result.SchemaJSON) 0) }}tools.RawJSON({{ printf "%q" .Result.SchemaJSON }}){{ else }}nil{{ end }},
            {{- if .Result }}
            SchemaWithoutRootExample: {{- if gt (len .Result.SchemaWithoutRootExampleJSON) 0 }}tools.RawJSON({{ printf "%q" .Result.SchemaWithoutRootExampleJSON }}){{ else }}nil{{ end }},
            ExampleJSON: {{- if gt (len .Result.ExampleJSON) 0 }}tools.RawJSON({{ printf "%q" .Result.ExampleJSON }}){{ else }}nil{{ end }},
            FieldDescriptions: {{- if .Result.FieldDescs }}{{ .Result.TypeName }}FieldDescs{{ else }}nil{{ end }},
            FieldJSONTypes: {{- if .Result.FieldJSONTypes }}{{ .Result.TypeName }}FieldJSONTypes{{ else }}nil{{ end }},
            Codec: {{ .Result.GenericCodec }},
            {{- else }}
            SchemaWithoutRootExample: nil,
            ExampleJSON: nil,
            FieldDescriptions: nil,
            FieldJSONTypes: nil,
            Codec: tools.JSONCodec[any]{},
            {{- end }}
        },
        Codec: {{ .Result.ExportedCodec }},
    }
{{- end }}
)

{{- range .Completions }}
// {{ .DecodeFunc }} decodes the structured assistant response for {{ .Name }}.
func {{ .DecodeFunc }}(resp *model.Response) ({{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}, error) {
    return completion.DecodeResponse(resp, {{ .SpecVar }})
}

// {{ .DecodeChunk }} decodes the final structured completion chunk for {{ .Name }}.
func {{ .DecodeChunk }}(chunk model.Chunk) ({{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}, bool, error) {
    return completion.DecodeChunk(chunk, {{ .SpecVar }})
}

// {{ .Complete }} runs the typed completion for {{ .Name }}.
func {{ .Complete }}(ctx context.Context, client model.Client, req *model.Request) (*completion.Response[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}], error) {
    return completion.Complete(ctx, client, req, {{ .SpecVar }})
}

// {{ .Stream }} starts the typed completion stream for {{ .Name }}.
func {{ .Stream }}(ctx context.Context, client model.Client, req *model.Request) (model.Streamer, error) {
    return completion.Stream(ctx, client, req, {{ .SpecVar }})
}
{{- end }}
