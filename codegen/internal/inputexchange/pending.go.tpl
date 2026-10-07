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
            {{- if .Schema }}
            {{- if $input.Native }}
            params, err := json.Marshal(struct {
                Mode string `json:"mode"`
                Message string `json:"message"`
                RequestedSchema json.RawMessage `json:"requestedSchema"`
            }{Mode: "form", Message: string({{ if .MessagePointer }}*{{ end }}question.{{ .MessageField }}), RequestedSchema: json.RawMessage({{ quote .Schema }})})
            if err != nil { return nil, goa.PermanentError("internal_error", "%s", err.Error()) }
            result.Requests[{{ quote .Name }}] = {{ $input.MCPPackage }}.InputRequest{Method: "elicitation/create", Params: params}
            {{- else }}
            params := &ElicitationFormParams{Message: string({{ if .MessagePointer }}*{{ end }}question.{{ .MessageField }}), RequestedSchema: json.RawMessage({{ quote .Schema }})}
            result.InputRequests[{{ quote .Name }}] = &InputRequest{Method: "elicitation/create", Params: NewElicitationParamsForm(params)}
            required.Elicitation.Form = &struct{}{}
            {{- end }}
            {{- else }}
            {{- if $input.Native }}
            params, err := json.Marshal(struct {
                Mode string `json:"mode"`
                Message string `json:"message"`
                URL string `json:"url"`
            }{Mode: "url", Message: string({{ if .MessagePointer }}*{{ end }}question.{{ .MessageField }}), URL: string({{ if .URLPointer }}*{{ end }}question.{{ .URLField }})})
            if err != nil { return nil, goa.PermanentError("internal_error", "%s", err.Error()) }
            result.Requests[{{ quote .Name }}] = {{ $input.MCPPackage }}.InputRequest{Method: "elicitation/create", Params: params}
            {{- else }}
            params := &ElicitationURLParams{Message: string({{ if .MessagePointer }}*{{ end }}question.{{ .MessageField }}), URL: string({{ if .URLPointer }}*{{ end }}question.{{ .URLField }})}
            result.InputRequests[{{ quote .Name }}] = &InputRequest{Method: "elicitation/create", Params: NewElicitationParamsURL(params)}
            required.Elicitation.URL = &struct{}{}
            {{- end }}
            {{- end }}
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
