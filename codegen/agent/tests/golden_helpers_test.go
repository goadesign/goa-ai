package tests

import (
	"bytes"
	"maps"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/codegen/agent"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/testutil"
	gcodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// setupEvalRoots initializes and registers eval roots for testing.
func setupEvalRoots(t *testing.T) {
	t.Helper()
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))
}

// runDesign prepares roots for generation by executing the DSL.
func runDesign(t *testing.T, design func()) (string, []eval.Root) {
	t.Helper()
	setupEvalRoots(t)
	ok := eval.Execute(design, nil)
	require.True(t, ok, eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return "goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root}
}

// buildAndGenerate executes the DSL, runs codegen and returns generated files.
func buildAndGenerate(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	genpkg, roots := runDesign(t, design)
	files, err := codegen.Generate(genpkg, roots, nil)
	require.NoError(t, err)
	return files
}

// buildAndGenerateExample executes the DSL, runs example-phase codegen and returns files.
func buildAndGenerateExample(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	genpkg, roots := runDesign(t, design)
	files, err := codegen.GenerateExample(genpkg, roots, nil)
	require.NoError(t, err)
	return files
}

// fileContent locates a generated file by path (slash-normalized) and returns the concatenated sections.
func fileContent(t *testing.T, files []*gcodegen.File, wantPath string) string {
	t.Helper()
	normWant := filepath.ToSlash(wantPath)
	for _, f := range files {
		if filepath.ToSlash(f.Path) != normWant {
			continue
		}
		var buf bytes.Buffer
		for _, s := range f.SectionTemplates {
			// Render template sections into final code using optional FuncMap/Data
			tmpl := template.New(s.Name)
			// Provide default helper funcs used by shared templates (e.g., header)
			fm := template.FuncMap{
				"comment": gcodegen.Comment,
				"commandLine": func() string {
					return ""
				},
			}
			if s.FuncMap != nil {
				maps.Copy(fm, s.FuncMap)
			}
			tmpl = tmpl.Funcs(fm)
			pt, err := tmpl.Parse(s.Source)
			require.NoErrorf(t, err, "parse section %s", s.Name)
			var sb bytes.Buffer
			err = pt.Execute(&sb, s.Data)
			require.NoErrorf(t, err, "execute section %s", s.Name)
			buf.Write(sb.Bytes())
		}
		content := buf.String()
		require.NotEmptyf(t, content, "empty content for %s", wantPath)
		return content
	}
	require.Failf(t, "not found", "generated file not found: %s", wantPath)
	return "" // unreachable
}

// assertGoldenGo compares content as Go source with the golden file path
// relative to tests/testdata/golden/<scenario>/...
func assertGoldenGo(t *testing.T, scenario string, name string, content string) {
	t.Helper()
	p := filepath.Join("testdata", "golden", scenario, name)
	testutil.AssertGo(t, p, content)
}
