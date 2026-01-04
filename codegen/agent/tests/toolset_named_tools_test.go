package tests

import (
	"strings"
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

	// The generated code should compile - the toolset "tools" should be aliased
	// to "toolsspecs" to avoid conflicting with the runtime tools import
	require.Contains(t, specsContent, `tools "goa.design/goa-ai/runtime/agent/tools"`,
		"runtime tools import should have explicit alias")

	// The toolset import should be aliased to avoid conflict
	require.True(t,
		strings.Contains(specsContent, `toolsspecs "`) ||
			strings.Contains(specsContent, "Specs = append(Specs, toolsspecs.Specs...)"),
		"toolset named 'tools' should be aliased to 'toolsspecs' in import or usage")

	// Verify the generated code is syntactically valid by checking structure
	require.Contains(t, specsContent, "package specs")
	require.Contains(t, specsContent, "func Spec(")
}
