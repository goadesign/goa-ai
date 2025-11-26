package {{ .PackageName }}

// Compiled activity names for reference.
const (
{{- range .Runtime.Activities }}
    {{ goify .Name true }}ActivityName = {{ printf "%q" .Name }}
{{- end }}
)
