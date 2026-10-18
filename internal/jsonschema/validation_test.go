// These tests check document identity and local references through the shared
// compiler used by MCP clients. No schema may load an external document.
package jsonschema

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCompileDocumentIdentity(t *testing.T) {
	for _, test := range []struct {
		name     string
		document string
		valid    string
		invalid  string
	}{
		{"embedded private address", `{"$id":"schema://example/wrapper","type":"object","properties":{"value":{"$id":"schema://goa-ai/contract.json","type":"object","properties":{"next":{"$ref":"#"}},"additionalProperties":false}},"required":["value"]}`, `{"value":{"next":{}}}`, `{"value":{"next":{"unexpected":true}}}`},
		{"explicit dialect", `{"$schema":"https://json-schema.org/draft/2020-12/schema#","$id":"schema://example/root#","type":"object","properties":{"next":{"$ref":"#"}},"additionalProperties":false}`, `{"next":{}}`, `{"next":{"unexpected":true}}`},
		{"absent identifier", `{"type":"object","properties":{"next":{"$ref":"#"}},"additionalProperties":false}`, `{"next":{}}`, `{"next":{"unexpected":true}}`},
		{"relative identifier", `{"$id":"relative/root","type":"object","properties":{"next":{"$ref":"#"}},"additionalProperties":false}`, `{"next":{}}`, `{"next":{"unexpected":true}}`},
		{"draft04 ignores dollar id", `{"$schema":"http://json-schema.org/draft-04/schema#","$id":"schema://other/ignored","$ref":"schema://goa-ai/contract.json#/definitions/value","definitions":{"value":{"type":"integer"}}}`, `42`, `"wrong"`},
		{"draft07 ignores reference siblings", `{"$schema":"http://json-schema.org/draft-07/schema#","$id":"schema://other/ignored","$ref":"schema://goa-ai/contract.json#/definitions/value","definitions":{"value":{"type":"integer"}}}`, `42`, `"wrong"`},
		{"nested boolean", `{"$id":"schema://example/root","type":"object","properties":{"denied":false}}`, `{}`, `{"denied":true}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := Compile([]byte(test.document))
			require.NoError(t, err)
			assert.NoError(t, Validate(compiled, []byte(test.valid)))
			assert.Error(t, Validate(compiled, []byte(test.invalid)))
		})
	}
}

func TestCompileBooleanDocuments(t *testing.T) {
	for _, document := range []string{"true", "false"} {
		compiled, err := Compile([]byte(document))
		require.NoError(t, err)
		assert.Equal(t, document == "true", Validate(compiled, []byte(`null`)) == nil)
	}
}

func TestCompileRejectsExternalDocuments(t *testing.T) {
	for _, document := range []string{
		`{"$ref":"https://example.com/schema"}`,
		`{"$id":"schema://example/root","$ref":"child"}`,
	} {
		_, err := Compile([]byte(document))
		assert.ErrorContains(t, err, "external schema reference")
	}
}

func TestCompileDoesNotReplaceDocumentWithBuiltin(t *testing.T) {
	compiled, err := Compile([]byte(`{"$id":"https://json-schema.org/draft/2020-12/schema","type":"integer"}`))
	require.NoError(t, err)
	assert.NoError(t, Validate(compiled, []byte(`42`)))
	assert.Error(t, Validate(compiled, []byte(`"wrong"`)))
}

func TestCompileValidatesSchemasWithReservedIdentifiers(t *testing.T) {
	for _, document := range []string{
		`{"$id":"https://json-schema.org/custom-result","type":"invalid-type"}`,
		`{"$id":"https://json-schema.org/custom-result","required":42}`,
		`{"$id":"https://json-schema.org/custom-result","$schema":null}`,
	} {
		_, err := Compile([]byte(document))
		assert.Error(t, err)
	}
}
