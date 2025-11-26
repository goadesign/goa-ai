type (
{{- range $i, $t := .Types }}
{{ if gt $i 0 }}

{{ end }}
    // {{ $t.Doc }}
    {{ $t.Def }}
    {{- if $t.JSONDef }}

    // JSON decode-body type for {{ $t.TypeName }} (server body style)
    {{ $t.JSONDef }}
    {{- end }}
{{- end }}
)
