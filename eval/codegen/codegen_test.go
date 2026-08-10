package codegen_test

import (
	"bytes"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	evalcodegen "goa.design/goa-ai/eval/codegen"
	. "goa.design/goa-ai/eval/dsl"
	evalexpr "goa.design/goa-ai/eval/expr"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

func TestGenerateSuite(t *testing.T) {
	root := runDesign(t, func() {
		Suite("chat_quality", func() {
			Description("Evaluates chat answers.")
			Timeout("90s")
			Scenario("alarm_inventory", func() {
				Description("Lists every alarm.")
				Input("List alarms.\nDo not stop early.")
				Tags("alarm", "production")
			})
			Scenario("solar_analysis", func() {
				Description("Analyzes solar performance.")
				Input("Analyze solar performance.")
				Timeout("3m")
			})
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", []eval.Root{root}, nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, filepath.Join("gen", "evals", "chat_quality", "suite.go"), files[0].Path)
	content := render(t, files[0])
	assert.Contains(t, content, "package chatquality")
	assert.Contains(t, content, "AlarmInventory(context.Context, string) (eval.Result, error)")
	assert.Contains(t, content, "SolarAnalysis(context.Context, string) (eval.Result, error)")
	assert.Contains(t, content, "Run: hooks.AlarmInventory")
	assert.Contains(t, content, "Timeout: time.Duration(90000000000)")
	assert.Contains(t, content, "Timeout: time.Duration(180000000000)")
	assert.Contains(t, content, `Input: "List alarms.\nDo not stop early."`)
	assert.NotContains(t, content, "reflect")
	assert.NotContains(t, content, "register")
}

func TestGenerateWithoutEvalRootDoesNothing(t *testing.T) {
	existing := []*goacodegen.File{{Path: "gen/existing.go"}}
	files, err := evalcodegen.Generate("example.com/project/gen", nil, existing)
	require.NoError(t, err)
	assert.Equal(t, existing, files)
}

func TestGenerateQuotesAuthoredText(t *testing.T) {
	root := runDesign(t, func() {
		Suite("chat", func() {
			Description("A \"quoted\" description.")
			Timeout("1s")
			Scenario("case", func() {
				Description("Case.")
				Input("line one\n`line two`")
			})
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", []eval.Root{root}, nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	content := render(t, files[0])
	assert.Contains(t, content, `Description: "A \"quoted\" description."`)
	assert.Contains(t, content, `Input: "line one\n`+"`"+`line two`+"`"+`"`)
}

func TestGenerateMultipleSuitesPreservesDeclarationOrder(t *testing.T) {
	root := runDesign(t, func() {
		Suite("first", func() {
			Description("First suite.")
			Timeout("1s")
			Scenario("one", func() {
				Description("First scenario.")
				Input("One.")
			})
		})
		Suite("second_suite", func() {
			Description("Second suite.")
			Timeout("2s")
			Scenario("two", func() {
				Description("Second scenario.")
				Input("Two.")
			})
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", []eval.Root{root}, nil)

	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, filepath.Join("gen", "evals", "first", "suite.go"), files[0].Path)
	assert.Equal(t, filepath.Join("gen", "evals", "second_suite", "suite.go"), files[1].Path)
	assert.Contains(t, render(t, files[1]), "package secondsuite")
}

func TestGenerateGoifiesReservedSuitePackageName(t *testing.T) {
	root := runDesign(t, func() {
		Suite("type", func() {
			Description("Reserved package name.")
			Timeout("1s")
			Scenario("case", func() {
				Description("Case.")
				Input("Run.")
			})
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", []eval.Root{root}, nil)

	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Contains(t, render(t, files[0]), "package type_")
}

func TestGenerateEmptyTags(t *testing.T) {
	root := runDesign(t, func() {
		Suite("untagged", func() {
			Description("Untagged scenario.")
			Timeout("1s")
			Scenario("case", func() {
				Description("No tags.")
				Input("Run.")
			})
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", []eval.Root{root}, nil)

	require.NoError(t, err)
	require.Len(t, files, 1)
	content := render(t, files[0])
	assert.Contains(t, content, "Tags: []string{}")
}

func runDesign(t *testing.T, design func()) *evalexpr.RootExpr {
	t.Helper()
	eval.Reset()
	evalexpr.Root = new(evalexpr.RootExpr)
	require.NoError(t, eval.Register(evalexpr.Root))
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return evalexpr.Root
}

func render(t *testing.T, file *goacodegen.File) string {
	t.Helper()
	var result bytes.Buffer
	for _, section := range file.SectionTemplates {
		functions := template.FuncMap{
			"comment": goacodegen.Comment,
			"commandLine": func() string {
				return "goa gen example.com/project/design"
			},
		}
		maps.Copy(functions, section.FuncMap)
		parsed, err := template.New(section.Name).Funcs(functions).Parse(section.Source)
		require.NoError(t, err)
		require.NoError(t, parsed.Execute(&result, section.Data))
	}
	content := result.String()
	_, err := parser.ParseFile(token.NewFileSet(), file.Path, content, parser.AllErrors)
	require.NoError(t, err)
	return content
}
