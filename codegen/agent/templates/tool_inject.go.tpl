{{- range .Tools }}
{{- if .Injected }}
{{- $tool := . }}

// {{ .InjectFunc }} fills the fields that Inject() marked on the
// {{ .QualifiedName }} tool input. It copies call information from meta and
// reads named values from labels. Missing or invalid values return an error
// that names the tool and field.
//
// Generated executors call this after decoding and before running the tool.
// The model never supplies these fields.
func {{ .InjectFunc }}(p *{{ .PayloadTypeName }}, meta runtime.ToolCallMeta, labels map[string]string) error {
{{- range .Injected }}
{{- if .IsMetaBacked }}
	{
		v := meta.{{ .MetaField }}
		p.{{ .GoFieldName }} = v
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
		p.{{ .GoFieldName }} = v
	}
{{- end }}
{{- end }}
	return nil
}

// {{ .DecodeFunc }} decodes payload into a {{ .PayloadTypeName }} and fills
// the fields supplied by the server.
//
// Custom executors for {{ .QualifiedName }} must call this function. Calling
// {{ .PayloadCodecName }}.FromJSON alone does not fill fields marked by
// Inject().
func {{ .DecodeFunc }}(payload []byte, meta runtime.ToolCallMeta, labels map[string]string) (*{{ .PayloadTypeName }}, error) {
	p, err := {{ .PayloadCodecName }}.FromJSON(payload)
	if err != nil {
		return nil, err
	}
	if err := {{ .InjectFunc }}(p, meta, labels); err != nil {
		return nil, err
	}
	return p, nil
}
{{- end }}
{{- end }}
