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
	aidls "goa.design/goa-ai/dsl"
	evalcodegen "goa.design/goa-ai/eval/codegen"
	evaldsl "goa.design/goa-ai/eval/dsl"
	evalexpr "goa.design/goa-ai/eval/expr"
	agentexpr "goa.design/goa-ai/expr/agent"
	goacodegen "goa.design/goa/v3/codegen"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestGenerateTypedSuiteAndExample(t *testing.T) {
	roots := runDesign(t, func() {
		input := goadsl.Type("ChatEvalInput", func() {
			goadsl.Attribute("prompt", goadsl.String, "User message.", func() {
				goadsl.MinLength(1)
			})
			goadsl.Required("prompt")
		})
		evaldsl.Suite("chat_quality", func() {
			goadsl.Description("Evaluates chat answers.")
			goadsl.Timeout("90s")
			evaldsl.Scenario("alarm_inventory", func() {
				goadsl.Description("Lists every alarm.")
				evaldsl.Input(input)
				aidls.Tags("alarm", "production")
			})
			evaldsl.Scenario("health_check", func() {
				goadsl.Description("Checks application health.")
			})
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", roots, nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	assert.Equal(t, filepath.Join("gen", "evals", "chat_quality", "suite.go"), files[0].Path)
	content := render(t, files[0])
	assert.Contains(t, content, "ChatEvalInput struct")
	assert.Contains(t, content, "AlarmInventory(context.Context, *ChatEvalInput) (eval.Result, error)")
	assert.Contains(t, content, "HealthCheck(context.Context) (eval.Result, error)")
	assert.Contains(t, content, "AlarmInventory *ChatEvalInput")
	assert.Contains(t, content, "func New(hooks Hooks, inputs Inputs) (eval.Suite, error)")
	assert.Contains(t, content, "ValidateChatEvalInput(inputs.AlarmInventory)")
	assert.Contains(t, content, "hooks.AlarmInventory(ctx, inputs.AlarmInventory)")
	assert.Contains(t, content, "utf8.RuneCountInString")
	assert.NotContains(t, content, "reflect")

	examples, err := evalcodegen.GenerateExample("example.com/project/gen", roots, nil)
	require.NoError(t, err)
	require.Len(t, examples, 1)
	assert.Equal(t, filepath.Join("cmd", "chat_quality-evals", "main.go"), examples[0].Path)
	assert.True(t, examples[0].SkipExist)
	example := render(t, examples[0])
	assert.NotContains(t, example, "DO NOT EDIT")
	assert.Contains(t, example, "This file was generated once by goa example")
	assert.Contains(t, example, "genevalchatquality.New(&hooks{}, scenarioInputs())")
	assert.Contains(t, example, "AlarmInventory: new(genevalchatquality.ChatEvalInput)")
	assert.Contains(t, example, "func (*hooks) HealthCheck(context.Context) (eval.Result, error)")
	assert.Contains(t, example, `flag.Var(&opts.scenarios, "scenario"`)
	assert.Contains(t, example, "runner.RunScenarios(ctx, suite, opts.scenarios...)")
	assert.Contains(t, example, "runner.RunTags(ctx, suite, opts.tags...)")
	assert.Contains(t, example, "json.NewEncoder(os.Stdout)")
}

func TestGenerateInputFormsAndDistinctCustomizations(t *testing.T) {
	roots := runDesign(t, func() {
		shared := goadsl.Type("SharedInput", func() {
			goadsl.Attribute("value", goadsl.String, "Input value.")
		})
		evaldsl.Suite("input_forms", func() {
			goadsl.Description("Exercises supported eval input forms.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("primitive", func() {
				goadsl.Description("Uses a primitive.")
				evaldsl.Input(goadsl.String)
			})
			evaldsl.Scenario("array", func() {
				goadsl.Description("Uses an array.")
				evaldsl.Input(goadsl.ArrayOf(goadsl.String))
			})
			evaldsl.Scenario("mapping", func() {
				goadsl.Description("Uses a map.")
				evaldsl.Input(goadsl.MapOf(goadsl.String, goadsl.Int))
			})
			evaldsl.Scenario("inline", func() {
				goadsl.Description("Uses an inline object.")
				evaldsl.Input(func() {
					goadsl.Attribute("value", goadsl.String, "Input value.")
					goadsl.Required("value")
				})
			})
			evaldsl.Scenario("first_custom", func() {
				goadsl.Description("Uses the first customization.")
				evaldsl.Input(shared, func() {
					goadsl.Required("value")
				})
			})
			evaldsl.Scenario("second_custom", func() {
				goadsl.Description("Uses the second customization.")
				evaldsl.Input(shared, func() {
					goadsl.Required("value")
				})
			})
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", roots, nil)
	require.NoError(t, err)
	require.Len(t, files, 1)
	content := render(t, files[0])
	assert.Contains(t, content, "Primitive(context.Context, string)")
	assert.Contains(t, content, "Array(context.Context, []string)")
	assert.Contains(t, content, "Mapping(context.Context, map[string]int)")
	assert.Contains(t, content, "InlineInput struct")
	assert.Contains(t, content, "SharedInputFirstCustomInput struct")
	assert.Contains(t, content, "SharedInputSecondCustomInput struct")
	assert.Contains(t, content, "ValidateSharedInputFirstCustomInput")
	assert.Contains(t, content, "ValidateSharedInputSecondCustomInput")
}

func TestGenerateAgentAttachedReachableToolContracts(t *testing.T) {
	roots := runDesign(t, func() {
		goadsl.Service("atlas_data_agent", func() {
			aidls.Agent("atlas_data", "Retrieves facility data.", func() {
				aidls.Export("ada", func() {
					aidls.Tool("fetch", "Fetch data.", func() {
						aidls.Args(goadsl.String)
						aidls.Return(goadsl.String)
					})
				})
				aidls.Export("private", func() {
					aidls.Tool("hidden", "Not reachable from Chat.", func() {
						aidls.Args(goadsl.String)
						aidls.Return(goadsl.String)
					})
				})
			})
		})
		goadsl.Service("chat_agent", func() {
			aidls.Agent("chat", "Answers user questions.", func() {
				aidls.Use("chat_tools", func() {
					aidls.Tool("answer", "Answer directly.", func() {
						aidls.Args(goadsl.String)
						aidls.Return(goadsl.String)
					})
				})
				aidls.Use(aidls.AgentToolset("atlas_data_agent", "atlas_data", "ada"))
				evaldsl.Suite("chat", func() {
					goadsl.Description("Evaluates Chat.")
					goadsl.Timeout("1m")
					evaldsl.Scenario("answer", func() {
						goadsl.Description("Answers a question.")
					})
				})
			})
		})
	})

	files, err := evalcodegen.Generate("example.com/project/gen", roots, nil)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, filepath.Join("gen", "evals", "chat", "contract.go"), files[1].Path)
	content := render(t, files[1])
	assert.Contains(t, content, "func MustToolContract(name tools.Ident) *tools.ToolSpec")
	assert.Contains(t, content, "genchattools.Spec(name)")
	assert.Contains(t, content, "genada.Spec(name)")
	assert.NotContains(t, content, "private")
}

func TestGenerateWithoutEvalRootDoesNothing(t *testing.T) {
	existing := []*goacodegen.File{{Path: "gen/existing.go"}}
	files, err := evalcodegen.Generate("example.com/project/gen", nil, existing)
	require.NoError(t, err)
	assert.Equal(t, existing, files)
}

func runDesign(t *testing.T, design func()) []eval.Root {
	t.Helper()
	eval.Reset()
	goaexpr.Root = new(goaexpr.RootExpr)
	goaexpr.GeneratedResultTypes = new(goaexpr.ResultTypesRoot)
	require.NoError(t, eval.Register(goaexpr.Root))
	require.NoError(t, eval.Register(goaexpr.GeneratedResultTypes))
	agentexpr.Root = new(agentexpr.RootExpr)
	require.NoError(t, eval.Register(agentexpr.Root))
	evalexpr.Root = new(evalexpr.RootExpr)
	require.NoError(t, eval.Register(evalexpr.Root))
	goaexpr.Root.API = goaexpr.NewAPIExpr("test", func() {})
	goaexpr.Root.API.Servers = []*goaexpr.ServerExpr{goaexpr.Root.API.DefaultServer()}
	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	return []eval.Root{goaexpr.Root, agentexpr.Root, evalexpr.Root}
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
