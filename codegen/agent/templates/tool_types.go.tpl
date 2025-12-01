type (
{{- range $i, $t := .Types }}
{{- if gt $i 0 }}

{{- end }}
    // {{ $t.Doc }}
    {{ $t.Def }}
    {{- if $t.JSONDef }}

    // JSON decode-body type for {{ $t.TypeName }} (server body style)
    {{ $t.JSONDef }}
    {{- end }}
{{- end }}
)

{{- range $t := .Types }}
{{- if $t.ImplementsBounds }}

// ResultBounds implements the agent.BoundedResult interface for {{ $t.TypeName }}.
// It maps the embedded bounds helper struct to the canonical agent.Bounds
// contract. A nil Bounds field means no bounds metadata.
func (v *{{ $t.TypeName }}) ResultBounds() *agent.Bounds {
	if v == nil || v.Bounds == nil {
		return nil
	}
	b := v.Bounds
	hint := ""
	if b.RefinementHint != nil {
		hint = *b.RefinementHint
	}
	return &agent.Bounds{
		Returned:       b.Returned,
		Total:          b.Total,
		Truncated:      b.Truncated,
		RefinementHint: hint,
	}
}
{{- end }}
{{- end }}
