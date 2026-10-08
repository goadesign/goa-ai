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
// typed capture hook per scenario, observation codecs, exact predicates, and
// compiled requirement selectors under gen/evals/<name>. goa example creates
// an application-owned cmd/<name>-evals command once.
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
//	                Observation(RecordObservation)
//	                Check("complete", "All requested records were captured.")
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

// Component declares reusable assertions over a named Goa observation type.
// It must appear at the design top level and requires Description, Observation,
// and at least one Check or Requirement. It does not execute or capture a run.
// Assess applies these assertions to saved evidence in one or more scenarios.
func Component(name string, fn func()) *evalexpr.ComponentExpr {
	if eval.Current() != eval.Top {
		eval.IncompatibleDSL()
		return nil
	}
	component := &evalexpr.ComponentExpr{Name: name, DSLFunc: fn}
	evalexpr.Root.Components = append(evalexpr.Root.Components, component)
	return component
}

// Assess applies a component to one field of the scenario observation.
// The selector must resolve to the component's named Goa type; "$" selects the
// complete observation. Each use has a distinct name, while its typed check
// methods are shared. Missing selected evidence is an assessment error.
func Assess(name string, component *evalexpr.ComponentExpr, selector string) {
	scenario, ok := eval.Current().(*evalexpr.ScenarioExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	scenario.Assessments = append(scenario.Assessments, &evalexpr.AssessmentExpr{
		Name: name, Component: component, Selector: selector,
	})
}

// Scenario defines one evaluation case. Each scenario generates one hook
// method that the application implements to execute the real product and
// return a typed observation. Checks and requirements are declared in the
// design, so returning less evidence cannot remove an assertion.
//
// Scenario must appear in Suite.
//
// Scenario accepts two arguments: the scenario name in lower_snake_case and
// the defining DSL function. The DSL function requires Description and may set
// Input, Tags, and a Timeout overriding the suite timeout. Observation and at
// least one Check, Requirement, or Assess are required.
//
// Example:
//
//	Scenario("record_inventory", func() {
//	    Description("Retrieves every record in a fixed window.")
//	    Input(RecordEvalInput)
//	    Observation(RecordObservation)
//	    Check("complete", "All requested records were captured.")
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

// Observation declares the typed evidence returned by the scenario hook.
// Describe possible outcomes, including empty answers and failed product
// operations; Check and Requirement describe which of those outcomes pass.
// It accepts the same type forms as Input and must appear once in Scenario or
// Component. A Component requires a named Goa type so all uses share that type.
func Observation(value any, args ...any) {
	name, assertions := currentAssertions()
	if assertions == nil {
		eval.IncompatibleDSL()
		return
	}
	if assertions.Observation != nil {
		eval.ReportError("Observation may appear only once")
		return
	}
	assertions.Observation = dslshape.Build(name, "Observation", value, args...)
}

// Check declares an exact assertion over the scenario's observation. The
// generated Checks interface requires a typed predicate that returns an empty
// diagnostic on success, or an explanation on failure. Predicates receive
// only saved evidence and must not call the product or reload reference data.
func Check(name, description string) {
	_, assertions := currentAssertions()
	if assertions == nil {
		eval.IncompatibleDSL()
		return
	}
	assertions.Checks = append(assertions.Checks, &evalexpr.CheckExpr{
		Name: name, Description: description,
	})
}

// Requirement declares one semantic assertion and the captured fields used to
// assess it. Its defining function requires Subject and may specify Evidence
// and ForEach. The statement is fixed when the suite is generated.
func Requirement(name, statement string, fn func()) {
	_, assertions := currentAssertions()
	if assertions == nil {
		eval.IncompatibleDSL()
		return
	}
	assertions.Requirements = append(assertions.Requirements, &evalexpr.RequirementExpr{
		Name: name, Statement: statement, DSLFunc: fn, Owner: assertions,
	})
}

// Reasoning requires an independent reasoning model for this requirement.
// Native predictions cannot decide it or trigger extra adjudication. Use it for
// assessments that need broader interpretation, such as satisfying the user's
// complete request. It must appear once inside Requirement.
func Reasoning() {
	requirement, ok := eval.Current().(*evalexpr.RequirementExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if requirement.Reasoning {
		eval.ReportError("Reasoning may appear only once")
		return
	}
	requirement.Reasoning = true
}

// Subject selects the observation content a requirement assesses. A dotted
// path selects nested fields; "$" selects the complete observation. Inside
// ForEach, "@" selects the complete element and paths are relative to it unless
// prefixed with "$.".
// Factual context belongs in Evidence and cannot satisfy missing subject text.
func Subject(selector string) {
	requirement, ok := eval.Current().(*evalexpr.RequirementExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if requirement.Subject != "" {
		eval.ReportError("Subject may appear only once")
		return
	}
	requirement.Subject = selector
}

// Evidence selects captured facts needed to assess a requirement. Each path
// follows Subject's selector rules. All selected values must be present when
// assessment runs; missing context is an error, not a passing judgment. Empty
// arrays and maps are valid values. Capture availability in an explicit field
// when an empty collection must be distinguished from unavailable facts.
func Evidence(selectors ...string) {
	requirement, ok := eval.Current().(*evalexpr.RequirementExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if len(selectors) == 0 {
		eval.ReportError("Evidence requires at least one selector")
		return
	}
	requirement.Evidence = append(requirement.Evidence, selectors...)
}

// ForEach assesses a requirement separately for each captured array element.
// An empty array has no instances; declare a Check when existence is required.
// This function must appear at most once in a Requirement.
func ForEach(selector string) {
	requirement, ok := eval.Current().(*evalexpr.RequirementExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	if selector == "" || requirement.ForEach != "" {
		eval.ReportError("ForEach requires one non-empty array selector")
		return
	}
	requirement.ForEach = selector
}

// currentAssertions returns the declaration that owns checks and requirements.
// Other DSL contexts cannot add assertions or change captured field types.
func currentAssertions() (string, *evalexpr.AssertionExpr) {
	switch current := eval.Current().(type) {
	case *evalexpr.ScenarioExpr:
		return current.Name, &current.AssertionExpr
	case *evalexpr.ComponentExpr:
		return current.Name, &current.AssertionExpr
	default:
		return "", nil
	}
}
