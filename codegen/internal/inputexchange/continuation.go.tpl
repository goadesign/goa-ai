    if requestState != nil || inputResponses != nil {
        continuation := &{{ .ContinuationRef }}{}
        {{- if .StateField }}
        if requestState != nil {
            value := {{ .StateRef }}(*requestState)
            continuation.{{ .StateField }} = &value
        }
        {{- else }}
        if requestState != nil {
            return goa.PermanentError("invalid_params", "this operation does not accept requestState")
        }
        {{- end }}
        {{- if .Questions }}
        for name, raw := range inputResponses {
            switch name {
            {{- $input := . }}
            {{- range .Questions }}
            case {{ quote .Name }}:
                answer, err := {{ .Decode }}(raw)
                if err != nil {
                    return goa.PermanentError("invalid_params", "input response %q: %s", name, err.Error())
                }
                if continuation.{{ $input.ResponsesField }} == nil {
                    continuation.{{ $input.ResponsesField }} = &{{ $input.ResponsesRef }}{}
                }
                continuation.{{ $input.ResponsesField }}.{{ .ResponseField }} = &{{ .ResponseRef }}{ {{ .AnswerField }}: {{ if .AnswerDereference }}*{{ end }}answer }
            {{- end }}
            }
        }
        {{- end }}
        payload.{{ .ContinuationField }} = continuation
    }
