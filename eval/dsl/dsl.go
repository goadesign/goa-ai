// Package dsl defines generic evaluation suites whose scenario hooks are
// generated as direct Go interfaces.
package dsl

import (
	_ "goa.design/goa-ai/eval/codegen"
	evalexpr "goa.design/goa-ai/eval/expr"
	agentexpr "goa.design/goa-ai/expr/agent"
	"goa.design/goa-ai/internal/dslshape"
	"goa.design/goa/v3/eval"
)

// Suite defines an evaluation suite of stable scenarios. goa gen emits one
// typed hook method per scenario together with input types, validation, and a
// validating constructor under gen/evals/<name>. goa example creates an
// application-owned cmd/<name>-evals command once.
//
// Suite must appear at the design top level or in Agent. When Suite appears in
// Agent the generated package also exposes MustToolContract, a lookup over the
// tool contracts statically reachable from that agent.
//
// Suite accepts two arguments: the suite name in lower_snake_case and the
// defining DSL function.
//
// Example:
//
//	var _ = Service("assistant_service", func() {
//	    Agent("assistant", "Answers user questions.", func() {
//	        Suite("assistant_quality", func() {
//	            Description("Exercises assistant outcomes.")
//	            Timeout("2m")
//	            Scenario("record_inventory", func() {
//	                Description("Retrieves every record in a fixed window.")
//	                Input(RecordEvalInput)
//	                Tags("integration", "records")
//	            })
//	        })
//	    })
//	})
func Suite(name string, fn func()) *evalexpr.SuiteExpr {
	current := eval.Current()
	agent, attached := current.(*agentexpr.AgentExpr)
	if current != eval.Top && !attached {
		eval.IncompatibleDSL()
		return nil
	}
	suite := &evalexpr.SuiteExpr{Name: name, Agent: agent, DSLFunc: fn}
	evalexpr.Root.Suites = append(evalexpr.Root.Suites, suite)
	return suite
}

// Scenario defines one evaluation case. Each scenario generates one hook
// method that the application implements to execute the real product and
// return deterministic checks and semantic claims.
//
// Scenario must appear in Suite.
//
// Scenario accepts two arguments: the scenario name in lower_snake_case and
// the defining DSL function. The DSL function requires Description and may set
// Input, Tags, and a Timeout overriding the suite timeout.
//
// Example:
//
//	Scenario("record_inventory", func() {
//	    Description("Retrieves every record in a fixed window.")
//	    Input(RecordEvalInput)
//	    Tags("integration", "records")
//	    Timeout("3m")
//	})
func Scenario(name string, fn func()) *evalexpr.ScenarioExpr {
	suite, ok := eval.Current().(*evalexpr.SuiteExpr)
	if !ok {
		eval.IncompatibleDSL()
		return nil
	}
	scenario := &evalexpr.ScenarioExpr{Name: name, Suite: suite, DSLFunc: fn}
	suite.Scenarios = append(suite.Scenarios, scenario)
	return scenario
}

// Input declares the typed value passed to the generated scenario hook. Input
// declares a schema, not a fixture value: application code supplies the
// concrete value through the generated Inputs constructor, which validates it
// before any scenario runs. A scenario without Input generates a hook that
// receives only a context.
//
// Input must appear in Scenario.
//
// Input accepts the same forms as tool Args: a user type, a primitive, array,
// or map type, or an inline attribute function, optionally followed by a
// description string and a DSL function customizing the type. A customized
// user type generates a scenario-specific copy. OneOf is not supported in
// evaluation inputs.
//
// Example:
//
//	// User type:
//	Input(QueryEvalInput)
//
//	// Customized user type:
//	Input(QueryEvalInput, func() {
//	    Required("query")
//	})
//
//	// Inline object:
//	Input(func() {
//	    Attribute("query", String, "Assistant request.", func() {
//	        MinLength(1)
//	    })
//	    Required("query")
//	})
func Input(value any, args ...any) {
	scenario, ok := eval.Current().(*evalexpr.ScenarioExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	scenario.Input = dslshape.Build(scenario.Name, "Input", value, args...)
}
