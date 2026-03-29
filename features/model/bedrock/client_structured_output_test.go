package bedrock

import (
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestEncodeOutputConfigStructuredOutput(t *testing.T) {
	cfg, err := encodeOutputConfig(&model.StructuredOutput{
		Schema: []byte(`{"type":"object","required":["assistant_text"]}`),
		Name:   "structured_output",
	})
	require.NoError(t, err)
	require.NotNil(t, cfg)
	require.NotNil(t, cfg.TextFormat)
	require.Equal(t, "json_schema", string(cfg.TextFormat.Type))
	member, ok := cfg.TextFormat.Structure.(*brtypes.OutputFormatStructureMemberJsonSchema)
	require.True(t, ok)
	require.NotNil(t, member.Value.Schema)
	require.JSONEq(t, `{"type":"object","required":["assistant_text"],"additionalProperties":false}`, *member.Value.Schema)
	require.NotNil(t, member.Value.Name)
	require.Equal(t, "structured_output", *member.Value.Name)
}

func TestEncodeOutputConfigRejectsMissingSchema(t *testing.T) {
	_, err := encodeOutputConfig(&model.StructuredOutput{})
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires a schema")
}

func TestEncodeOutputConfigNormalizesStructuredOutputSchemaForBedrock(t *testing.T) {
	cfg, err := encodeOutputConfig(&model.StructuredOutput{
		Schema: []byte(`{
			"$schema": "https://json-schema.org/draft/2020-12/schema",
			"type": "object",
			"properties": {
				"count": {
					"type": "integer",
					"format": "int64",
					"minimum": 1
				},
				"metadata": {
					"type": "object",
					"properties": {
						"label": {
							"type": "string",
							"maxLength": 20
						}
					}
				},
				"name": {
					"type": "string",
					"format": "uuid",
					"minLength": 1,
					"pattern": "^[a-z]+$"
				},
				"items": {
					"type": "array",
					"items": {
						"type": "string",
						"maxLength": 10
					},
					"minItems": 2
				}
			},
			"required": ["count"]
		}`),
	})
	require.NoError(t, err)
	member, ok := cfg.TextFormat.Structure.(*brtypes.OutputFormatStructureMemberJsonSchema)
	require.True(t, ok)
	require.JSONEq(t, `{
		"type": "object",
		"properties": {
			"count": {
				"type": "integer"
			},
			"metadata": {
				"type": "object",
				"properties": {
					"label": {
						"type": "string"
					}
				},
				"additionalProperties": false
			},
			"name": {
				"type": "string",
				"format": "uuid"
			},
			"items": {
				"type": "array",
				"items": {
					"type": "string"
				}
			}
		},
		"required": ["count"],
		"additionalProperties": false
	}`, *member.Value.Schema)
}

func TestEncodeOutputConfigRejectsUnsupportedAdditionalPropertiesSchema(t *testing.T) {
	_, err := encodeOutputConfig(&model.StructuredOutput{
		Schema: []byte(`{
			"type": "object",
			"additionalProperties": {
				"type": "string"
			}
		}`),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "additionalProperties")
	require.Contains(t, err.Error(), "$")
}
