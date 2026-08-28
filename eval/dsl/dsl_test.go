package dsl_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	aidls "goa.design/goa-ai/dsl"
	. "goa.design/goa-ai/eval/dsl"
	evalexpr "goa.design/goa-ai/eval/expr"
	agentexpr "goa.design/goa-ai/expr/agent"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"
)

func TestSuiteDSL(t *testing.T) {
	setup(t)
	design := func() {
		input := goadsl.Type("QueryEvalInput", func() {
			goadsl.Attribute("query", goadsl.String, "Assistant request.")
			goadsl.Required("query")
		})
		Suite("assistant", func() {
			goadsl.Description("Evaluates assistant behavior.")
			goadsl.Timeout("2m")
			Scenario("record_inventory", func() {
				goadsl.Description("Lists records without truncation.")
				Input(input)
				aidls.Tags("records", "smoke")
				goadsl.Timeout("3m")
			})
		})
	}

	ok := eval.Execute(design, nil)
	require.True(t, ok, eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	require.Len(t, evalexpr.Root.Suites, 1)
	suite := evalexpr.Root.Suites[0]
	assert.Equal(t, "assistant", suite.Name)
	assert.Len(t, suite.Scenarios, 1)
	assert.NotNil(t, suite.Scenarios[0].Input)
	assert.Equal(t, []string{"records", "smoke"}, suite.Scenarios[0].Tags)
	assert.Equal(t, 3*time.Minute, suite.Scenarios[0].Timeout)
}

func TestInputCustomizationExecutesOnce(t *testing.T) {
	setup(t)
	customizations := 0
	design := func() {
		input := goadsl.Type("Input", func() {
			goadsl.Attribute("query", goadsl.String, "Assistant request.")
		})
		Suite("assistant", func() {
			goadsl.Description("Evaluates assistant behavior.")
			goadsl.Timeout("1m")
			Scenario("answer", func() {
				goadsl.Description("Answers one query.")
				Input(input, func() {
					customizations++
					goadsl.Required("query")
				})
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	assert.Equal(t, 1, customizations)
	input := evalexpr.Root.Suites[0].Scenarios[0].Input.Type.(goaexpr.UserType)
	assert.Equal(t, "Input_answer_Input", input.Name())
}

// A customization that does not change requiredness must keep the original
// type identity so the type stays shared across declarations, mirroring Goa's
// method DSL semantics. Regression test for goa-ai v0.75.1, which renamed
// every customized duplicate and split shared types per declaration.
func TestInputCustomizationWithoutRequirednessChangeKeepsTypeIdentity(t *testing.T) {
	setup(t)
	design := func() {
		input := goadsl.Type("SharedInput", func() {
			goadsl.Attribute("query", goadsl.String, "Assistant request.")
			goadsl.Required("query")
		})
		Suite("assistant", func() {
			goadsl.Description("Evaluates assistant behavior.")
			goadsl.Timeout("1m")
			Scenario("answer", func() {
				goadsl.Description("Answers one query.")
				Input(input, func() {
					goadsl.Description("Query for the answer scenario.")
				})
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	input := evalexpr.Root.Suites[0].Scenarios[0].Input.Type.(goaexpr.UserType)
	assert.Equal(t, "SharedInput", input.Name())
}

// A declaration with a description string must operate on a duplicate of the
// user type so downstream processing never mutates the shared original
// instance. Regression test for goa-ai v0.75.1, which returned the original
// type and let tool processing mutations leak into every other use of the
// type, changing generated type placement in multi-service designs.
func TestInputWithDescriptionDuplicatesType(t *testing.T) {
	setup(t)
	var original goaexpr.UserType
	design := func() {
		original = goadsl.Type("SharedInput", func() {
			goadsl.Attribute("query", goadsl.String, "Assistant request.")
			goadsl.Required("query")
		})
		Suite("assistant", func() {
			goadsl.Description("Evaluates assistant behavior.")
			goadsl.Timeout("1m")
			Scenario("answer", func() {
				goadsl.Description("Answers one query.")
				Input(original, "Query for the answer scenario.")
			})
		})
	}

	require.True(t, eval.Execute(design, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	input := evalexpr.Root.Suites[0].Scenarios[0].Input.Type.(goaexpr.UserType)
	assert.Equal(t, "SharedInput", input.Name())
	assert.NotSame(t, original, input)
}

func TestInputRejectsMalformedShapeArguments(t *testing.T) {
	tests := []struct {
		name    string
		input   func()
		wantErr string
	}{
		{
			name: "invalid optional argument",
			input: func() {
				Input(goadsl.String, 42)
			},
			wantErr: "description string or DSL function",
		},
		{
			name: "invalid description position",
			input: func() {
				Input(goadsl.String, func() {}, "description")
			},
			wantErr: "description string",
		},
		{
			name: "excess arguments",
			input: func() {
				Input(goadsl.String, "description", func() {}, func() {})
			},
			wantErr: "too many arguments",
		},
		{
			name: "inline function with description",
			input: func() {
				Input(func() {}, "description")
			},
			wantErr: "accepts no additional arguments",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup(t)
			design := func() {
				Suite("assistant", func() {
					goadsl.Description("Evaluates assistant behavior.")
					goadsl.Timeout("1m")
					Scenario("answer", func() {
						goadsl.Description("Answers one query.")
						test.input()
					})
				})
			}

			require.True(t, eval.Execute(design, nil), eval.Context.Error())
			assert.ErrorContains(t, eval.RunDSL(), test.wantErr)
		})
	}
}

func TestSuiteDSLValidation(t *testing.T) {
	tests := []struct {
		name    string
		design  func()
		wantErr string
	}{
		{
			name: "invalid suite identifier",
			design: func() {
				Suite("Assistant-Evals", func() {
					goadsl.Description("Assistant.")
					goadsl.Timeout("1m")
					validScenario()
				})
			},
			wantErr: "must be lower_snake_case",
		},
		{
			name: "missing static fields",
			design: func() {
				Suite("assistant", func() {
					Scenario("case", func() {})
				})
			},
			wantErr: "description is required",
		},
		{
			name: "generated method collision",
			design: func() {
				Suite("assistant", func() {
					goadsl.Description("Assistant.")
					goadsl.Timeout("1m")
					Scenario("foo_id", func() {
						goadsl.Description("First.")
					})
					Scenario("foo_i_d", func() {
						goadsl.Description("Second.")
					})
				})
			},
			wantErr: "both generate hook method",
		},
		{
			name: "zero scenario timeout",
			design: func() {
				Suite("assistant", func() {
					goadsl.Description("Assistant.")
					goadsl.Timeout("1m")
					Scenario("case", func() {
						goadsl.Description("Case.")
						goadsl.Timeout("0s")
					})
				})
			},
			wantErr: "must be greater than zero",
		},
		{
			name: "duplicate tag",
			design: func() {
				Suite("assistant", func() {
					goadsl.Description("Assistant.")
					goadsl.Timeout("1m")
					Scenario("case", func() {
						goadsl.Description("Case.")
						aidls.Tags("smoke", "smoke")
					})
				})
			},
			wantErr: "duplicate tag",
		},
		{
			name: "union input",
			design: func() {
				Suite("assistant", func() {
					goadsl.Description("Assistant.")
					goadsl.Timeout("1m")
					Scenario("case", func() {
						goadsl.Description("Case.")
						Input(func() {
							goadsl.OneOf("value", func() {
								goadsl.Attribute("text", goadsl.String, "Text value.")
								goadsl.Attribute("count", goadsl.Int, "Count value.")
							})
						})
					})
				})
			},
			wantErr: "does not support OneOf",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			setup(t)
			assert.True(t, eval.Execute(test.design, nil), eval.Context.Error())
			assert.ErrorContains(t, eval.RunDSL(), test.wantErr)
		})
	}
}

func validScenario() {
	Scenario("case", func() {
		goadsl.Description("Case.")
	})
}

func setup(t *testing.T) {
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
}
