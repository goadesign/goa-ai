package tests

// This file checks that a shared tool contract stays independent of the
// service selected to hold its generated Go package.

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	"goa.design/goa-ai/testutil"
)

func TestGoldenSharedToolsetConsumers(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.SharedToolsetConsumers())

	canonical := fileContent(t, files, "gen/alpha/toolsets/shared/specs.go")
	require.NotContains(t, canonical, "Service:")
	require.NotContains(t, canonical, "Toolset:")
	assertGoldenGo(t, "shared_toolset_consumers", "canonical_specs.go.golden", canonical)

	alphaSpecs := fileContent(t, files, "gen/alpha/agents/alpha_worker/specs/specs.go")
	assertGoldenGo(t, "shared_toolset_consumers", "alpha_specs.go.golden", alphaSpecs)
	alphaRegistry := fileContent(t, files, "gen/alpha/agents/alpha_worker/registry.go")
	assertGoldenGo(t, "shared_toolset_consumers", "alpha_registry.go.golden", alphaRegistry)

	betaSpecs := fileContent(t, files, "gen/beta/agents/beta_worker/specs/specs.go")
	assertGoldenGo(t, "shared_toolset_consumers", "beta_specs.go.golden", betaSpecs)
	betaRegistry := fileContent(t, files, "gen/beta/agents/beta_worker/registry.go")
	assertGoldenGo(t, "shared_toolset_consumers", "beta_registry.go.golden", betaRegistry)

	betaCatalog := fileContent(t, files, "gen/beta/agents/beta_worker/specs/tool_schemas.json")
	require.Contains(t, betaCatalog, `"service": "beta"`)
	require.Contains(t, betaCatalog, `"toolset": "beta.shared"`)
	require.NotContains(t, betaCatalog, `"service": "alpha"`)
	testutil.AssertJSON(
		t,
		filepath.Join("testdata", "golden", "shared_toolset_consumers", "beta_tool_schemas.json.golden"),
		[]byte(betaCatalog),
	)
}
