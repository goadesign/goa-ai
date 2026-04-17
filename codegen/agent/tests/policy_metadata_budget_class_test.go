package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
)

// TestGeneratedPolicyMetadataIncludesBudgetClass verifies that generated
// toolset and agent metadata expose whether tools consume the run-level
// retrieval budget.
func TestGeneratedPolicyMetadataIncludesBudgetClass(t *testing.T) {
	files := buildAndGenerate(t, func() {
		goadsl.API("alpha", func() {})

		goadsl.Service("alpha", func() {
			Agent("scribe", "Doc helper", func() {
				Use("tasks", func() {
					Tool("read", "Read task state", func() {})
					Tool("complete", "Complete task", func() {
						TerminalRun()
					})
				})
			})
		})
	})

	toolsetSpecs := fileContent(t, files, "gen/alpha/toolsets/tasks/specs.go")
	require.Contains(t, toolsetSpecs, "BudgetClass: policy.ToolBudgetClassBudgeted")
	require.Contains(t, toolsetSpecs, "BudgetClass: policy.ToolBudgetClassBookkeeping")

	agentSpecs := fileContent(t, files, "gen/alpha/agents/scribe/specs/specs.go")
	require.Contains(t, agentSpecs, "BudgetClass: policy.ToolBudgetClassBudgeted")
	require.Contains(t, agentSpecs, "BudgetClass: policy.ToolBudgetClassBookkeeping")
}
