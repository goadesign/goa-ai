// Package testhelpers provides shared test utilities for codegen packages.
package testhelpers

import (
	"bytes"
	"maps"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/stretchr/testify/require"
	codegen "goa.design/goa-ai/codegen/agent"
	agentsExpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/testutil"
	gcodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// SetupEvalRoots initializes and registers eval roots for testing.
func SetupEvalRoots(t *testing.T) {
	t.Helper()
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	agentsExpr.Root = &agentsExpr.RootExpr{}
	require.NoError(t, eval.Register(agentsExpr.Root))
}

// RunDesign prepares roots for generation by executing the DSL.
func RunDesign(t *testing.T, design func()) (string, []eval.Root) {
	t.Helper()
	SetupEvalRoots(t)
	ok := eval.Execute(design, nil)
	require.True(t, ok, eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return "goa.design/goa-ai", []eval.Root{goaexpr.Root, agentsExpr.Root}
}

// BuildAndGenerate executes the DSL, runs codegen and returns generated files.
func BuildAndGenerate(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	genpkg, roots := RunDesign(t, design)
	files, err := codegen.Generate(genpkg, roots, nil)
	require.NoError(t, err)
	return files
}

// BuildAndGenerateWithPkg executes the DSL with a custom package path.
func BuildAndGenerateWithPkg(t *testing.T, genpkg string, design func()) []*gcodegen.File {
	t.Helper()
	SetupEvalRoots(t)
	ok := eval.Execute(design, nil)
	require.True(t, ok, eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	files, err := codegen.Generate(genpkg, []eval.Root{goaexpr.Root, agentsExpr.Root}, nil)
	require.NoError(t, err)
	return files
}

// BuildAndGenerateExample executes the DSL, runs example-phase codegen and returns files.
func BuildAndGenerateExample(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	genpkg, roots := RunDesign(t, design)
	files, err := codegen.GenerateExample(genpkg, roots, nil)
	require.NoError(t, err)
	return files
}

// FileContent locates a generated file by path (slash-normalized) and returns the concatenated sections.
func FileContent(t *testing.T, files []*gcodegen.File, wantPath string) string {
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

// FileExists checks if a file exists in the generated files.
func FileExists(files []*gcodegen.File, wantPath string) bool {
	normWant := filepath.ToSlash(wantPath)
	for _, f := range files {
		if filepath.ToSlash(f.Path) == normWant {
			return true
		}
	}
	return false
}

// FindFile locates a generated file by path (slash-normalized).
func FindFile(files []*gcodegen.File, wantPath string) *gcodegen.File {
	normWant := filepath.ToSlash(wantPath)
	for _, f := range files {
		if filepath.ToSlash(f.Path) == normWant {
			return f
		}
	}
	return nil
}

// AssertGoldenGo compares content as Go source with the golden file path
// relative to testdata/golden/<scenario>/...
func AssertGoldenGo(t *testing.T, scenario string, name string, content string) {
	t.Helper()
	p := filepath.Join("testdata", "golden", scenario, name)
	testutil.AssertGo(t, p, content)
}

// AssertGoldenGoAbs compares content as Go source with an absolute golden file path.
func AssertGoldenGoAbs(t *testing.T, goldenPath string, content string) {
	t.Helper()
	testutil.AssertGo(t, goldenPath, content)
}
