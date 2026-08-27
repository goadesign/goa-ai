// Package model tests that raw JSON Schema compilation resolves only references
// contained in the caller's one in-memory schema document.
package model

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestRawSchemaLoaderRejectsExternalReferences(t *testing.T) {
	tests := []struct {
		name    string
		baseURI string
		ref     string
		wantURL string
	}{
		{
			name:    "relative",
			ref:     "other.json",
			wantURL: "schema://goa-ai/model/other.json",
		},
		{
			name:    "http",
			ref:     "http://example.com/external.json",
			wantURL: "http://example.com/external.json",
		},
		{
			name:    "https",
			ref:     "https://example.com/external.json",
			wantURL: "https://example.com/external.json",
		},
		{
			name:    "file",
			ref:     "file:///var/run/external-schema.json",
			wantURL: "file:///var/run/external-schema.json",
		},
		{
			name:    "data",
			ref:     "data:application/schema+json,%7B%22type%22:%22string%22%7D",
			wantURL: "data:application/schema+json,%7B%22type%22:%22string%22%7D",
		},
		{
			name:    "URN",
			ref:     "urn:example:external-schema",
			wantURL: "urn:example:external-schema",
		},
		{
			name:    "alternate base URI",
			baseURI: "https://schemas.example/root/schema.json",
			ref:     "child.json",
			wantURL: "https://schemas.example/root/child.json",
		},
		{
			name:    "fragment on external resource",
			ref:     "https://example.com/external.json#/$defs/value",
			wantURL: "https://example.com/external.json",
		},
	}
	for _, test := range tests {
		for _, location := range []string{"root", "nested"} {
			t.Run(test.name+"/"+location, func(t *testing.T) {
				schema := externalReferenceSchema(test.baseURI, test.ref, location == "nested")

				validator, err := compileJSONSchemaValidator(rawjson.Message(schema))

				require.Nil(t, validator)
				require.ErrorContains(t, err, "external schema reference")
				require.ErrorContains(t, err, test.wantURL)
			})
		}
	}
}

func TestRawSchemaLoaderAllowsInternalReferences(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		valid   string
		invalid string
	}{
		{
			name:    "root fragment",
			schema:  `{"$defs":{"value":{"type":"string"}},"$ref":"#/$defs/value"}`,
			valid:   `"ok"`,
			invalid: `1`,
		},
		{
			name: "nested fragment",
			schema: `{
				"$defs":{"value":{"type":"string"}},
				"type":"object",
				"properties":{"value":{"$ref":"#/$defs/value"}}
			}`,
			valid:   `{"value":"ok"}`,
			invalid: `{"value":1}`,
		},
		{
			name: "alternate base self fragment",
			schema: `{
				"$id":"https://schemas.example/root/schema.json",
				"$defs":{"value":{"type":"string"}},
				"$ref":"https://schemas.example/root/schema.json#/$defs/value"
			}`,
			valid:   `"ok"`,
			invalid: `1`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			validator, err := compileJSONSchemaValidator(rawjson.Message(test.schema))
			require.NoError(t, err)

			require.NoError(t, validator(rawjson.Message(test.valid)))
			require.ErrorContains(t, validator(rawjson.Message(test.invalid)), "validate JSON Schema")
		})
	}
}

// externalReferenceSchema puts one reference either at the schema root or in a
// nested property. The returned document lets each test prove that URI
// resolution cannot escape the in-memory root resource from either position.
func externalReferenceSchema(baseURI, ref string, nested bool) string {
	base := ""
	if baseURI != "" {
		base = fmt.Sprintf(`"$id":%q,`, baseURI)
	}
	if nested {
		return fmt.Sprintf(`{%s"type":"object","properties":{"value":{"$ref":%q}}}`, base, ref)
	}
	return fmt.Sprintf(`{%s"$ref":%q}`, base, ref)
}
