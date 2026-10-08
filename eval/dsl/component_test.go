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

func TestComponentReusesAssertionsWithoutChangingSelectors(t *testing.T) {
	setup(t)
	require.True(t, eval.Execute(func() {
		answer := goadsl.Type("AnswerObservation", messageShape)
		quality := Component("answer_quality", func() {
			goadsl.Description("Checks an answer against captured facts.")
			Observation(answer)
			Check("present", "An answer was captured.")
			Requirement("grounded", "The answer agrees with captured facts.", func() {
				Subject("answer")
				Evidence("messages")
				Reasoning()
			})
		})
		Suite("messages", func() {
			goadsl.Description("Checks focused answers and complete flows.")
			goadsl.Timeout("1m")
			Scenario("focused", func() {
				goadsl.Description("Captures one answer.")
				Observation(answer)
				Assess("answer", quality, "$")
			})
			Scenario("flow", func() {
				goadsl.Description("Captures both answers in one flow.")
				Observation(func() {
					goadsl.Attribute("first", answer, "First captured answer.")
					goadsl.Attribute("second", answer, "Second captured answer.")
				})
				Assess("first", quality, "first")
				Assess("second", quality, "second")
			})
		})
	}, nil), eval.Context.Error())
	require.NoError(t, eval.RunDSL())
	component := evalexpr.Root.Components[0]
	require.Len(t, component.Requirements, 1)
	assert.Equal(t, "answer", component.Requirements[0].Subject)
	assert.Equal(t, []string{"messages"}, component.Requirements[0].Evidence)
	assert.True(t, component.Requirements[0].Reasoning)
	assert.Len(t, evalexpr.Root.Suites[0].Scenarios[1].Assessments, 2)
}

func TestComponentDesignRejectsInvalidBindings(t *testing.T) {
	for _, test := range []struct {
		name   string
		define func()
		want   string
	}{
		{"inline type", func() {
			Component("quality", func() {
				goadsl.Description("Checks an answer.")
				Observation(messageShape)
				Check("present", "The answer exists.")
			})
		}, "must use a named Goa type"},
		{"no assertions", func() {
			answer := goadsl.Type("AnswerObservation", messageShape)
			Component("quality", func() {
				goadsl.Description("Checks an answer.")
				Observation(answer)
			})
		}, "at least one Check or Requirement"},
		{"wrong type", func() { componentBinding("other", false) }, "must select the component's named observation type"},
		{"unknown field", func() { componentBinding("missing", false) }, "unknown field"},
		{"missing component", func() { componentBinding("answer", true) }, "requires a Component"},
	} {
		t.Run(test.name, func(t *testing.T) {
			setup(t)
			require.True(t, eval.Execute(test.define, nil), eval.Context.Error())
			assert.ErrorContains(t, eval.RunDSL(), test.want)
		})
	}
}

func componentBinding(selector string, missing bool) {
	answer := goadsl.Type("AnswerObservation", messageShape)
	component := Component("quality", func() {
		goadsl.Description("Checks an answer.")
		Observation(answer)
		Check("present", "The answer exists.")
	})
	if missing {
		component = nil
	}
	Suite("messages", func() {
		goadsl.Description("Checks a captured flow.")
		goadsl.Timeout("1m")
		Scenario("flow", func() {
			goadsl.Description("Captures one answer.")
			Observation(func() {
				goadsl.Attribute("answer", answer, "Captured answer.")
				goadsl.Attribute("other", goadsl.String, "Other captured value.")
			})
			Assess("answer", component, selector)
		})
	})
}
