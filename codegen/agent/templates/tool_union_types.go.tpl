{{- range $i, $u := .Unions }}
{{- if gt $i 0 }}

{{- end }}
// {{ $u.Name }} is a sum-type union.
type {{ $u.Name }} struct {
	kind {{ $u.KindName }}
	{{- range $u.Fields }}
	{{ .FieldName }} {{ .FieldType }}
	{{- end }}
}

// {{ $u.KindName }} enumerates the union variants for {{ $u.Name }}.
type {{ $u.KindName }} string

const (
	{{- range $u.Fields }}
	// {{ .KindConst }} identifies the {{ .Name }} branch of the union.
	{{ .KindConst }} {{ $u.KindName }} = "{{ .TypeTag }}"
	{{- end }}
)

// Kind returns the discriminator value of the union.
func (u {{ $u.Name }}) Kind() {{ $u.KindName }} {
	return u.kind
}

{{- range $u.Fields }}
// New{{ $u.Name }}{{ .FieldName }} constructs a {{ $u.Name }} with the {{ .Name }} branch set.
func New{{ $u.Name }}{{ .FieldName }}(v {{ .FieldType }}) {{ $u.Name }} {
	return {{ $u.Name }}{
		kind: {{ .KindConst }},
		{{ .FieldName }}: v,
	}
}

// As{{ .FieldName }} returns the value of the {{ .Name }} branch if set.
func (u {{ $u.Name }}) As{{ .FieldName }}() (_ {{ .FieldType }}, ok bool) {
	if u.kind != {{ .KindConst }} {
		return
	}
	return u.{{ .FieldName }}, true
}

// Set{{ .FieldName }} sets the {{ .Name }} branch of the union.
func (u *{{ $u.Name }}) Set{{ .FieldName }}(v {{ .FieldType }}) {
	u.kind = {{ .KindConst }}
	u.{{ .FieldName }} = v
}
{{- end }}

// Validate ensures the union discriminant is valid.
func (u {{ $u.Name }}) Validate() error {
	switch u.kind {
	case "":
		return goa.InvalidEnumValueError("type", "", []any{
			{{- range $u.Fields }}
			string({{ .KindConst }}),
			{{- end }}
		})
	{{- range $u.Fields }}
	case {{ .KindConst }}:
		return nil
	{{- end }}
	default:
		return goa.InvalidEnumValueError("type", u.kind, []any{
			{{- range $u.Fields }}
			string({{ .KindConst }}),
			{{- end }}
		})
	}
}

// MarshalJSON marshals the union into the canonical {type,value} JSON shape.
func (u {{ $u.Name }}) MarshalJSON() ([]byte, error) {
	if err := u.Validate(); err != nil {
		return nil, err
	}
	var (
		value any
	)
	switch u.kind {
	{{- range $u.Fields }}
	case {{ .KindConst }}:
		value = u.{{ .FieldName }}
	{{- end }}
	default:
		return nil, fmt.Errorf("unexpected {{ $u.Name }} discriminant %q", u.kind)
	}
	return json.Marshal(struct {
		Type  string `json:"type"`
		Value any    `json:"value"`
	}{
		Type:  string(u.kind),
		Value: value,
	})
}

// UnmarshalJSON unmarshals the union from the canonical {type,value} JSON shape.
func (u *{{ $u.Name }}) UnmarshalJSON(data []byte) error {
	var raw struct {
		Type  string          `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	switch raw.Type {
	{{- range $u.Fields }}
	case string({{ .KindConst }}):
		var v {{ .FieldType }}
		if err := json.Unmarshal(raw.Value, &v); err != nil {
			return err
		}
		u.kind = {{ .KindConst }}
		u.{{ .FieldName }} = v
	{{- end }}
	default:
		return fmt.Errorf("unexpected {{ $u.Name }} type %q", raw.Type)
	}
	return nil
}
{{- end }}
