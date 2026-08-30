// Package tests exercises generated MCP servers through their public HTTP
// JSON-RPC endpoint.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/integration_tests/framework"
)

func TestMCPProtocol(t *testing.T) {
	runMCPScenarios(t, "../scenarios/protocol.yaml")
}

func TestMCPTools(t *testing.T) {
	runMCPScenarios(t, "../scenarios/tools.yaml")
}

func TestMCPResources(t *testing.T) {
	runMCPScenarios(t, "../scenarios/resources.yaml")
}

func TestMCPPrompts(t *testing.T) {
	runMCPScenarios(t, "../scenarios/prompts.yaml")
}

// runMCPScenarios starts a separately initialized generated server for each
// scenario so concurrent cases cannot share protocol state.
func runMCPScenarios(t *testing.T, path string) {
	t.Helper()
	if !framework.SupportsServer() {
		t.Skip("integration server not available; set TEST_SERVER_URL or restore the fixture")
	}
	scenarios, err := framework.LoadScenarios(path)
	require.NoError(t, err)
	for _, scenario := range scenarios {
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			runner := framework.NewRunner()
			require.NoError(t, runner.Run(t, []framework.Scenario{scenario}))
		})
	}
}
