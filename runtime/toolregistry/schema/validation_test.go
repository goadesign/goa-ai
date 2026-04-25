package schema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateObjectSchema(t *testing.T) {
	schema := []byte(`{
		"type": "object",
		"required": ["name"],
		"additionalProperties": false,
		"properties": {
			"name": {"type": "string", "minLength": 2},
			"count": {"type": "integer", "minimum": 1}
		}
	}`)

	err := Validate(schema, map[string]any{"name": "ok", "count": 3}, "payload")
	require.NoError(t, err)

	err = Validate(schema, map[string]any{"name": "x", "extra": true}, "payload")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "name: string length 1 is less than minimum 2")
	assert.Contains(t, err.Error(), "extra: additional property not allowed")
}

func TestValidateOneOf(t *testing.T) {
	schema := []byte(`{
		"oneOf": [
			{"type": "string"},
			{"type": "integer"}
		]
	}`)

	require.NoError(t, Validate(schema, "ok", "result"))
	require.NoError(t, Validate(schema, 1, "result"))

	err := Validate(schema, true, "result")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "value must match exactly one schema in oneOf")
}
