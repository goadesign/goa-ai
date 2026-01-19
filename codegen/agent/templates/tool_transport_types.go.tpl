type (
{{- range $i, $t := .Types }}
    {{- if $t.TransportDef }}
        {{- if gt $i 0 }}

        {{- end }}
    // {{ $t.TransportTypeName }} is the internal JSON transport type for {{ $t.TypeName }}.
    // It lives in the toolset-local http package and is used only for JSON
    // decode + validation (missing-field detection) before transforming into
    // the public tool type.
    {{ $t.TransportDef }}
    {{- end }}
{{- end }}
)
