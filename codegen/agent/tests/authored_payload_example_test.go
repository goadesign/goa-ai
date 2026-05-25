package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
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
