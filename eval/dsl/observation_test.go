package dsl_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	. "goa.design/goa-ai/eval/dsl"
	evalexpr "goa.design/goa-ai/eval/expr"
	goadsl "goa.design/goa/v3/dsl"
	"goa.design/goa/v3/eval"
)

func TestObservationRequirementsCompileFieldSelection(t *testing.T) {
	setup(t)
	require.True(t, eval.Execute(func() {
		Suite("messages", func() {
			goadsl.Description("Checks captured messages.")
			goadsl.Timeout("1m")
			Scenario("reply", func() {
				goadsl.Description("Captures all returned messages and their facts.")
				Observation(func() {
					goadsl.Attribute("messages", goadsl.ArrayOf(goadsl.String), "Returned messages, including an empty collection.")
					goadsl.Attribute("facts", goadsl.ArrayOf(goadsl.String), "Captured facts.")
				})
				Requirement("grounded", "The message agrees with captured facts.", func() {
					ForEach("messages")
					Subject("@")
					Evidence("$.facts")
				})
			})
		})
	}, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	requirements := evalexpr.Root.Suites[0].Scenarios[0].Requirements
	require.Len(t, requirements, 1)
	assert.Equal(t, "@", requirements[0].Subject)
	assert.Equal(t, []string{"$.facts"}, requirements[0].Evidence)
	assert.Equal(t, "messages", requirements[0].ForEach)
}

func TestObservationDesignRejectsUnrecordedFactsAndInvalidSelectors(t *testing.T) {
	for _, test := range []struct {
		name      string
		shape     func()
		assertion func()
		want      string
	}{
		{"default value", func() {
			goadsl.Attribute("completed", goadsl.Boolean, "Observed completion.", func() { goadsl.Default(true) })
		}, func() { Check("valid", "Checks the result.") }, "cannot declare defaults"},
		{"nested default", func() {
			goadsl.Attribute("details", func() {
				goadsl.Attribute("answer", goadsl.String, "Captured answer.", func() { goadsl.Default("done") })
			})
		}, func() { Check("valid", "Checks the result.") }, "cannot declare defaults"},
		{"unknown field", messageShape, func() {
			Requirement("grounded", "The answer is grounded.", func() { Subject("unknown") })
		}, "unknown field"},
		{"array traversal", messageShape, func() {
			Requirement("grounded", "The answer is grounded.", func() { Subject("messages.text") })
		}, "not an object"},
		{"non array", messageShape, func() {
			Requirement("grounded", "The answer is grounded.", func() { ForEach("answer"); Subject("@") })
		}, "must select an array"},
		{"missing subject", messageShape, func() {
			Requirement("grounded", "The answer is grounded.", func() {})
		}, "field selector is required"},
		{"duplicate evidence", messageShape, func() {
			Requirement("grounded", "The answer is grounded.", func() { Subject("answer"); Evidence("messages", "messages") })
		}, "duplicate Evidence"},
		{"no assertions", messageShape, func() {}, "at least one Check or Requirement"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setup(t)
			require.True(t, eval.Execute(func() {
				Suite("messages", func() {
					goadsl.Description("Checks captured messages.")
					goadsl.Timeout("1m")
					Scenario("reply", func() {
						goadsl.Description("Captures a reply.")
						Observation(test.shape)
						test.assertion()
					})
				})
			}, nil), eval.Context.Error())
			assert.ErrorContains(t, eval.RunDSL(), test.want)
		})
	}
}

func TestCheckMethodsCannotCollideAcrossScenarios(t *testing.T) {
	setup(t)
	require.True(t, eval.Execute(func() {
		Suite("messages", func() {
			goadsl.Description("Checks captured messages.")
			goadsl.Timeout("1m")
			Scenario("foo_bar", func() {
				goadsl.Description("First case.")
				Observation(goadsl.String)
				Check("valid", "Checks the result.")
			})
			Scenario("foo", func() {
				goadsl.Description("Second case.")
				Observation(goadsl.String)
				Check("bar_valid", "Checks the result.")
			})
		})
	}, nil), eval.Context.Error())
	assert.ErrorContains(t, eval.RunDSL(), `both generate hook method "CheckFooBarValid"`)
}

func messageShape() {
	goadsl.Attribute("answer", goadsl.String, "Captured answer.")
	goadsl.Attribute("messages", goadsl.ArrayOf(goadsl.String), "Captured messages.")
}
