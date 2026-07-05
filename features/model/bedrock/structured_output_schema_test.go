package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestNormalizePreservesPropertiesNamedLikeKeywords guards against treating
// property names as schema keywords: a property named "default" (or "title",
// "pattern", …) lives under a properties map and must survive normalization,
// while actual unsupported keywords at schema level are stripped.
func TestNormalizePreservesPropertiesNamedLikeKeywords(t *testing.T) {
	t.Parallel()

	schema := []byte(`{
		"$defs": {
			"Param": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"default": {"type": "string", "minLength": 1, "description": "Textual default."},
					"title": {"type": "string"},
					"name": {"type": "string", "pattern": "^[a-z]+$"}
				},
				"required": ["name"]
			}
		},
		"type": "object",
		"title": "Root",
		"properties": {
			"parameters": {"type": "array", "items": {"$ref": "#/$defs/Param"}, "default": []}
		},
		"required": ["parameters"]
	}`)

	normalized, err := normalizeStructuredOutputSchemaForBedrock(schema)
	require.NoError(t, err)

	var doc map[string]any
	require.NoError(t, json.Unmarshal(normalized, &doc))

	// Schema-level unsupported keywords are gone.
	_, hasTitle := doc["title"]
	require.False(t, hasTitle, "root title keyword must be stripped")
	rootProps := doc["properties"].(map[string]any)
	paramsSchema := rootProps["parameters"].(map[string]any)
	_, hasDefaultKeyword := paramsSchema["default"]
	require.False(t, hasDefaultKeyword, "default keyword on a schema must be stripped")

	// Property names that collide with keywords survive.
	defs := doc["$defs"].(map[string]any)
	param := defs["Param"].(map[string]any)
	props := param["properties"].(map[string]any)
	require.Contains(t, props, "default", "property named default must survive")
	require.Contains(t, props, "title", "property named title must survive")
	require.Contains(t, props, "name")

	// Keyword stripping still applies inside the surviving property schemas.
	defaultProp := props["default"].(map[string]any)
	_, hasMinLength := defaultProp["minLength"]
	require.False(t, hasMinLength, "minLength inside a property schema must be stripped")
	require.Equal(t, "string", defaultProp["type"])
}
