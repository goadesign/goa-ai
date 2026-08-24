package openai

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestProjectStrictSchema(t *testing.T) {
	tests := []struct {
		name   string
		schema string
		want   string
	}{
		{
			name:   "empty schema projects to closed empty object",
			schema: "",
			want:   `{"type":"object","additionalProperties":false}`,
		},
		{
			name: "closes objects and strips schema annotations",
			schema: `{
				"$schema": "https://json-schema.org/draft/2020-12/schema",
				"type": "object",
				"properties": {
					"question": {"type": "string", "description": "User question", "example": "What?"}
				},
				"example": {"question": "What is the capital of Japan?"},
				"required": ["question"]
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"question": {"type": "string", "description": "User question"}
				},
				"required": ["question"]
			}`,
		},
		{
			name: "optional properties become required and nullable",
			schema: `{
				"type": "object",
				"properties": {
					"query": {"type": "string"},
					"limit": {"type": "integer", "default": 10},
					"level": {"type": "string", "enum": ["low", "high"]}
				},
				"required": ["query"]
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"query": {"type": "string"},
					"limit": {"anyOf": [{"type": "integer"}, {"type": "null"}]},
					"level": {"anyOf": [{"type": "string", "enum": ["low", "high"]}, {"type": "null"}]}
				},
				"required": ["level", "limit", "query"]
			}`,
		},
		{
			name: "closes nested objects and array items recursively",
			schema: `{
				"type": "object",
				"properties": {
					"filters": {
						"type": "array",
						"items": {
							"type": "object",
							"properties": {"field": {"type": "string"}},
							"required": ["field"]
						}
					}
				},
				"required": ["filters"]
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"filters": {
						"type": "array",
						"items": {
							"type": "object",
							"additionalProperties": false,
							"properties": {"field": {"type": "string"}},
							"required": ["field"]
						}
					}
				},
				"required": ["filters"]
			}`,
		},
		{
			name: "keeps supported constraints and drops unsupported formats",
			schema: `{
				"type": "object",
				"properties": {
					"id": {"type": "string", "format": "uuid", "pattern": ".+"},
					"count": {"type": "integer", "format": "int64", "minimum": 0},
					"code": {"type": "string", "format": "regexp", "pattern": "^[a-z]+$"}
				},
				"required": ["id", "count", "code"]
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"id": {"type": "string", "format": "uuid", "pattern": ".+"},
					"count": {"type": "integer", "minimum": 0},
					"code": {"type": "string", "pattern": "^[a-z]+$"}
				},
				"required": ["code", "count", "id"]
			}`,
		},
		{
			name: "optional reference properties become nullable unions",
			schema: `{
				"type": "object",
				"properties": {
					"draft": {"$ref": "#/$defs/Draft"}
				},
				"$defs": {
					"Draft": {
						"type": "object",
						"properties": {"title": {"type": "string"}},
						"required": ["title"]
					}
				}
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"draft": {"anyOf": [{"$ref": "#/$defs/Draft"}, {"type": "null"}]}
				},
				"required": ["draft"],
				"$defs": {
					"Draft": {
						"type": "object",
						"additionalProperties": false,
						"properties": {"title": {"type": "string"}},
						"required": ["title"]
					}
				}
			}`,
		},
		{
			name: "optional union properties gain a null branch",
			schema: `{
				"type": "object",
				"properties": {
					"value": {"anyOf": [{"type": "string"}, {"type": "integer"}]}
				}
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"value": {"anyOf": [
						{"anyOf": [{"type": "string"}, {"type": "integer"}]},
						{"type": "null"}
					]}
				},
				"required": ["value"]
			}`,
		},
		{
			name: "disjoint oneOf types fold into anyOf and optionals gain a null branch",
			schema: `{
				"type": "object",
				"properties": {
					"choice": {"oneOf": [{"type": "string"}, {"type": "integer"}]},
					"pick": {"oneOf": [{"type": "string"}, {"type": "boolean"}]}
				},
				"required": ["choice"]
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"choice": {"anyOf": [{"type": "string"}, {"type": "integer"}]},
					"pick": {"anyOf": [
						{"anyOf": [{"type": "string"}, {"type": "boolean"}]},
						{"type": "null"}
					]}
				},
				"required": ["choice", "pick"]
			}`,
		},
		{
			name: "generated discriminator union folds into anyOf",
			schema: `{
				"type": "object",
				"properties": {
					"choice": {
						"oneOf": [
							{
								"type": "object",
								"properties": {
									"type": {"type": "string", "enum": ["left"]},
									"value": {"type": "string"}
								},
								"required": ["type", "value"]
							},
							{
								"type": "object",
								"properties": {
									"type": {"type": "string", "enum": ["right"]},
									"value": {"type": "integer"}
								},
								"required": ["type", "value"]
							}
						]
					}
				},
				"required": ["choice"]
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"choice": {
						"anyOf": [
							{
								"type": "object",
								"additionalProperties": false,
								"properties": {
									"type": {"type": "string", "enum": ["left"]},
									"value": {"type": "string"}
								},
								"required": ["type", "value"]
							},
							{
								"type": "object",
								"additionalProperties": false,
								"properties": {
									"type": {"type": "string", "enum": ["right"]},
									"value": {"type": "integer"}
								},
								"required": ["type", "value"]
							}
						]
					}
				},
				"required": ["choice"]
			}`,
		},
		{
			name: "optional constrained properties preserve their complete constraint",
			schema: `{
				"type": "object",
				"properties": {
					"constant": {"type": "string", "const": "fixed"}
				}
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"constant": {"anyOf": [{"type": "string", "const": "fixed"}, {"type": "null"}]}
				},
				"required": ["constant"]
			}`,
		},
		{
			name: "property names that look like keywords stay untouched",
			schema: `{
				"type": "object",
				"properties": {
					"default": {"type": "string"},
					"example": {"type": "string"}
				},
				"required": ["default", "example"]
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"default": {"type": "string"},
					"example": {"type": "string"}
				},
				"required": ["default", "example"]
			}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			projected, err := projectStrictSchema(rawjson.Message(tt.schema))
			require.NoError(t, err)
			got, err := json.Marshal(projected)
			require.NoError(t, err)
			assert.JSONEq(t, tt.want, string(got))
		})
	}
}

func TestProjectStrictSchemaPreservesLargeIntegers(t *testing.T) {
	projected, err := projectStrictSchema(rawjson.Message(`{
		"type":"object",
		"properties":{"reading":{"type":"integer","const":9007199254740993}},
		"required":["reading"]
	}`))
	require.NoError(t, err)

	properties := projected["properties"].(map[string]any)
	reading := properties["reading"].(map[string]any)
	require.Equal(t, json.Number("9007199254740993"), reading["const"])
}

func TestProjectStrictSchemaRejectsUnrepresentableContracts(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		wantErr string
	}{
		{
			name:    "non-object root",
			schema:  `{"type": "string"}`,
			wantErr: "schema root must declare type",
		},
		{
			name: "map-style additionalProperties",
			schema: `{
				"type": "object",
				"properties": {
					"labels": {"type": "object", "additionalProperties": {"type": "string"}}
				},
				"required": ["labels"]
			}`,
			wantErr: "map-style object",
		},
		{
			name:    "open object",
			schema:  `{"type": "object", "additionalProperties": true}`,
			wantErr: "open object",
		},
		{
			name: "root union",
			schema: `{
				"type": "object",
				"oneOf": [
					{"type": "object", "properties": {"left": {"type": "string"}}, "required": ["left"]},
					{"type": "object", "properties": {"right": {"type": "integer"}}, "required": ["right"]}
				]
			}`,
			wantErr: "oneOf branches that may overlap",
		},
		{
			name: "unsupported composition",
			schema: `{
				"type": "object",
				"properties": {"value": {"type": "string", "allOf": [{"pattern": ".+"}]}},
				"required": ["value"]
			}`,
			wantErr: `unsupported OpenAI strict-mode keyword "allOf"`,
		},
		{
			name: "overlapping oneOf",
			schema: `{
				"type": "object",
				"properties": {
					"value": {
						"oneOf": [
							{"type": "string", "minLength": 1},
							{"type": "string", "pattern": "^[a-z]+$"}
						]
					}
				},
				"required": ["value"]
			}`,
			wantErr: "oneOf branches that may overlap",
		},
		{
			name: "integer overlaps number",
			schema: `{
				"type": "object",
				"properties": {
					"value": {
						"oneOf": [
							{"type": "number"},
							{"type": "integer"}
						]
					}
				},
				"required": ["value"]
			}`,
			wantErr: "oneOf branches that may overlap",
		},
		{
			name: "combined oneOf and anyOf",
			schema: `{
				"type": "object",
				"properties": {
					"value": {
						"oneOf": [{"type": "string"}, {"type": "integer"}],
						"anyOf": [{"type": "string"}, {"type": "boolean"}]
					}
				},
				"required": ["value"]
			}`,
			wantErr: "combines oneOf with anyOf",
		},
		{
			name: "unsupported string keyword",
			schema: `{
				"type": "object",
				"properties": {"value": {"type": "string", "contentEncoding": "base64"}},
				"required": ["value"]
			}`,
			wantErr: `unsupported OpenAI strict-mode keyword "contentEncoding"`,
		},
		{
			name:    "invalid JSON",
			schema:  `{"type":`,
			wantErr: "invalid JSON schema",
		},
		{
			name: "conflicting branch null projection",
			schema: `{
				"type": "object",
				"anyOf": [
					{
						"type": "object",
						"properties": {"value": {"type": "string"}}
					},
					{
						"type": "object",
						"properties": {"value": {"type": ["string", "null"]}},
						"required": ["value"]
					}
				]
			}`,
			wantErr: "conflicting null handling",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := projectStrictSchema(rawjson.Message(tt.schema))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestProjectStrictSchemaEnforcesOpenAIResourceLimits(t *testing.T) {
	t.Run("properties", func(t *testing.T) {
		properties := make(map[string]any, strictSchemaMaxProps+1)
		for index := 0; index <= strictSchemaMaxProps; index++ {
			properties[fmt.Sprintf("field_%d", index)] = map[string]any{"type": "string"}
		}
		schema, err := json.Marshal(map[string]any{
			"type":       "object",
			"properties": properties,
		})
		require.NoError(t, err)

		_, err = projectStrictSchema(schema)

		require.ErrorContains(t, err, fmt.Sprintf("maximum of %d object properties", strictSchemaMaxProps))
	})

	t.Run("enum values", func(t *testing.T) {
		values := make([]any, strictSchemaMaxEnums+1)
		for index := range values {
			values[index] = fmt.Sprintf("value_%d", index)
		}
		schema, err := json.Marshal(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string", "enum": values},
			},
		})
		require.NoError(t, err)

		_, err = projectStrictSchema(schema)

		require.ErrorContains(t, err, fmt.Sprintf("maximum of %d enum values", strictSchemaMaxEnums))
	})

	t.Run("enum string limits count characters", func(t *testing.T) {
		values := make([]any, 251)
		for index := range values {
			values[index] = strings.Repeat("界", 50) + fmt.Sprintf("_%d", index)
		}
		schema, err := json.Marshal(map[string]any{
			"type": "object",
			"properties": map[string]any{
				"value": map[string]any{"type": "string", "enum": values},
			},
		})
		require.NoError(t, err)

		projection, err := projectStrictSchema(schema)

		require.NoError(t, err)
		require.NotNil(t, projection)
	})

	t.Run("nesting depth", func(t *testing.T) {
		nested := map[string]any{"type": "string"}
		for depth := 0; depth < strictSchemaMaxDepth; depth++ {
			nested = map[string]any{
				"type":       "object",
				"properties": map[string]any{"child": nested},
			}
		}
		schema, err := json.Marshal(nested)
		require.NoError(t, err)

		_, err = projectStrictSchema(schema)

		require.ErrorContains(t, err, fmt.Sprintf("maximum nesting depth %d", strictSchemaMaxDepth))
	})
}

func TestCompileStrictSchemaSpecializesFineTunedModels(t *testing.T) {
	tests := []struct {
		name     string
		property string
		schema   string
	}{
		{
			name:     "string length",
			property: "minLength",
			schema:   `{"type":"string","minLength":1}`,
		},
		{
			name:     "string pattern",
			property: "pattern",
			schema:   `{"type":"string","pattern":".+"}`,
		},
		{
			name:     "string format",
			property: "format",
			schema:   `{"type":"string","format":"uuid"}`,
		},
		{
			name:     "number bound",
			property: "minimum",
			schema:   `{"type":"number","minimum":0}`,
		},
		{
			name:     "number multiple",
			property: "multipleOf",
			schema:   `{"type":"number","multipleOf":2}`,
		},
		{
			name:     "array minimum",
			property: "minItems",
			schema:   `{"type":"array","items":{"type":"string"},"minItems":1}`,
		},
		{
			name:     "array maximum",
			property: "maxItems",
			schema:   `{"type":"array","items":{"type":"string"},"maxItems":2}`,
		},
		{
			name:     "object pattern properties",
			property: "patternProperties",
			schema:   `{"type":"object","patternProperties":{"^item_":{"type":"string"}}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			schema := rawjson.Message(fmt.Sprintf(
				`{"type":"object","properties":{"value":%s},"required":["value"]}`,
				test.schema,
			))

			baseProjection, err := compileStrictSchemaForModel(schema, "gpt-5.6")
			require.NoError(t, err)
			require.NotNil(t, baseProjection)

			fineTunedProjection, err := compileStrictSchemaForModel(schema, "ft:gpt-5.6:org:model")
			require.Nil(t, fineTunedProjection)
			require.ErrorContains(t, err, fmt.Sprintf("keyword %q", test.property))
			require.ErrorContains(t, err, "fine-tuned models do not support")
		})
	}
}
