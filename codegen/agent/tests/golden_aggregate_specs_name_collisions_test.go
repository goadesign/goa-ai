// These tests verify that aggregate specifications use the package names Goa
// chose before generation started.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenAggregateSpecsNameCollisions(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.AggregateSpecsNameCollisions())
	content := renderedFileContent(t, files, "gen/alpha/agents/scribe/specs/specs.go")
	require.Contains(t, content, `policy2 "goa.design/goa-ai/gen/alpha/toolsets/policy"`)
	require.Contains(t, content, `tools2 "goa.design/goa-ai/gen/alpha/toolsets/tools"`)
	assertGoldenGo(t, "aggregate_specs_name_collisions", "specs.go.golden", content)
}
