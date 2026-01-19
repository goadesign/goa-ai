{{- range .Types }}
    {{- if .TransportValidationSrc }}
// Validate{{ .TransportTypeName }} runs the validations defined on {{ .TransportTypeName }}.
func Validate{{ .TransportTypeName }}(body {{ .TransportTypeRef }}) (err error) {
        {{- range .TransportValidationSrc }}
    {{ . }}
        {{- end }}
    return
}

    {{- end }}
{{- end }}
