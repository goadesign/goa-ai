package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent/tests/testscenarios"
	. "goa.design/goa-ai/dsl"
	"goa.design/goa-ai/testutil"
	goadsl "goa.design/goa/v3/dsl"
)

// Validates the Quickstart README via a golden for the stable header section
// and a few structural markers for the rest to avoid brittleness.
func TestQuickstart_Renders_Minimal(t *testing.T) {
	files := buildAndGenerate(t, testscenarios.ToolSpecsMinimal())

	content := fileContent(t, files, "AGENTS_QUICKSTART.md")
	require.NotEmpty(t, content)

	// Compare the header + services overview against a golden with normalization.
	// Split at the start of section 2 to keep the golden focused and stable.
	split := "\n## 2. 🚀 The 3-Step Liftoff"
	var header string
	if idx := strings.Index(content, split); idx > 0 {
		header = content[:idx+1] // include trailing newline before the section header
	} else {
		t.Fatalf("expected quickstart section header %q", split)
	}
	testutil.AssertString(t, "testdata/golden/quickstart/minimal.header.md.golden", header)

	// Sanity markers beyond the header to ensure key content is present.
	require.Contains(t, content, "calc.scribe")
	require.Contains(t, content, "calc.helpers")
	require.Contains(t, content, "client := scribe.NewClient(rt)")
	require.Contains(t, content, "[]planner.AgentMessage{")
	require.Contains(t, content, "## 4. 🧠 The Planner:")
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
