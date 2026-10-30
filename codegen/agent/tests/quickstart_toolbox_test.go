package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	gcodegen "goa.design/goa/v3/codegen"
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
			files := buildAndGenerate(t, c.design)
			toolbox := quickstartToolbox(t, fileContent(t, files, "AGENTS_QUICKSTART.md"))
			for _, w := range c.want {
				assert.Contains(t, toolbox, w)
			}
			for _, u := range c.unwanted {
				assert.NotContains(t, toolbox, u)
			}
			catalog := agentCatalogPath(t, files)
			assert.Contains(t, toolbox, "`"+catalog+"` (`input_binding`)",
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

// agentCatalogPath returns the path of the single generated per-agent
// tool_schemas.json catalog.
func agentCatalogPath(t *testing.T, files []*gcodegen.File) string {
	t.Helper()
	var paths []string
	for _, f := range files {
		if p := filepath.ToSlash(f.Path); strings.HasSuffix(p, "/specs/tool_schemas.json") {
			paths = append(paths, p)
		}
	}
	require.Len(t, paths, 1, "the design generates one agent catalog")
	return paths[0]
}
