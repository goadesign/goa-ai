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
		input := goadsl.Type("ChatEvalInput", func() {
			goadsl.Attribute("prompt", goadsl.String, "User message.")
			goadsl.Required("prompt")
		})
		Suite("chat", func() {
			goadsl.Description("Evaluates chat behavior.")
			goadsl.Timeout("2m")
			Scenario("alarm_inventory", func() {
				goadsl.Description("Lists alarms without truncation.")
				Input(input)
				aidls.Tags("alarm", "smoke")
				goadsl.Timeout("3m")
			})
		})
	}

	ok := eval.Execute(design, nil)
	require.True(t, ok, eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	require.Len(t, evalexpr.Root.Suites, 1)
	suite := evalexpr.Root.Suites[0]
	assert.Equal(t, "chat", suite.Name)
	assert.Len(t, suite.Scenarios, 1)
	assert.NotNil(t, suite.Scenarios[0].Input)
	assert.Equal(t, []string{"alarm", "smoke"}, suite.Scenarios[0].Tags)
	assert.Equal(t, 3*time.Minute, suite.Scenarios[0].Timeout)
}

func TestInputCustomizationExecutesOnce(t *testing.T) {
	setup(t)
	customizations := 0
	design := func() {
		input := goadsl.Type("Input", func() {
			goadsl.Attribute("prompt", goadsl.String, "User message.")
		})
		Suite("chat", func() {
			goadsl.Description("Evaluates chat behavior.")
			goadsl.Timeout("1m")
			Scenario("answer", func() {
				goadsl.Description("Answers one prompt.")
				Input(input, func() {
					customizations++
					goadsl.Required("prompt")
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
				Suite("chat", func() {
					goadsl.Description("Evaluates chat behavior.")
					goadsl.Timeout("1m")
					Scenario("answer", func() {
						goadsl.Description("Answers one prompt.")
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
				Suite("Chat-Evals", func() {
					goadsl.Description("Chat.")
					goadsl.Timeout("1m")
					validScenario()
				})
			},
			wantErr: "must be lower_snake_case",
		},
		{
			name: "missing static fields",
			design: func() {
				Suite("chat", func() {
					Scenario("case", func() {})
				})
			},
			wantErr: "description is required",
		},
		{
			name: "generated method collision",
			design: func() {
				Suite("chat", func() {
					goadsl.Description("Chat.")
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
				Suite("chat", func() {
					goadsl.Description("Chat.")
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
				Suite("chat", func() {
					goadsl.Description("Chat.")
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
				Suite("chat", func() {
					goadsl.Description("Chat.")
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
