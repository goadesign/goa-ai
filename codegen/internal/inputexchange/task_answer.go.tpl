    payload.{{ .ResponsesField }} = make({{ .ResponsesRef }}, len(inputResponses))
    for name, raw := range inputResponses {
        kind, encodedID, found := strings.Cut(name, ".")
        if !found {
            continue
        }
        id, err := base64.RawURLEncoding.DecodeString(encodedID)
        if err != nil || base64.RawURLEncoding.EncodeToString(id) != encodedID {
            continue
        }
        switch kind {
        {{- $input := . }}
        {{- range .Questions }}
        case {{ quote .KeyKind }}:
            answer, err := {{ .Decode }}(raw)
            if err != nil {
                return goa.PermanentError("invalid_params", "input response %q: %s", name, err.Error())
            }
            response := &{{ .ResponseRef }}{ {{ .AnswerField }}: {{ if .AnswerDereference }}*{{ end }}answer }
            responseValue := {{ .ResponseConstructor }}(response)
            if _, exists := payload.{{ $input.ResponsesField }}[{{ $input.ResponseKeyRef }}(id)]; exists {
                return goa.PermanentError("invalid_params", "multiple answers refer to native question %q", string(id))
            }
            payload.{{ $input.ResponsesField }}[{{ $input.ResponseKeyRef }}(id)] = &{{ $input.ResponseRef }}{
                {{ $input.ResponseField }}: {{ if .ResponsePointer }}&{{ end }}responseValue,
            }
        {{- end }}
        }
    }
