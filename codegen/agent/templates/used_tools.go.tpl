// Used toolset typed helpers for {{ .Toolset.Name }}
// These helpers mirror the agent-as-tool helpers to provide a consistent planner UX.
// They expose typed payload/result aliases and `New<Tool>Call` builders.

// Tool IDs (globally unique). Use these constants in planner tool calls.
const (
{{- range .Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
{{- end }}
)

// Type aliases and codec re-exports for convenience.
{{- range .Tools }}
type {{ .GoName }}Payload = {{ $.Toolset.SpecsPackageName }}specs.{{ .Payload.TypeName }}
var {{ .GoName }}PayloadCodec = {{ $.Toolset.SpecsPackageName }}specs.{{ .Payload.ExportedCodec }}
{{- if .Result }}
type {{ .GoName }}Result  = {{ $.Toolset.SpecsPackageName }}specs.{{ .Result.TypeName }}
var {{ .GoName }}ResultCodec  = {{ $.Toolset.SpecsPackageName }}specs.{{ .Result.ExportedCodec }}
{{- end }}
{{- end }}

// Typed tool-call helpers (one per tool). These ensure use of the generated tool ID
// and accept typed payloads matching tool schemas.
{{- range .Tools }}
// New{{ .GoName }}Call builds a planner.ToolRequest for {{ .Name }}.
// toolCallID must be nonempty and unique within the containing planner.PlanResult.
func New{{ .GoName }}Call(toolCallID string, args *{{ .GoName }}Payload) planner.ToolRequest {
    if toolCallID == "" {
        panic("{{ .Name }} tool call ID is required")
    }
    var payload []byte
    if args != nil {
        // Encode typed payloads into canonical JSON using the generated codec.
        b, err := {{ .GoName }}PayloadCodec.ToJSON(args)
        if err != nil {
            panic(err)
        }
        payload = b
    }
    req := planner.ToolRequest{
        Name:       {{ .ConstName }},
        Payload:    payload,
        ToolCallID: toolCallID,
    }
    return req
}
{{- end }}


