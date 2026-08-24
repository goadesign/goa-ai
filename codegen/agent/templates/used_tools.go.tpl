// Used toolset typed helpers for {{ .Toolset.Name }}
// These helpers mirror the agent-as-tool helpers to provide a consistent planner UX.
// They expose typed payload/result aliases and `New<Tool>Call` builders.

// Tool IDs (globally unique). Use these constants in planner tool calls.
const (
{{- range .Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
{{- end }}
)

// Type aliases preserve exact tool payload and result identities.
{{- range .Tools }}
type {{ .GoName }}Payload = {{ $.Toolset.SpecsPackageName }}specs.{{ .Payload.TypeName }}
{{- if .Result }}
type {{ .GoName }}Result  = {{ $.Toolset.SpecsPackageName }}specs.{{ .Result.TypeName }}
{{- end }}
{{- end }}

// Typed tool-call helpers (one per tool). These ensure use of the generated tool ID
// and accept typed payloads matching tool schemas.
{{- range .Tools }}
// New{{ .GoName }}Call builds a planner-authored request for {{ .Name }}.
// The runtime assigns its execution ID.
func New{{ .GoName }}Call(args *{{ .GoName }}Payload) (planner.ToolRequest, error) {
    return planner.NewToolRequest({{ $.Toolset.SpecsPackageName }}specs.{{ .TypedToolVar }}(), args)
}
{{- end }}

