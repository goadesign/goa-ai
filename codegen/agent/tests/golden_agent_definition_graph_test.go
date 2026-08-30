// Package tests verifies that generated caller definitions contain every agent
// contract needed to validate nested continuations before starting a workflow.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestGoldenAgentDefinitionGrandchild verifies that a root caller contains
// both its child and grandchild definitions without importing their agent
// packages.
func TestGoldenAgentDefinitionGrandchild(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.AgentDefinitionGrandchild())
	agent := renderedFileContent(t, files, "gen/entry/agents/root/agent.go")

	require.Equal(t, 1, strings.Count(agent, `"middle.child"`))
	require.Equal(t, 1, strings.Count(agent, `"leaf.grandchild"`))
	require.NotContains(t, agent, "/agents/child\"")
	require.NotContains(t, agent, "/agents/grandchild\"")
	assertGoldenGo(t, "agent_definition_graph", "grandchild_agent.go.golden", agent)
	runCompleteGeneratedPackageTest(t, files, "./gen/...")
}

// TestGoldenAgentDefinitionCycle verifies that a cyclic agent design produces
// a finite definition graph with each reachable agent emitted once.
func TestGoldenAgentDefinitionCycle(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.AgentDefinitionCycle())
	agent := renderedFileContent(t, files, "gen/alpha/agents/worker/agent.go")

	require.Equal(t, 1, strings.Count(agent, `"beta.worker"`))
	require.Equal(t, 1, strings.Count(agent, `"alpha.worker"`))
	assertGoldenGo(t, "agent_definition_graph", "cycle_agent.go.golden", agent)
	runCompleteGeneratedPackageTest(t, files, "./gen/...")
}
