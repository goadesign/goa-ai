package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// Verifies aggregated specs import and merge multiple per-toolset packages.
func TestGolden_MultiToolset_Aggregate(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.MultiToolset())
	agg := fileContent(t, files, "gen/alpha/agents/scribe/specs/specs.go")

	require.Contains(t, agg, "/agents/scribe/specs/ops")
	require.Contains(t, agg, "/agents/scribe/specs/math")
	require.Contains(t, agg, "Specs = append(Specs, ops.Specs...)")
	require.Contains(t, agg, "metadata = append(metadata, ops.Metadata()...)")
	require.Contains(t, agg, "Specs = append(Specs, math.Specs...)")
	require.Contains(t, agg, "metadata = append(metadata, math.Metadata()...)")
}
