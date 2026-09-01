// Package tests verifies package-wide import planning for generated agent files.
package tests

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

func TestGoldenAgentPackageImportCollisions(t *testing.T) {
	files := buildCompleteGeneratedFiles(t, testscenarios.AgentPackageImportCollisions())
	agent := renderedFileContent(t, files, "gen/alpha/agents/scribe/agent.go")
	registry := renderedFileContent(t, files, "gen/alpha/agents/scribe/registry.go")

	require.Contains(t, agent, `agent3 "goa.design/goa-ai/runtime/agent"`)
	require.Contains(t, agent, "const AgentID agent3.Ident")
	require.Contains(t, agent, `specs "generated.local/gen/alpha/agents/scribe/specs"`)
	require.Contains(t, registry, `agent "generated.local/gen/alpha/agents/scribe/agent"`)
	require.Contains(t, registry, `agent2 "generated.local/gen/calc/toolsets/agent"`)
	require.Contains(t, agent, "specs.Specs(),")
	require.Contains(t, registry, "Specs:              agent2.Specs()")

	assertGoldenGo(t, "agent_package_import_collisions", "agent.go.golden", agent)
	assertGoldenGo(t, "agent_package_import_collisions", "registry.go.golden", registry)
	runCompleteGeneratedPackageTest(t, files, "./gen/alpha/agents/scribe/...")
}
