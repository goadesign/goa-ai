package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/integration_tests/framework"
)

func requireServer(t *testing.T) {
	t.Helper()
	if !framework.SupportsServer() {
		t.Skip("integration server not available; set TEST_SERVER_URL or restore the example directory")
	}
}

func TestMCPProtocol(t *testing.T) {
	requireServer(t)
	scenarios, err := framework.LoadScenarios("../scenarios/protocol.yaml")
	require.NoError(t, err)
	for _, sc := range scenarios {
		scenario := sc
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			r := framework.NewRunner()
			require.NoError(t, r.Run(t, []framework.Scenario{scenario}))
		})
	}
}

func TestMCPTools(t *testing.T) {
	requireServer(t)
	scenarios, err := framework.LoadScenarios("../scenarios/tools.yaml")
	require.NoError(t, err)
	for _, sc := range scenarios {
		scenario := sc
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			r := framework.NewRunner()
			require.NoError(t, r.Run(t, []framework.Scenario{scenario}))
		})
	}
}

func TestMCPResources(t *testing.T) {
	requireServer(t)
	scenarios, err := framework.LoadScenarios("../scenarios/resources.yaml")
	require.NoError(t, err)
	for _, sc := range scenarios {
		scenario := sc
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			r := framework.NewRunner()
			require.NoError(t, r.Run(t, []framework.Scenario{scenario}))
		})
	}
}

func TestMCPPrompts(t *testing.T) {
	requireServer(t)
	scenarios, err := framework.LoadScenarios("../scenarios/prompts.yaml")
	require.NoError(t, err)
	for _, sc := range scenarios {
		scenario := sc
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			r := framework.NewRunner()
			require.NoError(t, r.Run(t, []framework.Scenario{scenario}))
		})
	}
}
