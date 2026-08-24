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
	evalsExpr "goa.design/goa-ai/eval/expr"
	agentsExpr "goa.design/goa-ai/expr/agent"
	mcpexpr "goa.design/goa-ai/expr/mcp"
	"goa.design/goa-ai/testutil"
	gcodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/codegen/service"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

// SetupEvalRoots creates and registers the design roots used by one test run.
// It returns the roots that code generation must read after evaluation.
func SetupEvalRoots(t *testing.T) []eval.Root {
	t.Helper()
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	mcpexpr.Root = mcpexpr.NewRoot()
	agentsExpr.Root = &agentsExpr.RootExpr{}
	evalsExpr.Root = new(evalsExpr.RootExpr)

	roots := []eval.Root{goaexpr.Root, mcpexpr.Root, agentsExpr.Root, evalsExpr.Root}
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	for _, root := range roots[1:] {
		require.NoError(t, eval.Register(root))
	}
	return roots
}

// RunDesign prepares roots for generation by executing the DSL.
func RunDesign(t *testing.T, design func()) (string, []eval.Root) {
	t.Helper()
	roots := SetupEvalRoots(t)
	ok := eval.Execute(design, nil)
	require.True(t, ok, eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return "goa.design/goa-ai/gen", roots
}

// BuildAndGenerate executes the DSL, runs codegen and returns generated files.
func BuildAndGenerate(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	genpkg, roots := RunDesign(t, design)
	return buildAgentFiles(t, genpkg, roots, false)
}

// BuildAndGenerateWithPkg executes the DSL with a custom package path.
func BuildAndGenerateWithPkg(t *testing.T, genpkg string, design func()) []*gcodegen.File {
	t.Helper()
	roots := SetupEvalRoots(t)
	ok := eval.Execute(design, nil)
	require.True(t, ok, eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return buildAgentFiles(t, genpkg, roots, false)
}

// BuildAndGenerateWithExamplePkg runs agent and application-example generation
// against one evaluated design so compile tests can render both sides of the
// generated import contract into a standalone module.
func BuildAndGenerateWithExamplePkg(t *testing.T, genpkg string, design func()) []*gcodegen.File {
	t.Helper()
	SetupEvalRoots(t)
	ok := eval.Execute(design, nil)
	require.True(t, ok, eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	roots := []eval.Root{goaexpr.Root, agentsExpr.Root, evalsExpr.Root}
	require.NoError(t, codegen.Prepare(genpkg, roots))
	files, err := codegen.Generate(genpkg, roots, nil)
	require.NoError(t, err)
	files, err = codegen.GenerateExample(genpkg, roots, files)
	require.NoError(t, err)
	return files
}

// BuildAndGenerateExample executes the DSL, runs example-phase codegen and returns files.
func BuildAndGenerateExample(t *testing.T, design func()) []*gcodegen.File {
	t.Helper()
	genpkg, roots := RunDesign(t, design)
	return buildAgentFiles(t, genpkg, roots, true)
}

// buildAgentFiles runs the typed Goa and agent plans used by production
// plugins and returns their in-memory files.
func buildAgentFiles(t *testing.T, genpkg string, roots []eval.Root, example bool) []*gcodegen.File {
	t.Helper()
	require.NoError(t, codegen.Prepare(genpkg, roots))
	generation, err := gcodegen.NewGeneration(genpkg, roots)
	require.NoError(t, err)
	var goaRoot *goaexpr.RootExpr
	for _, root := range generation.Roots() {
		if candidate, ok := root.(*goaexpr.RootExpr); ok {
			goaRoot = candidate
			break
		}
	}
	require.NotNil(t, goaRoot)
	servicePlan, err := service.NewPlan(
		goaRoot,
		generation,
		goaexpr.NewExampleGenerator(goaRoot.API.RandomizerFactory),
	)
	require.NoError(t, err)
	var agentPlan *codegen.Plan
	if example {
		agentPlan, err = codegen.NewExamplePlan(generation, servicePlan)
	} else {
		agentPlan, err = codegen.NewPlan(generation, servicePlan)
	}
	require.NoError(t, err)
	require.NoError(t, generation.Freeze())
	require.NoError(t, servicePlan.Link())
	require.NoError(t, agentPlan.Link())
	files, err := agentPlan.Files(nil)
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
