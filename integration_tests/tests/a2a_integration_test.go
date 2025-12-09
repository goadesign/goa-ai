package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/integration_tests/framework"
)

func requireA2AServer(t *testing.T) {
	t.Helper()
	if !framework.SupportsA2AServer() {
		t.Skip("A2A integration server not available; set A2A_TEST_SERVER_URL or ensure the a2a_agent fixture exists")
	}
}

// TestA2AProtocol tests the core A2A protocol operations:
// - Agent card discovery
// - Task send (synchronous)
// - Task send subscribe (streaming)
// - Task get
// - Task cancel
// - Error handling
func TestA2AProtocol(t *testing.T) {
	requireA2AServer(t)
	scenarios, err := framework.LoadScenarios("../scenarios/a2a_protocol.yaml")
	require.NoError(t, err)
	for _, sc := range scenarios {
		scenario := sc
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			r := framework.NewA2ARunner()
			require.NoError(t, r.Run(t, []framework.Scenario{scenario}))
		})
	}
}

// TestA2ASkills tests skill invocation through the A2A protocol:
// - Echo skill
// - Add numbers skill
// - Process data skill with different formats
// - Validate input skill
// - Error handling for invalid arguments
// - Streaming skill invocation
func TestA2ASkills(t *testing.T) {
	requireA2AServer(t)
	scenarios, err := framework.LoadScenarios("../scenarios/a2a_skills.yaml")
	require.NoError(t, err)
	for _, sc := range scenarios {
		scenario := sc
		t.Run(scenario.Name, func(t *testing.T) {
			t.Parallel()
			r := framework.NewA2ARunner()
			require.NoError(t, r.Run(t, []framework.Scenario{scenario}))
		})
	}
}
