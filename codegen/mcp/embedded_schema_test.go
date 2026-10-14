// These tests check documentation built from the same protocol declarations as
// generated MCP encoders, including required form and URL fields.
package codegen

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	frameworkschema "goa.design/goa-ai/codegen/jsonschema"
	contract "goa.design/goa-ai/internal/jsonschema"
	"goa.design/goa/v3/expr"
	genopenapi "goa.design/goa/v3/http/codegen/openapi/v2"
)

func TestMCPEmbeddedOpenAPISchema(t *testing.T) {
	builder := newMCPExprBuilder(&expr.ServiceExpr{Name: "questions"}, nil)
	result := builder.buildInputRequiredResultType()
	request := expr.AsMap(result.Find("inputRequests").Type).ElemType
	api := &expr.APIExpr{RandomizerFactory: expr.NewDeterministicRandomizerFactory()}
	named := request.Type.(expr.UserType)
	schema := genopenapi.BuildAttributeSchema(api, request, expr.NewExampleGenerator(api.RandomizerFactory).At(expr.UserTypeExampleIdentity(named)))
	encoded, err := schema.JSON()
	require.NoError(t, err)
	t.Logf("protocol schema: %s", encoded)
	frameworkValue, frameworkErr := frameworkschema.Build(api, request, expr.UserTypeExampleIdentity(named))
	require.NoError(t, frameworkErr)
	assert.NotContains(t, string(encoded), `"extensions":`)
	var document map[string]any
	require.NoError(t, json.Unmarshal(encoded, &document))
	assertNoStoredExtensionProperty(t, document)
	compiled, err := contract.Compile(encoded)
	require.NoError(t, err)
	frameworkCompiled, err := contract.Compile(frameworkValue)
	require.NoError(t, err)
	for _, tc := range []struct {
		name, input string
		valid       bool
	}{
		{"form", `{"method":"elicitation/create","params":{"mode":"form","message":"Choose","requestedSchema":{"type":"object","properties":{}},"extra":{"integer":9007199254740993123456789}}}`, true},
		{"url", `{"method":"elicitation/create","params":{"mode":"url","message":"Consent","url":"https://example.com","extra":null}}`, true},
		{"missing mode", `{"method":"elicitation/create","params":{"message":"Consent","url":"https://example.com"}}`, false},
		{"missing message", `{"method":"elicitation/create","params":{"mode":"url","url":"https://example.com"}}`, false},
		{"missing schema", `{"method":"elicitation/create","params":{"mode":"form","message":"Choose"}}`, false},
		{"missing URL", `{"method":"elicitation/create","params":{"mode":"url","message":"Consent"}}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := contract.Validate(compiled, []byte(tc.input))
			assert.Equal(t, tc.valid, err == nil, "OpenAPI: %v", err)
			err = contract.Validate(frameworkCompiled, []byte(tc.input))
			assert.Equal(t, tc.valid, err == nil, "framework: %v", err)
		})
	}
}

func assertNoStoredExtensionProperty(t *testing.T, value any) {
	t.Helper()
	switch node := value.(type) {
	case map[string]any:
		if properties, ok := node["properties"].(map[string]any); ok {
			assert.NotContains(t, properties, "extensions")
		}
		for _, child := range node {
			assertNoStoredExtensionProperty(t, child)
		}
	case []any:
		for _, child := range node {
			assertNoStoredExtensionProperty(t, child)
		}
	}
}
