package openai

// These tests check the explanation sent with strict function tools and the
// corresponding argument decoding. Only null choices added by the adapter mean
// omission; explicit values and nulls allowed by the original schema survive.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestEncodeToolsExplainsOptionalFieldOmission(t *testing.T) {
	tests := []struct {
		name         string
		schema       rawjson.Message
		providerArgs string
		wantArgs     string
		wantGuidance bool
	}{
		{
			name: "omitted optional enum",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{
					"query":{"type":"string"},
					"priority":{"type":"string","enum":["low","high"]}
				},
				"required":["query"],
				"additionalProperties":false
			}`),
			providerArgs: `{"query":"alerts","priority":null}`,
			wantArgs:     `{"query":"alerts"}`,
			wantGuidance: true,
		},
		{
			name: "explicit optional enum",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{
					"query":{"type":"string"},
					"priority":{"type":"string","enum":["low","high"]}
				},
				"required":["query"],
				"additionalProperties":false
			}`),
			providerArgs: `{"query":"alerts","priority":"high"}`,
			wantArgs:     `{"query":"alerts","priority":"high"}`,
			wantGuidance: true,
		},
		{
			name: "nested optional value with meaningful null",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{
					"filters":{
						"type":"array",
						"items":{
							"type":"object",
							"properties":{
								"priority":{"type":"string","enum":["low","high"]},
								"label":{"type":["string","null"]}
							},
							"required":["label"],
							"additionalProperties":false
						}
					}
				},
				"required":["filters"],
				"additionalProperties":false
			}`),
			providerArgs: `{"filters":[{"priority":null,"label":null},{"priority":"high","label":"review"}]}`,
			wantArgs:     `{"filters":[{"label":null},{"priority":"high","label":"review"}]}`,
			wantGuidance: true,
		},
		{
			name: "required value",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{"query":{"type":"string"}},
				"required":["query"],
				"additionalProperties":false
			}`),
			providerArgs: `{"query":"alerts"}`,
			wantArgs:     `{"query":"alerts"}`,
		},
		{
			name: "original optional null",
			schema: rawjson.Message(`{
				"type":"object",
				"properties":{"label":{"type":["string","null"]}},
				"additionalProperties":false
			}`),
			providerArgs: `{"label":null}`,
			wantArgs:     `{"label":null}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const description = "Find matching records."
			definition := &model.ToolDefinition{
				Name:        "records.find",
				Description: description,
				Input:       mustOpenAIToolInput(tt.schema),
			}

			for _, exact := range []bool{false, true} {
				encoded, codec, err := encodeTools([]*model.ToolDefinition{definition}, "gpt-4o", exact)
				require.NoError(t, err)
				require.Len(t, encoded, 1)
				function := encoded[0].OfFunction
				require.NotNil(t, function)
				assert.Equal(t, !exact, function.Strict.Value)

				wantDescription := description
				if !exact && tt.wantGuidance {
					wantDescription += "\n\nIn the strict tool schema, an optional field that should be omitted is represented by null. Use null when the tool instructions say to omit a field."
				}
				assert.Equal(t, wantDescription, function.Description.Value)
				assert.Equal(t, description, definition.Description)
				assert.JSONEq(t, string(tt.schema), string(definition.Input.Contract().Schema))

				if exact {
					parameters, marshalErr := json.Marshal(function.Parameters)
					require.NoError(t, marshalErr)
					assert.JSONEq(t, string(tt.schema), string(parameters))
					continue
				}
				args, err := codec.canonicalPayload(function.Name, []byte(tt.providerArgs))
				require.NoError(t, err)
				assert.JSONEq(t, tt.wantArgs, string(args))
			}
		})
	}
}
