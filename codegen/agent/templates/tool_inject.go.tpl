{{- range .Tools }}
{{- if .Injected }}
{{- $tool := . }}

// Inject{{ .ConstName }} populates the server-owned fields Inject() marked on
// the {{ .QualifiedName }} tool payload. Meta-backed fields are copied
// directly from the run's ToolCallMeta. Label-backed fields are read from the
// run's labels, converted to their declared type, and validated using the
// same rules enforced on the design; a missing label or a validation failure
// is a precise, actionable error naming the tool and the field.
//
// Both topologies (in-process executors and the registry-served provider)
// call this function between decode and execute so injection behaves
// identically regardless of where the tool runs.
//
// Injected fields are pointers on the tool payload struct: the model-facing
// contract marks them optional (the model never supplies them), so this
// function is the single point that fills them in.
func Inject{{ .ConstName }}(p *{{ .ConstName }}Payload, meta runtime.ToolCallMeta, labels map[string]string) error {
{{- range .Injected }}
{{- if .IsMetaBacked }}
	{
		v := meta.{{ .MetaField }}
		p.{{ .GoFieldName }} = &v
	}
{{- else }}
	{
		v, ok := labels[{{ printf "%q" .LabelKey }}]
		if !ok {
			return fmt.Errorf("tool %q: required label %q is missing; call WithLabels(%q, ...) at run start", {{ printf "%q" $tool.QualifiedName }}, {{ printf "%q" .LabelKey }}, {{ printf "%q" .LabelKey }})
		}
		{{- if .ValidationCode }}
		var err error
		{{ .ValidationCode }}
		if err != nil {
			return fmt.Errorf("tool %q: label %q failed validation: %w", {{ printf "%q" $tool.QualifiedName }}, {{ printf "%q" .LabelKey }}, err)
		}
		{{- end }}
		p.{{ .GoFieldName }} = &v
	}
{{- end }}
{{- end }}
	return nil
}
{{- end }}
{{- end }}
