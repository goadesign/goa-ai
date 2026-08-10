package dsl_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/eval/dsl"
	evalexpr "goa.design/goa-ai/eval/expr"
	"goa.design/goa/v3/eval"
)

func TestSuiteDSL(t *testing.T) {
	setup(t)
	design := func() {
		Suite("chat", func() {
			Description("Evaluates chat behavior.")
			Timeout("2m")
			Scenario("alarm_inventory", func() {
				Description("Lists alarms without truncation.")
				Input("List every alarm.")
				Tags("alarm", "smoke")
				Timeout("3m")
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
	assert.Equal(t, []string{"alarm", "smoke"}, suite.Scenarios[0].Tags)
	assert.Equal(t, 3*time.Minute, suite.Scenarios[0].Timeout)
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
					Description("Chat.")
					Timeout("1m")
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
					Description("Chat.")
					Timeout("1m")
					Scenario("foo_id", func() {
						Description("First.")
						Input("First.")
					})
					Scenario("foo_i_d", func() {
						Description("Second.")
						Input("Second.")
					})
				})
			},
			wantErr: "both generate hook method",
		},
		{
			name: "zero scenario timeout",
			design: func() {
				Suite("chat", func() {
					Description("Chat.")
					Timeout("1m")
					Scenario("case", func() {
						Description("Case.")
						Input("Run.")
						Timeout("0s")
					})
				})
			},
			wantErr: "must be greater than zero",
		},
		{
			name: "duplicate tag",
			design: func() {
				Suite("chat", func() {
					Description("Chat.")
					Timeout("1m")
					Scenario("case", func() {
						Description("Case.")
						Input("Run.")
						Tags("smoke", "smoke")
					})
				})
			},
			wantErr: "duplicate tag",
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
		Description("Case.")
		Input("Run.")
	})
}

func setup(t *testing.T) {
	t.Helper()
	eval.Reset()
	evalexpr.Root = new(evalexpr.RootExpr)
	require.NoError(t, eval.Register(evalexpr.Root))
}
