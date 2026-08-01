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
	aieval "goa.design/goa-ai/eval"
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
			Calibration("entailed", func() {
				Answer("The pump is on.")
				Claim("The pump is on.")
				Want(aieval.Entailed)
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
	assert.Contains(t, content, "Want: eval.Entailed")
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

func TestGenerateAllLabelsAndEmptyStaticCollections(t *testing.T) {
	root := runDesign(t, func() {
		Suite("judge", func() {
			Description("Judge labels.")
			Timeout("1s")
			Scenario("case", func() {
				Description("No tags.")
				Input("Run.")
			})
			for _, calibration := range []struct {
				id    string
				label aieval.Label
			}{
				{id: "entailed", label: aieval.Entailed},
				{id: "contradicted", label: aieval.Contradicted},
				{id: "not_addressed", label: aieval.NotAddressed},
				{id: "indeterminate", label: aieval.Indeterminate},
			} {
				Calibration(calibration.id, func() {
					Answer("Answer.")
					Claim("Claim.")
					Want(calibration.label)
				})
			}
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", []eval.Root{root}, nil)

	require.NoError(t, err)
	require.Len(t, files, 1)
	content := render(t, files[0])
	assert.Contains(t, content, "Tags: []string{}")
	assert.Contains(t, content, "Want: eval.Entailed")
	assert.Contains(t, content, "Want: eval.Contradicted")
	assert.Contains(t, content, "Want: eval.NotAddressed")
	assert.Contains(t, content, "Want: eval.Indeterminate")
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
