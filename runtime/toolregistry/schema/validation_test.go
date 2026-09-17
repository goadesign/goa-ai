// Package schema tests validation of registry-provided JSON contracts.
// These tests preserve exact values and references within one document while
// rejecting values and external files outside the declared contract.
package schema

import (
	"net/url"
	"os"
	"path/filepath"
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
	assert.Contains(t, err.Error(), "validate payload")
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
	assert.Contains(t, err.Error(), "validate result")
}

func TestValidateUsesFullJSONSchemaComposition(t *testing.T) {
	schema := []byte(`{
		"$schema": "https://json-schema.org/draft/2020-12/schema",
		"$defs": {
			"tag": {"type": "string", "const": "fixed"}
		},
		"allOf": [{
			"type": "object",
			"required": ["tag", "meta"],
			"additionalProperties": false,
			"properties": {
				"tag": {"$ref": "#/$defs/tag"},
				"meta": {
					"type": "object",
					"additionalProperties": {"type": "integer"}
				}
			}
		}]
	}`)

	require.NoError(t, Validate(schema, map[string]any{
		"tag":  "fixed",
		"meta": map[string]any{"count": 3},
	}, "payload"))

	err := Validate(schema, map[string]any{
		"tag":  "other",
		"meta": map[string]any{"count": "three"},
	}, "payload")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validate payload")
}

func TestValidateSupportsNonStringEnum(t *testing.T) {
	schema := []byte(`{
		"type": "integer",
		"enum": [1, 2]
	}`)

	require.NoError(t, Validate(schema, 1, "result"))

	err := Validate(schema, 3, "result")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "validate result")
}

func TestValidatePreservesLargeIntegerConstraints(t *testing.T) {
	t.Parallel()

	contract := []byte(`{"type":"integer","const":9007199254740992}`)
	require.NoError(t, Validate(contract, int64(9007199254740992), "payload"))
	require.Error(t, Validate(contract, int64(9007199254740993), "payload"))
}

func TestValidateRejectsExternalSchemaFiles(t *testing.T) {
	t.Parallel()

	filename := filepath.Join(t.TempDir(), "external.json")
	require.NoError(t, os.WriteFile(filename, []byte(`{"type":"string"}`), 0o600))
	ref := (&url.URL{Scheme: "file", Path: filename}).String()
	contract := []byte(`{"$ref":"` + ref + `"}`)
	require.ErrorContains(t, Validate(contract, "value", "payload"), "external schema reference")
}

func TestValidateAllowsReferencesWithinOneDocument(t *testing.T) {
	t.Parallel()

	for _, contract := range []string{
		`{"$defs":{"name":{"type":"string","minLength":2}},"$ref":"#/$defs/name"}`,
		`{"$id":"https://registry.example.invalid/tool.json","$defs":{"name":{"type":"string","minLength":2}},"$ref":"#/$defs/name"}`,
	} {
		require.NoError(t, Validate([]byte(contract), "valid", "payload"))
		require.Error(t, Validate([]byte(contract), "x", "payload"))
		require.Error(t, Validate([]byte(contract), 1, "payload"))
	}
}
