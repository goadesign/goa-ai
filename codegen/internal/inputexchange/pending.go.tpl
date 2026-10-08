    if err := {{ .PendingValidate }}(pending); err != nil {
        return nil, goa.PermanentError("internal_error", "%s", err.Error())
    }
    result := &{{ if .Native }}{{ .MCPPackage }}.InputRequired{}{{ else }}InputRequiredResult{Meta: resultMeta()}{{ end }}
    {{- if .PendingStateField }}
    if pending.{{ .PendingStateField }} != nil {
        value := string(*pending.{{ .PendingStateField }})
        result.RequestState = &value
    }
    {{- end }}
    {{- if .RequestsField }}
    {{- if and .Questions (not .Native) }}
    required := &RequiredClientCapabilities{Elicitation: &ElicitationCapabilities{}}
    {{- end }}
    if pending.{{ .RequestsField }} != nil {
        result.{{ if .Native }}Requests{{ else }}InputRequests{{ end }} = make(map[string]{{ if .Native }}{{ .MCPPackage }}.InputRequest{{ else }}*InputRequest{{ end }})
        {{- $input := . }}
        {{- range .Questions }}
        if question := pending.{{ $input.RequestsField }}.{{ .RequestField }}; question != nil {
            {{ template "question" (request . $input.Native $input.MCPPackage (quote .Name)) }}
        }
        {{- end }}
    }
    {{- if and .Questions (not .Native) }}
    if len(result.{{ if .Native }}Requests{{ else }}InputRequests{{ end }}) > 0 {
        if err := validateInputCapabilities(meta, required); err != nil {
            return nil, err
        }
    }
    {{- end }}
    {{- end }}
    if {{ if .RequestsField }}pending.{{ .RequestsField }} == nil && {{ end }}result.RequestState == nil {
        return nil, goa.PermanentError("internal_error", "unfinished operation requires requests or requestState")
    }
    return result, nil
