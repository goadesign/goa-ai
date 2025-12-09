// agentCardTemplate is the static agent card template with all skills, security
// schemes, and capabilities inlined at generation time. Only the URL field
// needs to be set at runtime.
var agentCardTemplate = AgentCard{
	ProtocolVersion: A2AProtocolVersion,
	Name:            {{ printf "%q" .Agent.Name }},
	{{- if .Agent.Description }}
	Description:     {{ printf "%q" .Agent.Description }},
	{{- end }}
	Version:         "1.0.0",
	Capabilities: map[string]any{
		"streaming": true,
	},
	DefaultInputModes:  []string{"application/json"},
	DefaultOutputModes: []string{"application/json"},
	Skills: []*Skill{
		{{- range .Skills }}
		{
			ID:          {{ printf "%q" .ID }},
			Name:        {{ printf "%q" .Name }},
			{{- if .Description }}
			Description: {{ printf "%q" .Description }},
			{{- end }}
			{{- if .Tags }}
			Tags:        []string{ {{- range $i, $t := .Tags }}{{ if $i }}, {{ end }}{{ printf "%q" $t }}{{ end -}} },
			{{- end }}
			InputModes:  []string{"application/json"},
			OutputModes: []string{"application/json"},
		},
		{{- end }}
	},
	{{- if .HasSecuritySchemes }}
	SecuritySchemes: map[string]*SecurityScheme{
		{{- range .SecuritySchemes }}
		{{ printf "%q" .Name }}: {
			Type: {{ printf "%q" .Type }},
			{{- if .Scheme }}
			Scheme: {{ printf "%q" .Scheme }},
			{{- end }}
			{{- if .In }}
			In: {{ printf "%q" .In }},
			{{- end }}
			{{- if .ParamName }}
			Name: {{ printf "%q" .ParamName }},
			{{- end }}
			{{- if .Flows }}
			Flows: &OAuth2Flows{
				{{- if .Flows.ClientCredentials }}
				ClientCredentials: &OAuth2Flow{
					TokenURL: {{ printf "%q" .Flows.ClientCredentials.TokenURL }},
					{{- if .Flows.ClientCredentials.Scopes }}
					Scopes: map[string]string{
						{{- range $k, $v := .Flows.ClientCredentials.Scopes }}
						{{ printf "%q" $k }}: {{ printf "%q" $v }},
						{{- end }}
					},
					{{- end }}
				},
				{{- end }}
				{{- if .Flows.AuthorizationCode }}
				AuthorizationCode: &OAuth2Flow{
					AuthorizationURL: {{ printf "%q" .Flows.AuthorizationCode.AuthorizationURL }},
					TokenURL: {{ printf "%q" .Flows.AuthorizationCode.TokenURL }},
					{{- if .Flows.AuthorizationCode.Scopes }}
					Scopes: map[string]string{
						{{- range $k, $v := .Flows.AuthorizationCode.Scopes }}
						{{ printf "%q" $k }}: {{ printf "%q" $v }},
						{{- end }}
					},
					{{- end }}
				},
				{{- end }}
			},
			{{- end }}
		},
		{{- end }}
	},
	Security: []map[string][]string{
		{{- range .SecurityRequirements }}
		{
			{{- range $name, $scopes := . }}
			{{ printf "%q" $name }}: { {{- range $i, $s := $scopes }}{{ if $i }}, {{ end }}{{ printf "%q" $s }}{{ end -}} },
			{{- end }}
		},
		{{- end }}
	},
	{{- end }}
}

// GetAgentCard returns an A2A-compliant agent card for the {{ .Agent.GoName }} agent.
// The baseURL parameter should be the HTTP endpoint where the agent is accessible.
// This function copies the static template and sets only the URL field.
func GetAgentCard(baseURL string) *AgentCard {
	card := agentCardTemplate
	card.URL = baseURL
	return &card
}
