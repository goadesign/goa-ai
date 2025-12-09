// AgentCard implements the agent/card A2A method.
func (a *Adapter) AgentCard(ctx context.Context) (*AgentCardResponse, error) {
	return &AgentCardResponse{
		ProtocolVersion:    {{ quote .ProtocolVersion }},
		Name:               {{ quote .Agent.Name }},
		{{- if .Agent.Description }}
		Description:        ptrString({{ quote .Agent.Description }}),
		{{- end }}
		URL:                a.baseURL,
		Version:            ptrString("1.0.0"),
		Capabilities:       map[string]any{"streaming": true},
		DefaultInputModes:  []string{"application/json"},
		DefaultOutputModes: []string{"application/json"},
		Skills: []*A2ASkill{
			{{- range .Skills }}
			{
				ID:          {{ quote .ID }},
				Name:        {{ quote .Name }},
				{{- if .Description }}
				Description: ptrString({{ quote .Description }}),
				{{- end }}
				{{- if .Tags }}
				Tags:        []string{ {{- range $i, $t := .Tags }}{{ if $i }}, {{ end }}{{ quote $t }}{{ end -}} },
				{{- end }}
				InputModes:  []string{"application/json"},
				OutputModes: []string{"application/json"},
			},
			{{- end }}
		},
		{{- if .HasSecuritySchemes }}
		SecuritySchemes: map[string]*A2ASecurityScheme{
			{{- range .SecuritySchemes }}
			{{ quote .Name }}: {
				Type: {{ quote .Type }},
				{{- if .Scheme }}
				Scheme: ptrString({{ quote .Scheme }}),
				{{- end }}
				{{- if .In }}
				In: ptrString({{ quote .In }}),
				{{- end }}
				{{- if .ParamName }}
				Name: ptrString({{ quote .ParamName }}),
				{{- end }}
			},
			{{- end }}
		},
		Security: []map[string][]string{
			{{- range .SecurityRequirements }}
			{
				{{- range $name, $scopes := . }}
				{{ quote $name }}: { {{- range $i, $s := $scopes }}{{ if $i }}, {{ end }}{{ quote $s }}{{ end -}} },
				{{- end }}
			},
			{{- end }}
		},
		{{- end }}
	}, nil
}
