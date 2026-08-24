package codegen_test

import (
	"bytes"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"sync"
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

	files := generateEvalFiles(t, "example.com/project/gen", roots, false)
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

	examples := generateEvalFiles(t, "example.com/project/gen", roots, true)
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

	files := generateEvalFiles(t, "example.com/project/gen", roots, false)
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

// TestGeneratePresenceOnlyInputValidator verifies that a required input object
// keeps its top-level nil check without emitting an empty private validator.
func TestGeneratePresenceOnlyInputValidator(t *testing.T) {
	roots := runDesign(t, func() {
		input := goadsl.Type("AskPayload", func() {
			goadsl.Attribute("question", goadsl.String, "Question to answer.")
			goadsl.Required("question")
		})
		evaldsl.Suite("chat_quality", func() {
			goadsl.Description("Evaluates chat answers.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("greeting_reply", func() {
				goadsl.Description("Answers one question.")
				evaldsl.Input(input)
			})
		})
	})

	files := generateEvalFiles(t, "example.com/project/gen", roots, false)
	require.Len(t, files, 1)
	content := render(t, files[0])
	assert.Contains(t, content, "// New validates application inputs and builds the evaluation suite.")
	assert.Contains(t, content, "func ValidateAskPayload(value *AskPayload) error")
	assert.Contains(t, content, `goa.MissingFieldError("input", "evaluation scenario")`)
	assert.Contains(t, content, "return nil")
	assert.NotContains(t, content, "validateAskPayload")
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

	plan, err := evalcodegen.PlanForTest("example.com/project/gen", roots, false)
	require.NoError(t, err)
	pkg := plan.Generation().Package("example.com/project/gen/evals/chat")
	require.NoError(t, pkg.DeclareName(goacodegen.NewExactName(
		goacodegen.NameFunction,
		"MustToolContract",
	)))
	// Remove the toolsets after planning. File writing must use the tool names
	// and schemas already saved in the plan.
	for _, root := range roots {
		agents, ok := root.(*agentexpr.RootExpr)
		if !ok {
			continue
		}
		for _, agent := range agents.Agents {
			if agent.Used != nil {
				agent.Used.Toolsets = nil
			}
			if agent.Exported != nil {
				agent.Exported.Toolsets = nil
			}
		}
	}
	files, err := plan.Generate(nil)
	require.NoError(t, err)
	require.Len(t, files, 2)
	assert.Equal(t, filepath.Join("gen", "evals", "chat", "contract.go"), files[1].Path)
	content := render(t, files[1])
	assert.Contains(t, content, "func MustToolContract2(name tools.Ident) *tools.ToolSpec")
	assert.Contains(t, content, "genchattools.Spec(name)")
	assert.Contains(t, content, "genada.Spec(name)")
	assert.NotContains(t, content, "private")
}

func TestGenerateWithoutEvalRootDoesNothing(t *testing.T) {
	existing := []*goacodegen.File{{Path: "gen/existing.go"}}
	files := generateEvalFiles(t, "example.com/project/gen", nil, false, existing...)
	assert.Equal(t, existing, files)
}

// TestEvalPlansKeepRunDataSeparate builds two plans at the same time and
// checks that each one writes only the suite from its own design.
func TestEvalPlansKeepRunDataSeparate(t *testing.T) {
	firstRoots := runDesign(t, func() {
		evaldsl.Suite("first", func() {
			goadsl.Description("Evaluates the first flow.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("run_first", func() {
				goadsl.Description("Runs the first flow.")
			})
		})
	})
	secondRoots := runDesign(t, func() {
		evaldsl.Suite("second", func() {
			goadsl.Description("Evaluates the second flow.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("run_second", func() {
				goadsl.Description("Runs the second flow.")
			})
		})
	})
	genpkgs := []string{"example.com/first/gen", "example.com/second/gen"}
	roots := [][]eval.Root{firstRoots, secondRoots}
	errors := make(chan error, len(roots))
	results := make([][]*goacodegen.File, len(roots))
	var wait sync.WaitGroup
	for index := range roots {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			plan, err := evalcodegen.PlanForTest(genpkgs[index], roots[index], false)
			if err != nil {
				errors <- err
				return
			}
			generated, err := plan.Generate(nil)
			results[index] = generated
			errors <- err
		}(index)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	first := results[0]
	second := results[1]
	require.Equal(t, filepath.Join("gen", "evals", "first", "suite.go"), first[0].Path)
	require.Equal(t, filepath.Join("gen", "evals", "second", "suite.go"), second[0].Path)
	assert.Contains(t, render(t, first[0]), "RunFirst")
	assert.NotContains(t, render(t, first[0]), "RunSecond")
	assert.Contains(t, render(t, second[0]), "RunSecond")
	assert.NotContains(t, render(t, second[0]), "RunFirst")
}

// TestEvalPlanCopiesSuiteFacts changes a scenario name after planning and
// checks that file writing still uses the original name saved in the plan.
func TestEvalPlanCopiesSuiteFacts(t *testing.T) {
	roots := runDesign(t, func() {
		evaldsl.Suite("stable", func() {
			goadsl.Description("Evaluates a stable flow.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("before", func() {
				goadsl.Description("Runs before the change.")
			})
		})
	})
	plan, err := evalcodegen.PlanForTest("example.com/project/gen", roots, false)
	require.NoError(t, err)
	evaluationRootForTest(t, roots).Suites[0].Scenarios[0].Name = "after"

	files, err := plan.Generate(nil)
	require.NoError(t, err)
	content := render(t, files[0])
	assert.Contains(t, content, "Before(context.Context)")
	assert.NotContains(t, content, "After(context.Context)")
}

