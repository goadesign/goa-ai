// This file checks the pairing between semantic tool data and the final names
// selected for the generated tool package.
package codegen

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestToolRenderDataRejectsMissingGeneratedEntry verifies that file generation
// reports the exact tool whose generated names are unavailable.
func TestToolRenderDataRejectsMissingGeneratedEntry(t *testing.T) {
	toolset := &ToolsetData{
		QualifiedName: "service.ops",
		Tools: []*ToolData{
			{QualifiedName: "ops.lookup"},
		},
		specs: &toolSpecsData{},
	}

	_, err := buildToolRenderData(toolset)

	require.EqualError(t, err, `agent codegen: generated names for tool "ops.lookup" are missing from toolset "service.ops"`)
}
