// decodeUnionStrictJSON decodes one union JSON document and rejects fields that
// are not present in the generated union envelope or selected branch contract.
func decodeUnionStrictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("multiple JSON documents")
	}
	return nil
}

// missingUnionValueError reports an omitted value in a tagged union envelope.
func missingUnionValueError() error {
	return tools.NewValidationError(
		"missing required union field \"value\"",
		[]*tools.FieldIssue{
			{
				Field:      "value",
				Constraint: "missing_field",
			},
		},
		nil,
	)
}

// nullUnionValueError reports a null value in a tagged union envelope.
func nullUnionValueError(expected string) error {
	return tools.NewValidationError(
		fmt.Sprintf("union field \"value\" must be a %s, not null", expected),
		[]*tools.FieldIssue{
			{
				Field:            "value",
				Constraint:       "invalid_field_type",
				ExpectedJSONType: expected,
				ActualJSONType:   "null",
			},
		},
		nil,
	)
}

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
// {{ .Constructor }} constructs {{ $u.Name }} with the {{ .Name }} branch set.
func {{ .Constructor }}(v {{ .FieldType }}) {{ $u.Name }} {
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
		return {{ $u.DiscriminatorError }}("", false)
	{{- range $u.Fields }}
	case {{ .KindConst }}:
		{{- if .Nilable }}
		if u.{{ .FieldName }} == nil {
			return missingUnionValueError()
		}
		{{- end }}
		return nil
	{{- end }}
	default:
		got := string(u.kind)
		return {{ $u.DiscriminatorError }}(got, true)
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
		got := string(u.kind)
		return nil, {{ $u.DiscriminatorError }}(got, true)
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
		Type  *string         `json:"type"`
		Value json.RawMessage `json:"value"`
	}
	if err := decodeUnionStrictJSON(data, &raw); err != nil {
		return err
	}
	if raw.Type == nil {
		return {{ $u.DiscriminatorError }}("", false)
	}
	if len(raw.Value) == 0 {
		return missingUnionValueError()
	}
	switch *raw.Type {
	{{- range $u.Fields }}
	case string({{ .KindConst }}):
		{{- if .JSONType }}
		if bytes.Equal(bytes.TrimSpace(raw.Value), []byte("null")) {
			return nullUnionValueError({{ printf "%q" .JSONType }})
		}
		{{- end }}
		var v {{ .FieldType }}
		if err := decodeUnionStrictJSON(raw.Value, &v); err != nil {
			{{- if .JSONType }}
			var typeErr *json.UnmarshalTypeError
			if errors.As(err, &typeErr) && typeErr.Value != "" {
				return tools.NewValidationError(
					err.Error(),
					[]*tools.FieldIssue{
						{
							Field:            "value",
							Constraint:       "invalid_field_type",
							ExpectedJSONType: {{ printf "%q" .JSONType }},
							ActualJSONType:   typeErr.Value,
						},
					},
					nil,
				)
			}
			{{- end }}
			return err
		}
		u.kind = {{ .KindConst }}
		u.{{ .FieldName }} = v
	{{- end }}
	default:
		return {{ $u.DiscriminatorError }}(*raw.Type, true)
	}
	return nil
}

func {{ $u.DiscriminatorError }}(got string, typePresent bool) error {
	return tools.NewUnionDiscriminatorError("{{ $u.Name }}", got, typePresent, []string{
		{{- range $u.Fields }}
		string({{ .KindConst }}),
		{{- end }}
	})
}
{{- end }}