// TestEvalPlanUsesPackageNamesChosenAfterPlanning checks that a competing
// plugin can take base names without breaking eval definitions or references.
func TestEvalPlanUsesPackageNamesChosenAfterPlanning(t *testing.T) {
	roots := runDesign(t, func() {
		child := goadsl.Type("ChildInput", func() {
			goadsl.Attribute("value", goadsl.String, "Value to check.", func() {
				goadsl.MinLength(1)
			})
			goadsl.Required("value")
		})
		input := goadsl.Type("RunInput", func() {
			goadsl.Attribute("child", child, "Nested value to check.")
			goadsl.Required("child")
		})
		evaldsl.Suite("claimed", func() {
			goadsl.Description("Evaluates package claims.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("run", func() {
				goadsl.Description("Runs the claim check.")
				evaldsl.Input(input)
			})
		})
	})
	plan, err := evalcodegen.PlanForTest("example.com/project/gen", roots, false)
	require.NoError(t, err)

	pkg := plan.Generation().Package("example.com/project/gen/evals/claimed")
	for _, name := range []string{"RunInput", "Hooks", "Inputs"} {
		require.NoError(t, pkg.DeclareName(goacodegen.NewExactName(goacodegen.NameType, name)))
	}
	for _, name := range []string{"ValidateChildInput", "validateChildInput", "New"} {
		require.NoError(t, pkg.DeclareName(goacodegen.NewExactName(goacodegen.NameFunction, name)))
	}
	_, err = plan.Generation().ClaimOutputPackage("example.com/other", filepath.Join("gen", "evals", "claimed"))
	require.ErrorContains(t, err, "output directory")
	files, err := plan.Generate(nil)
	require.NoError(t, err)
	content := render(t, files[0])
	assert.Contains(t, content, "RunInput2 struct")
	assert.Contains(t, content, "Hooks2 interface")
	assert.Contains(t, content, "Inputs2 struct")
	assert.Contains(t, content, "func New2(hooks Hooks2, inputs Inputs2)")
	assert.Contains(t, content, "func ValidateRunInput2(value *RunInput2)")
	assert.Contains(t, content, "func ValidateChildInput2(value *ChildInput)")
	assert.Contains(t, content, `return validateRunInput2(value, "input")`)
	assert.Contains(t, content, "func validateChildInput2(value *ChildInput, path string)")
	assert.Contains(t, content, `validateChildInput2(value.Child, path + ".child")`)
	assert.Contains(t, content, "ValidateRunInput2(inputs.Run)")
}

// TestEvalExamplePlanUsesPackageNamesChosenAfterPlanning checks that command
// helpers use the final names owned by their output package.
func TestEvalExamplePlanUsesPackageNamesChosenAfterPlanning(t *testing.T) {
	roots := runDesign(t, func() {
		evaldsl.Suite("claimed", func() {
			goadsl.Description("Evaluates command package claims.")
			goadsl.Timeout("1m")
			evaldsl.Scenario("run", func() {
				goadsl.Description("Runs the command claim check.")
			})
		})
	})
	plan, err := evalcodegen.PlanForTest("example.com/project/gen", roots, true)
	require.NoError(t, err)
	suitePackage := plan.Generation().Package("example.com/project/gen/evals/claimed")
	require.NoError(t, suitePackage.DeclareName(goacodegen.NewExactName(
		goacodegen.NameType,
		"Inputs",
	)))
	require.NoError(t, suitePackage.DeclareName(goacodegen.NewExactName(
		goacodegen.NameFunction,
		"New",
	)))

	pkg, err := plan.Generation().ClaimOutputPackage(
		"example.com/project/cmd/claimed-evals",
		filepath.Join("cmd", "claimed-evals"),
	)
	require.NoError(t, err)
	_, err = plan.Generation().ClaimOutputPackage(
		"example.com/other",
		filepath.Join("cmd", "claimed-evals"),
	)
	require.ErrorContains(t, err, "output directory")
	for _, name := range []string{"hooks", "values", "options"} {
		require.NoError(t, pkg.DeclareName(goacodegen.NewExactName(goacodegen.NameType, name)))
	}
	for _, name := range []string{"run", "scenarioInputs"} {
		require.NoError(t, pkg.DeclareName(goacodegen.NewExactName(goacodegen.NameFunction, name)))
	}
	files, err := plan.Generate(nil)
	require.NoError(t, err)
	content := render(t, files[0])
	assert.Contains(t, content, "hooks2 struct")
	assert.Contains(t, content, "values2 []string")
	assert.Contains(t, content, "options2 struct")
	assert.Contains(t, content, "opts := options2{}")
	assert.Contains(t, content, "func run2(ctx context.Context, opts options2) error")
	assert.Contains(t, content, "func scenarioInputs2() genevalclaimed.Inputs2")
	assert.Contains(t, content, "genevalclaimed.New2(&hooks2{}, scenarioInputs2())")
}

// generateEvalFiles records the suite files, chooses every package-level Go
// name, and returns those files.
func generateEvalFiles(
	t *testing.T,
	genpkg string,
	roots []eval.Root,
	example bool,
	files ...*goacodegen.File,
) []*goacodegen.File {
	t.Helper()
	plan, err := evalcodegen.PlanForTest(genpkg, roots, example)
	require.NoError(t, err)
	generated, err := plan.Generate(files)
	require.NoError(t, err)
	return generated
}

// evaluationRootForTest finds the evaluation suites created by runDesign.
func evaluationRootForTest(t *testing.T, roots []eval.Root) *evalexpr.RootExpr {
	t.Helper()
	for _, root := range roots {
		if suites, ok := root.(*evalexpr.RootExpr); ok {
			return suites
		}
	}
	t.Fatal("evaluation root not found")
	return nil
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
