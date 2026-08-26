{{- range .Types }}
    {{- if .TransportValidationSrc }}
// {{ .ValidateFunc }} runs the validations defined on {{ .TransportTypeName }}.
func {{ .ValidateFunc }}(body {{ .TransportTypeRef }}) (err error) {
        {{- range .TransportValidationSrc }}
    {{ . }}
        {{- end }}
    return
}

    {{- end }}
{{- end }}
