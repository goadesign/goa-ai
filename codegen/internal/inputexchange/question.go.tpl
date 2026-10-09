{{ define "question" }}
            {{- if .Schema }}
            {{- if .Native }}
            params, err := json.Marshal(struct {
                Mode string `json:"mode"`
                Message string `json:"message"`
                RequestedSchema json.RawMessage `json:"requestedSchema"`
            }{Mode: "form", Message: string({{ if .MessagePointer }}*{{ end }}question.{{ .MessageField }}), RequestedSchema: json.RawMessage({{ quote .Schema }})})
            if err != nil { return nil, goa.PermanentError("internal_error", "%s", err.Error()) }
            result.Requests[{{ .RequestKey }}] = {{ .MCPPackage }}.InputRequest{Method: "elicitation/create", Params: params}
            {{- else }}
            params := &ElicitationFormParams{Message: string({{ if .MessagePointer }}*{{ end }}question.{{ .MessageField }}), RequestedSchema: json.RawMessage({{ quote .Schema }})}
            result.InputRequests[{{ .RequestKey }}] = &InputRequest{Method: "elicitation/create", Params: NewElicitationParamsForm(params)}
            required.Elicitation.Form = &struct{}{}
            {{- end }}
            {{- else }}
            {{- if .Native }}
            params, err := json.Marshal(struct {
                Mode string `json:"mode"`
                Message string `json:"message"`
                URL string `json:"url"`
            }{Mode: "url", Message: string({{ if .MessagePointer }}*{{ end }}question.{{ .MessageField }}), URL: string({{ if .URLPointer }}*{{ end }}question.{{ .URLField }})})
            if err != nil { return nil, goa.PermanentError("internal_error", "%s", err.Error()) }
            result.Requests[{{ .RequestKey }}] = {{ .MCPPackage }}.InputRequest{Method: "elicitation/create", Params: params}
            {{- else }}
            params := &ElicitationURLParams{Message: string({{ if .MessagePointer }}*{{ end }}question.{{ .MessageField }}), URL: string({{ if .URLPointer }}*{{ end }}question.{{ .URLField }})}
            result.InputRequests[{{ .RequestKey }}] = &InputRequest{Method: "elicitation/create", Params: NewElicitationParamsURL(params)}
            required.Elicitation.URL = &struct{}{}
            {{- end }}
            {{- end }}
{{ end }}
