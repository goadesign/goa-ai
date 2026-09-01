const (
{{- range .Exports }}
    // {{ .ConstName }} is the route for the exported {{ printf "%q" .Name }} toolset.
    {{ .ConstName }} = {{ printf "%q" .QualifiedName }}
{{- end }}
)
