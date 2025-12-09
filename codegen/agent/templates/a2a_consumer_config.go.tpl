package {{ .PackageName }}

import (
	"encoding/json"

	a2a "goa.design/goa-ai/runtime/a2a"
	"goa.design/goa-ai/runtime/agent/tools"
)

// ProviderConfig contains static configuration for consuming the remote A2A
// provider identified by the suite {{ printf "%s" .Suite }}.
var ProviderConfig = a2a.ProviderConfig{
	Suite: {{ printf "%q" .Suite }},
	Skills: []a2a.SkillConfig{
		{{- range .Skills }}
		{
			ID:          {{ printf "%q" .ID }},
			Description: {{ printf "%q" .Description }},
			Payload: tools.TypeSpec{
				Name:        {{ printf "%q" .ID }},
				Schema:      []byte({{ printf "%q" .InputSchema }}),
				ExampleJSON: []byte({{ printf "%q" .ExampleArgs }}),
				Codec: tools.JSONCodec[any]{
					ToJSON: func(v any) ([]byte, error) {
						return json.Marshal(v)
					},
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
				Name: {{ printf "%q" .ID }} + "_result",
				Codec: tools.JSONCodec[any]{
					ToJSON: func(v any) ([]byte, error) {
						return json.Marshal(v)
					},
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
}


