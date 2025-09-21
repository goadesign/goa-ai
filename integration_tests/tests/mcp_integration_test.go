package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/integration_tests/framework"
)

func TestMCPProtocol(t *testing.T) {
	scenarios, err := framework.LoadScenarios("../scenarios/protocol.yaml")
	require.NoError(t, err)
	r := framework.NewRunner()
	require.NoError(t, r.Run(t, scenarios))
}

func TestMCPTools(t *testing.T) {
	scenarios, err := framework.LoadScenarios("../scenarios/tools.yaml")
	require.NoError(t, err)
	r := framework.NewRunner()
	require.NoError(t, r.Run(t, scenarios))
}

func TestMCPResources(t *testing.T) {
	scenarios, err := framework.LoadScenarios("../scenarios/resources.yaml")
	require.NoError(t, err)
	r := framework.NewRunner()
	require.NoError(t, r.Run(t, scenarios))
}

func TestMCPPrompts(t *testing.T) {
	scenarios, err := framework.LoadScenarios("../scenarios/prompts.yaml")
	require.NoError(t, err)
	r := framework.NewRunner()
	require.NoError(t, r.Run(t, scenarios))
}

func TestMCPNotifications(t *testing.T) {
	scenarios, err := framework.LoadScenarios("../scenarios/notifications.yaml")
	require.NoError(t, err)
	r := framework.NewRunner()
	require.NoError(t, r.Run(t, scenarios))
}
