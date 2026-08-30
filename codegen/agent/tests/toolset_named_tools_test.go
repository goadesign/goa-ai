package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestToolsetNamedTools verifies that a toolset named "tools" doesn't conflict
// with the runtime tools package import in the generated specs aggregator.
func TestToolsetNamedTools(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolsetNamedTools())

	// Find the aggregated specs.go file
	specsContent := fileContent(t, files, "gen/alpha/agents/helper/specs/specs.go")
	require.NotEmpty(t, specsContent, "specs.go should be generated")

	// The runtime package keeps its public qualifier.
	require.Contains(t, specsContent, `tools "goa.design/goa-ai/runtime/agent/tools"`,
		"runtime tools import should have explicit alias")

	// Goa moves the generated toolset import away from the fixed runtime name.
	require.Contains(t, specsContent, `tools2 "goa.design/goa-ai/gen/alpha/toolsets/tools"`)
	require.Contains(t, specsContent, "specs = append(specs, tools2.Specs()...)")

	// Verify the generated code is syntactically valid by checking structure
	require.Contains(t, specsContent, "package specs")
	require.Contains(t, specsContent, "func Spec(")
	require.NotContains(t, specsContent, "func AdvertisedSpecs(")
	require.NotContains(t, specsContent, "func PayloadSchema(")
	require.NotContains(t, specsContent, "func ResultSchema(")
}
