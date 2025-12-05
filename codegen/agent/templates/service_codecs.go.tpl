{{- range .Types }}
// Marshal{{ .Name }} serializes {{ .PtrRef }} into JSON.
func Marshal{{ .Name }}(v {{ .PtrRef }}) ([]byte, error) {
	if v == nil {
		return nil, fmt.Errorf("{{ .VarName }} is nil")
	}
	return json.Marshal(v)
}

// Unmarshal{{ .Name }} deserializes JSON into {{ .PtrRef }}.
func Unmarshal{{ .Name }}(data []byte) ({{ .PtrRef }}, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("{{ .VarName }} JSON is empty")
	}
	var v {{ .ValRef }}
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("decode {{ .VarName }}: %w", err)
	}
	return &v, nil
}

{{- end }}


