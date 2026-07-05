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

// Decode{{ .ConstName }} decodes payload into a {{ .ConstName }}Payload and
// populates its Inject()-ed fields in one call, composing
// {{ .ConstName }}PayloadCodec.FromJSON with Inject{{ .ConstName }}.
//
// Custom ToolCallExecutors for the {{ .QualifiedName }} tool must call THIS
// function, not {{ .ConstName }}PayloadCodec.FromJSON followed by a manual
// Inject{{ .ConstName }} call: decoding with the codec alone leaves every
// injected field at its Go zero value with no error, because injected fields
// carry a `json:"-"` wire tag (hidden from the model) and are therefore never
// "missing" from the codec's point of view. Decode{{ .ConstName }} is the one
// path that cannot silently skip injection.
func Decode{{ .ConstName }}(payload []byte, meta runtime.ToolCallMeta, labels map[string]string) (*{{ .ConstName }}Payload, error) {
	p, err := {{ .ConstName }}PayloadCodec.FromJSON(payload)
	if err != nil {
		return nil, err
	}
	if err := Inject{{ .ConstName }}(p, meta, labels); err != nil {
		return nil, err
	}
	return p, nil
}
{{- end }}
{{- end }}
