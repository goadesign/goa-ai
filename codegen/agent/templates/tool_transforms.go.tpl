{{- range .Functions }}
// {{ .Name }} converts {{ .ParamTypeRef }} to {{ .ResultTypeRef }}.
func {{ .Name }}(in {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
    var out {{ .ResultTypeRef }}
{{ .Body }}
    return out
}

{{- end }}

// Helper transform functions
{{- range .Helpers }}
func {{ .Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
{{ .Code }}
    return res
}

{{- end }}
