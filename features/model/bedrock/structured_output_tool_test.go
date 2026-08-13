package bedrock

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestStructuredOutputToolContractSupportsAnyJSONResult(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		example string
	}{
		{name: "object", schema: `{"type":"object"}`, example: `{"title":"Inspect evaporator"}`},
		{name: "array", schema: `{"type":"array","items":{"type":"string"}}`, example: `["north","south"]`},
		{name: "string", schema: `{"type":"string"}`, example: `"Inspect evaporator"`},
		{name: "number", schema: `{"type":"number"}`, example: `42`},
		{name: "boolean", schema: `{"type":"boolean"}`, example: `true`},
		{name: "null", schema: `{"type":"null"}`, example: `null`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input, err := structuredOutputToolInput(&model.StructuredOutput{
				Name:                     "result",
				Schema:                   []byte(tt.schema),
				SchemaWithoutRootExample: []byte(tt.schema),
				ExampleJSON:              rawjson.Message(tt.example),
			})
			require.NoError(t, err)

			contract := input.Contract()
			assert.JSONEq(t, `{
				"type": "object",
				"additionalProperties": false,
				"required": ["value"],
				"properties": {"value": `+tt.schema+`}
			}`, string(contract.SchemaWithoutRootExample))
			assert.JSONEq(t, `{"value":`+tt.example+`}`, string(contract.ExampleJSON))

			value, err := unwrapStructuredOutputValue(contract.ExampleJSON)
			require.NoError(t, err)
			assert.JSONEq(t, tt.example, string(value))
		})
	}
}

func TestUnwrapStructuredOutputValueRejectsInvalidEnvelope(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    string
	}{
		{name: "missing value", payload: `{}`, want: `missing required field "value"`},
		{name: "unknown field", payload: `{"value":"ok","extra":true}`, want: `unknown field "extra"`},
		{name: "case variant", payload: `{"Value":"ok"}`, want: `unknown field "Value"`},
		{name: "duplicate value", payload: `{"value":"first","value":"second"}`, want: `duplicate field "value"`},
		{name: "non object", payload: `"ok"`, want: "expected object"},
		{name: "trailing value", payload: `{"value":"ok"} {}`, want: "trailing JSON value"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := unwrapStructuredOutputValue(rawjson.Message(tt.payload))
			require.Error(t, err)
			assert.ErrorContains(t, err, tt.want)
		})
	}
}
