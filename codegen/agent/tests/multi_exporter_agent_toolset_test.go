// This file verifies that generated agent-as-tool code keeps the exporting
// agent selected by each consumer.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/testhelpers"
	. "goa.design/goa-ai/dsl"
	. "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
)

func TestMultiExporterAgentToolsetKeepsSelectedExporter(t *testing.T) {
	files := buildAndGenerate(t, multiExporterAgentToolsetDesign(true))

	alphaPath := "gen/alpha/agents/first/agenttools/shared/helpers.go"
	betaPath := "gen/beta/agents/second/agenttools/shared/helpers.go"
	alphaFile := testhelpers.FindFile(files, alphaPath)
	betaFile := testhelpers.FindFile(files, betaPath)
	assert.NotNil(t, alphaFile, alphaPath)
	assert.NotNil(t, betaFile, betaPath)
	if alphaFile == nil || betaFile == nil {
		return
	}

	alpha := fileContent(t, files, alphaPath)
	beta := fileContent(t, files, betaPath)
	assert.Contains(t, alpha, `"alpha.first"`)
	assert.NotContains(t, alpha, `"beta.second"`)
	assert.Contains(t, beta, `"beta.second"`)
	assert.NotContains(t, beta, `"alpha.first"`)

	definition := fileContent(t, files, "gen/consumer/agents/worker/agent.go")
	assert.Contains(t, definition, `"beta.second"`)
	assert.Contains(t, definition, `"beta.second.workflow"`)
	assert.NotContains(t, definition, `"alpha.first"`)
	assert.NotContains(t, definition, `"alpha.first.workflow"`)

	consumer := fileContent(t, files, "gen/consumer/agents/worker/shared_agenttools_client.go")
	assert.Contains(t, consumer, `"goa.design/goa-ai/gen/beta/agents/second/agenttools/shared"`)
	assert.NotContains(t, consumer, `"goa.design/goa-ai/gen/alpha/agents/first/agenttools/shared"`)

	aggregate := fileContent(t, files, "gen/consumer/agents/worker/specs/specs.go")
	assert.Contains(t, aggregate, `"beta.second"`)
	assert.NotContains(t, aggregate, `"alpha.first"`)
}

func TestPlainUseRejectsAmbiguousAgentExporters(t *testing.T) {
	testhelpers.SetupEvalRoots(t)
	require.True(t, eval.Execute(multiExporterAgentToolsetDesign(false), nil), eval.Context.Error())
	err := eval.RunDSL()
	require.ErrorContains(t, err, `toolset "shared" is exported by multiple agents`)
	require.ErrorContains(t, err, "AgentToolset")
}

func TestPlainUseLinksUniqueAgentExporter(t *testing.T) {
	files := buildAndGenerate(t, uniqueExporterAgentToolsetDesign())
	consumer := fileContent(t, files, "gen/consumer/agents/worker/shared_agenttools_client.go")
	require.Contains(t, consumer, `"goa.design/goa-ai/gen/alpha/agents/first/agenttools/shared"`)
}

// multiExporterAgentToolsetDesign declares two agents that export one shared
// definition. explicit selects the second exporter for the consumer.
func multiExporterAgentToolsetDesign(explicit bool) func() {
	return func() {
		API("multi_exporter", func() {})
		shared := Toolset("shared", func() {
			Tool("lookup", "Look up a value.", func() {
				Args(String)
				Return(String)
			})
		})
		Service("alpha", func() {
			Agent("first", "First provider.", func() {
				Export(shared)
			})
		})
		Service("beta", func() {
			Agent("second", "Second provider.", func() {
				Export(shared)
			})
		})
		Service("consumer", func() {
			Agent("worker", "Consumer.", func() {
				if explicit {
					Use(AgentToolset("beta", "second", "shared"))
					return
				}
				Use(shared)
			})
		})
	}
}

// uniqueExporterAgentToolsetDesign declares one exporter so plain Use can
// resolve it without coordinates.
func uniqueExporterAgentToolsetDesign() func() {
	return func() {
		API("unique_exporter", func() {})
		shared := Toolset("shared", func() {
			Tool("lookup", "Look up a value.", func() {
				Args(String)
				Return(String)
			})
		})
		Service("alpha", func() {
			Agent("first", "Provider.", func() {
				Export(shared)
			})
		})
		Service("consumer", func() {
			Agent("worker", "Consumer.", func() {
				Use(shared)
			})
		})
	}
}
