// AgentCard implements the agent/card A2A method.
func (a *Adapter) AgentCard(ctx context.Context) (*{{ .A2APackage }}.AgentCardResponse, error) {
	return &{{ .A2APackage }}.AgentCardResponse{
		ProtocolVersion:    {{ quote .ProtocolVersion }},
		Name:               {{ quote .Agent.Name }},
		{{- if .Agent.Description }}
		Description:        {{ quote .Agent.Description }},
		{{- end }}
		URL:                a.BaseURL(),
		Version:            "1.0.0",
		Capabilities:       map[string]any{"streaming": true},
		DefaultInputModes:  []string{"application/json"},
		DefaultOutputModes: []string{"application/json"},
		Skills: []*{{ .A2APackage }}.A2ASkill{
			{{- range .Skills }}
			{
				ID:          {{ quote .ID }},
				Name:        {{ quote .Name }},
				{{- if .Description }}
				Description: {{ quote .Description }},
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
		SecuritySchemes: map[string]*{{ .A2APackage }}.A2ASecurityScheme{
			{{- range .SecuritySchemes }}
			{{ quote .Name }}: {
				Type: {{ quote .Type }},
				{{- if .Scheme }}
				Scheme: {{ quote .Scheme }},
				{{- end }}
				{{- if .In }}
				In: {{ quote .In }},
				{{- end }}
				{{- if .ParamName }}
				Name: {{ quote .ParamName }},
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
