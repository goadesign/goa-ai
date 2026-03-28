// Completion IDs for this service.
const (
{{- range .Completions }}
    {{ .ConstName }} completion.Ident = {{ printf "%q" .Name }}
{{- end }}
)

var (
{{- range .Completions }}
    Spec{{ .ConstName }} = completion.Spec[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}]{
        Name:        {{ .ConstName }},
        Description: {{ printf "%q" .Description }},
        Result: tools.TypeSpec{
            Name: {{ if .Result }}{{ printf "%q" .Result.TypeName }}{{ else }}""{{ end }},
            Schema: {{- if and .Result (gt (len .Result.SchemaJSON) 0) }}[]byte({{ printf "%q" .Result.SchemaJSON }}){{ else }}nil{{ end }},
            {{- if .Result }}
            ExampleJSON: {{- if gt (len .Result.ExampleJSON) 0 }}[]byte({{ printf "%q" .Result.ExampleJSON }}){{ else }}nil{{ end }},
            ExampleInput: {{- if .Result.ExampleInputGo }}{{ .Result.ExampleInputGo }}{{ else }}nil{{ end }},
            Codec: {{ .Result.GenericCodec }},
            {{- else }}
            ExampleJSON: nil,
            ExampleInput: nil,
            Codec: tools.JSONCodec[any]{},
            {{- end }}
        },
        Codec: {{ .Result.ExportedCodec }},
    }
{{- end }}
)

{{- range .Completions }}
// Decode{{ .ConstName }} decodes the structured assistant response for {{ .Name }}.
func Decode{{ .ConstName }}(resp *model.Response) ({{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}, error) {
    return completion.DecodeResponse(resp, Spec{{ .ConstName }})
}

// Decode{{ .ConstName }}Chunk decodes the final structured completion chunk for {{ .Name }}.
func Decode{{ .ConstName }}Chunk(chunk model.Chunk) ({{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}, bool, error) {
    return completion.DecodeChunk(chunk, Spec{{ .ConstName }})
}

// Complete{{ .ConstName }} runs the unary typed completion for {{ .Name }}.
func Complete{{ .ConstName }}(ctx context.Context, client model.Client, req *model.Request) (*completion.Response[{{ if .Result.Pointer }}*{{ end }}{{ .Result.FullRef }}], error) {
    return completion.Complete(ctx, client, req, Spec{{ .ConstName }})
}

// StreamComplete{{ .ConstName }} starts the typed completion stream for {{ .Name }}.
func StreamComplete{{ .ConstName }}(ctx context.Context, client model.Client, req *model.Request) (model.Streamer, error) {
    return completion.Stream(ctx, client, req, Spec{{ .ConstName }})
}
{{- end }}
