package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
)

// TestQuickstartToolboxListsCompiledGuardrails checks that the toolbox
// reference lists each tool's compiled guardrails from the generator's tool
// data, and nothing for a tool that declares none.
func TestQuickstartToolboxListsCompiledGuardrails(t *testing.T) {
	cases := []struct {
		name     string
		design   func()
		want     []string
		unwanted []string
	}{
		{
			name:   "injected, bounded, continued",
			design: testscenarios.ContinuationInputCodecs(),
			want: []string{
				"Server-injected (never ask the model for these): `session_id`",
				"Bounded result: the service reports `returned` and `truncated`; continue with `lookup.continue_search`",
				"Bounded result: the service reports `returned` and `truncated`; continue with `lookup.continue_list`",
			},
		},
		{
			name:   "confirmation",
			design: testscenarios.ConfirmationDSL(),
			want:   []string{"Requires confirmation before it runs"},
		},
		{
			name:   "bound to a method with injected fields",
			design: testscenarios.InjectBoundMetaExample(),
			want:   []string{"Bound to service method `", "Server-injected (never ask the model for these): `"},
		},
		{
			name:   "plain tool",
			design: testscenarios.ToolSpecsMinimal(),
			unwanted: []string{
				"Bound to service method", "Server-injected", "Bounded result",
				"Requires confirmation", "Server data", "Ends the run", "Bookkeeping:", "Excluded from text-only runs",
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			toolbox := quickstartToolbox(t, fileContent(t, buildAndGenerate(t, c.design), "AGENTS_QUICKSTART.md"))
			for _, w := range c.want {
				assert.Contains(t, toolbox, w)
			}
			for _, u := range c.unwanted {
				assert.NotContains(t, toolbox, u)
			}
			assert.Regexp(t, "`gen/[^`]+/agents/[^`]+/specs/tool_schemas.json` \\(`input_binding`\\)", toolbox,
				"the toolbox points to the agent's generated input schema file")
		})
	}
}

// quickstartToolbox returns section 6 of a generated quickstart guide.
func quickstartToolbox(t *testing.T, content string) string {
	t.Helper()
	start := strings.Index(content, "## 6. Your Agent's Toolbox")
	end := strings.Index(content, "## 7. Agents Calling Agents")
	require.True(t, start >= 0 && end > start, "quickstart has a toolbox section")
	return content[start:end]
}
