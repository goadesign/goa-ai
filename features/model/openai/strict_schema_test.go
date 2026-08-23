package openai

import (
	"encoding/json"
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
					"limit": {"type": ["integer", "null"]},
					"level": {"type": ["string", "null"], "enum": ["low", "high", null]}
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
					"id": {"type": "string", "format": "uuid", "minLength": 1},
					"count": {"type": "integer", "format": "int64", "minimum": 0},
					"code": {"type": "string", "format": "regexp", "pattern": "^[a-z]+$"}
				},
				"required": ["id", "count", "code"]
			}`,
			want: `{
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"id": {"type": "string", "format": "uuid", "minLength": 1},
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
					"value": {"anyOf": [{"type": "string"}, {"type": "integer"}, {"type": "null"}]}
				},
				"required": ["value"]
			}`,
		},
		{
			name: "oneOf unions fold into anyOf and optionals gain a null branch",
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
					"pick": {"anyOf": [{"type": "string"}, {"type": "boolean"}, {"type": "null"}]}
				},
				"required": ["choice", "pick"]
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
			name:    "invalid JSON",
			schema:  `{"type":`,
			wantErr: "invalid JSON schema",
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
