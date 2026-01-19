type (
{{- range $i, $t := .Types }}
{{- if gt $i 0 }}

{{- end }}
    // {{ $t.Doc }}
    {{ $t.Def }}
{{- end }}
)

{{- range $t := .Types }}
{{- if $t.ImplementsBounds }}

// ResultBounds implements the agent.BoundedResult interface for {{ $t.TypeName }}.
// It maps the tool result fields to the canonical agent.Bounds contract.
func (v *{{ $t.TypeName }}) ResultBounds() *agent.Bounds {
	if v == nil {
		return nil
	}
	hint := ""
	if v.RefinementHint != nil {
		hint = *v.RefinementHint
	}
	var next *string
    {{- if $t.NextCursorGoField }}
	next = v.{{ $t.NextCursorGoField }}
    {{- end }}
	return &agent.Bounds{
		Returned:       v.Returned,
		Total:          v.Total,
		Truncated:      v.Truncated,
		NextCursor:     next,
		RefinementHint: hint,
	}
}
{{- end }}
{{- end }}
