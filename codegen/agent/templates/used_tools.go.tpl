// This file provides Go types and request builders for {{ .Toolset.Name }} tools.

// Use these tool names when creating requests.
const (
{{- range .Tools }}
{{- if not .PrivatePage }}
    {{ .ConstName }} tools.Ident = {{ printf "%q" .Name }}
{{- end }}
{{- end }}
)

// These aliases expose each tool's generated input and result types.
{{- range .Tools }}
{{- if not .PrivatePage }}
type {{ .PayloadAlias }} = {{ $.SpecsAlias }}.{{ .Payload.TypeName }}
{{- if .Result }}
type {{ .ResultAlias }} = {{ $.SpecsAlias }}.{{ .Result.TypeName }}
{{- end }}
{{- end }}
{{- end }}

{{- range .Tools }}
{{- if not .PrivatePage }}
// {{ .CallFunc }} builds a request for {{ .Name }}.
// The runtime assigns the request ID.
func {{ .CallFunc }}(args {{ if .Payload.Pointer }}*{{ end }}{{ .PayloadAlias }}) (planner.ToolRequest, error) {
    return planner.NewToolRequest({{ $.SpecsAlias }}.{{ .TypedToolVar }}(), args)
}
{{- end }}
{{- end }}
