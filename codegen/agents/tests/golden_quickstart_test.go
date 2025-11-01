package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agents/tests/testscenarios"
	. "goa.design/goa-ai/dsl"
	goadsl "goa.design/goa/v3/dsl"
)

func TestQuickstart_Renders_Minimal(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())

	content := fileContent(t, files, "AGENTS_QUICKSTART.md")
	require.NotEmpty(t, content)

	// Sanity markers
	require.Contains(t, content, "Welcome to Your Goa-AI Agents!")
	require.Contains(t, content, "calc.scribe")
	require.Contains(t, content, "calc.helpers")

	// Ensure the example code does not contain raw Go template braces that break parsing
	require.NotContains(t, content, "[]planner.AgentMessage{{")
	require.Contains(t, content, "[]planner.AgentMessage{ {Role: \"user\", Content: \"Hi there!\"} }")

	// Basic structural sanity (no golden for full file to avoid brittleness on whitespace)
	require.Contains(t, content, "## 2. 🚀 The 3-Step Liftoff")
}

func TestQuickstart_Disabled(t *testing.T) {
	design := func() {
		goadsl.API("calc", func() {
			DisableAgentDocs()
		})
		goadsl.Service("calc", func() {
			Agent("scribe", "Doc helper", func() {
				Uses(func() {
					Toolset("helpers", func() {})
				})
			})
		})
	}
	files := buildAndGenerate(t, design)

	// Ensure quickstart is not emitted
	for _, f := range files {
		require.NotEqual(t, "AGENTS_QUICKSTART.md", f.Path, "AGENTS_QUICKSTART.md should not be generated when DisableAgentDocs is set")
	}
}
