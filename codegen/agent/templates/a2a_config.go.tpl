// {{ .AgentGoName }}ServerConfig contains static configuration for the A2A server.
var {{ .AgentGoName }}ServerConfig = a2a.ServerConfig{
	Suite:            {{ printf "%q" .SuiteQualifiedName }},
	AgentName:        {{ printf "%q" .AgentName }},
	AgentDescription: {{ printf "%q" .Description }},
	Version:          "1.0",
	DefaultInputModes: []string{
		"application/json",
	},
	DefaultOutputModes: []string{
		"application/json",
	},
	Capabilities: map[string]any{
		"streaming": true,
	},
	Skills: []a2a.SkillConfig{
		{{- range .Skills }}
		{
			ID:          {{ printf "%q" .QualifiedName }},
			Description: {{ printf "%q" .Description }},
			Payload: tools.TypeSpec{
				Name:        {{ printf "%q" .PayloadType }},
				Schema:      []byte({{ printf "%q" .InputSchema }}),
				ExampleJSON: []byte({{ printf "%q" .ExampleArgs }}),
				Codec: tools.JSONCodec[any]{
					ToJSON: json.Marshal,
					FromJSON: func(data []byte) (any, error) {
						if len(data) == 0 {
							return nil, nil
						}
						var out any
						if err := json.Unmarshal(data, &out); err != nil {
							return nil, err
						}
						return out, nil
					},
				},
			},
			Result: tools.TypeSpec{
				Name: {{ printf "%q" .ResultType }},
				Codec: tools.JSONCodec[any]{
					ToJSON: json.Marshal,
					FromJSON: func(data []byte) (any, error) {
						if len(data) == 0 {
							return nil, nil
						}
						var out any
						if err := json.Unmarshal(data, &out); err != nil {
							return nil, err
						}
						return out, nil
					},
				},
			},
			ExampleArgs: {{ printf "%q" .ExampleArgs }},
		},
		{{- end }}
	},
	// Security mapping from design-level A2ASecurityData into runtime.SecurityConfig
	// can be added in a future iteration when needed.
	Security: a2a.SecurityConfig{},
}

// {{ .AgentGoName }}A2AProviderConfig contains the ProviderConfig for consumers
// that integrate with this agent via FromRegistry.
var {{ .AgentGoName }}A2AProviderConfig = a2a.ProviderConfig{
	Suite:  {{ printf "%q" .SuiteQualifiedName }},
	Skills: {{ .AgentGoName }}ServerConfig.Skills,
}

