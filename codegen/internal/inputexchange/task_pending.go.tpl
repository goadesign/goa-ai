    if err := {{ .PendingValidate }}(pending); err != nil {
        return nil, goa.PermanentError("internal_error", "%s", err.Error())
    }
    result := &{{ if .Native }}{{ .MCPPackage }}.InputRequired{Requests: make(map[string]{{ .MCPPackage }}.InputRequest)}{{ else }}InputRequiredResult{Meta: resultMeta(), InputRequests: make(map[string]*InputRequest)}{{ end }}
    {{- if not .Native }}
    required := &RequiredClientCapabilities{Elicitation: &ElicitationCapabilities{}}
    {{- end }}
    for id, requestValue := range pending.{{ .RequestsField }} {
        switch requestValue.{{ .RequestField }}.Kind() {
        {{- $input := . }}
        {{- range .Questions }}
        case {{ .RequestKind }}:
            question, _ := requestValue.{{ $input.RequestField }}.{{ .RequestAccessor }}()
            requestID := {{ quote .KeyKind }} + "." + base64.RawURLEncoding.EncodeToString([]byte(id))
            {{ template "question" (request .Question $input.Native $input.MCPPackage "requestID") }}
        {{- end }}
        }
    }
    {{- if not .Native }}
    if len(result.InputRequests) > 0 {
        if err := validateInputCapabilities(meta, required); err != nil {
            return nil, err
        }
    }
    {{- end }}
    return result, nil
