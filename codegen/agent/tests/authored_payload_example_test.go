package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

func TestAuthoredPayloadExamplePreservedInToolSpecs(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.AuthoredPayloadExample())
	specsSrc := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")

	require.Contains(t, specsSrc, `\"example\":{\"limit\":7,\"query\":\"battery alarms\"}`)
	require.Contains(t, specsSrc, `SchemaWithoutRootExample:[]byte("{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"type\":\"object\"`)
	require.NotContains(t, specsSrc, `SchemaWithoutRootExample:[]byte("{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"type\":\"object\",\"example\"`)
	require.Contains(t, specsSrc, `ExampleJSON:[]byte("{\"limit\":7,\"query\":\"battery alarms\"}")`)
	require.Contains(t, specsSrc, `ExampleInput:map[string]any{"limit": 7, "query": "battery alarms"}`)
}

func TestAuthoredPayloadExamplePreservedThroughPrepareInToolSpecs(t *testing.T) {
	genpkg, roots := testhelpers.RunDesign(t, testscenarios.AuthoredPayloadExampleThroughPrepare())
	require.NoError(t, codegen.Prepare(genpkg, roots))
	files, err := codegen.Generate(genpkg, roots, nil)
	require.NoError(t, err)
	specsSrc := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")

	require.Contains(t, specsSrc, `\"example\":{\"query\":{\"type\":\"by_name\",\"value\":{\"name\":\"compressor_1\"}}}`)
	require.Contains(t, specsSrc, `ExampleJSON:[]byte("{\"query\":{\"type\":\"by_name\",\"value\":{\"name\":\"compressor_1\"}}}")`)
	require.Contains(t, specsSrc, `ExampleInput:map[string]any{"query": map[string]any{"type": "by_name", "value": map[string]any{"name": "compressor_1"}}}`)
}
