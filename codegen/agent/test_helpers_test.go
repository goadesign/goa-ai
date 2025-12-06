package codegen_test

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	agentcodegen "goa.design/goa-ai/codegen/agent"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// testSetup initializes the Goa evaluation context for tests.
// It resets the evaluation state and registers the required expression roots.
func testSetup(t *testing.T) {
	t.Helper()
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))

	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))
}

// testGenerate executes a design function and generates code.
// It returns the generated files.
func testGenerate(t *testing.T, genpkg string, design func()) []*codegen.File {
	t.Helper()
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())

	files, err := agentcodegen.Generate(genpkg, []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)
	return files
}

// testFindFileContent finds a file by path and returns its rendered content.
func testFindFileContent(t *testing.T, files []*codegen.File, expectedPath string) string {
	t.Helper()
	normalizedPath := filepath.ToSlash(expectedPath)
	for _, f := range files {
		if filepath.ToSlash(f.Path) == normalizedPath {
			var buf bytes.Buffer
			for _, s := range f.SectionTemplates {
				require.NoError(t, s.Write(&buf))
			}
			return buf.String()
		}
	}
	return ""
}
