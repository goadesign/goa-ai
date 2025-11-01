{{- range .Functions }}
// {{ .Name }} converts {{ .ParamTypeRef }} to {{ .ResultTypeRef }}.
func {{ .Name }}(in {{ .ParamTypeRef }}) ({{ .ResultTypeRef }}, error) {
    var out {{ .ResultTypeRef }}
{{ .Body }}
    return out, nil
}

{{- range .Helpers }}
func {{ .Name }}(v {{ .ParamTypeRef }}) {{ .ResultTypeRef }} {
{{ .Code }}
    return res
}

{{- end }}
{{- end }}
