package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/codegen/testhelpers"
)

func TestAuthoredPayloadExamplePreservedInToolSpecs(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.AuthoredPayloadExample())
	specsSrc := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")

	require.Contains(t, specsSrc, `\"example\":{\"limit\":7,\"query\":\"battery alarms\"}`)
	require.Contains(t, specsSrc, `SchemaWithoutRootExample:tools.RawJSON("`)
	require.NotContains(t, specsSrc, `SchemaWithoutRootExample:tools.RawJSON("{\"$schema\":\"https://json-schema.org/draft/2020-12/schema\",\"example\"`)
	require.Contains(t, specsSrc, `ExampleJSON:tools.RawJSON("{\"limit\":7,\"query\":\"battery alarms\"}")`)
	require.NotContains(t, specsSrc, `ExampleInput:`)
}

func TestAuthoredPayloadExamplePreservedThroughPrepareInToolSpecs(t *testing.T) {
	files := testhelpers.BuildAndGenerate(t, testscenarios.AuthoredPayloadExampleThroughPrepare())
	specsSrc := fileContent(t, files, "gen/calc/toolsets/helpers/specs.go")

	require.Contains(t, specsSrc, `\"example\":{\"query\":{\"type\":\"by_name\",\"value\":{\"name\":\"compressor_1\"}}}`)
	require.Contains(t, specsSrc, `ExampleJSON:tools.RawJSON("{\"query\":{\"type\":\"by_name\",\"value\":{\"name\":\"compressor_1\"}}}")`)
	require.NotContains(t, specsSrc, `\"session_id\"`)
	require.NotContains(t, specsSrc, `ExampleInput:`)
}
