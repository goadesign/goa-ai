// This file provides Go types and request builders for {{ .Toolset.Name }} tools.

// Use these tool names when creating requests.
const (
{{- range .Tools }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
{{- end }}
)

// These aliases expose each tool's generated input and result types.
{{- range .Tools }}
type {{ .PayloadAlias }} = {{ $.SpecsAlias }}.{{ .Payload.TypeName }}
{{- if .Result }}
type {{ .ResultAlias }} = {{ $.SpecsAlias }}.{{ .Result.TypeName }}
{{- end }}
{{- end }}

{{- range .Tools }}
// {{ .CallFunc }} builds a request for {{ .Name }}.
// The runtime assigns the request ID.
func {{ .CallFunc }}(args {{ if .Payload.Pointer }}*{{ end }}{{ .PayloadAlias }}) (planner.ToolRequest, error) {
    return planner.NewToolRequest({{ $.SpecsAlias }}.{{ .TypedToolVar }}(), args)
}
{{- end }}
