package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Minimal tool specs for an agent with one toolset and one tool with simple args/return.
func TestGolden_ToolSpecs_Minimal(t *testing.T) {
	design := testscenarios.ToolSpecsMinimal()
	files := buildAndGenerate(t, design)
	// Compare the three per-toolset files under specs/<toolset>
	codecs := fileContent(t, files, "gen/calc/agents/scribe/specs/helpers/codecs.go")
	specs := fileContent(t, files, "gen/calc/agents/scribe/specs/helpers/specs.go")

	require.Contains(t, codecs, "goa.design/goa-ai/gen/calc")
	require.Contains(t, codecs, "JSONCodec[*calc.SummarizePayload]")
	require.Contains(t, specs, "\"calc.helpers.summarize_doc\"")
	require.Contains(t, specs, "summarizeDocPayloadSchema")
	require.Contains(t, specs, "summarizeDocResultSchema")
}
