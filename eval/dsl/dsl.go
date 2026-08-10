// Package dsl defines generic evaluation suites whose scenario hooks are
// generated as direct Go interfaces.
package dsl

import (
	"time"

	_ "goa.design/goa-ai/eval/codegen"
	evalexpr "goa.design/goa-ai/eval/expr"
	"goa.design/goa/v3/eval"
)

// Suite defines a generic text-input evaluation suite at the design top level.
func Suite(name string, fn func()) *evalexpr.SuiteExpr {
	if eval.Current() != eval.Top {
		eval.IncompatibleDSL()
		return nil
	}
	suite := &evalexpr.SuiteExpr{Name: name, DSLFunc: fn}
	evalexpr.Root.Suites = append(evalexpr.Root.Suites, suite)
	return suite
}

// Scenario defines one application-owned behavior within the current suite.
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

// Description sets the current suite or scenario description.
func Description(value string) {
	switch current := eval.Current().(type) {
	case *evalexpr.SuiteExpr:
		current.Description = value
	case *evalexpr.ScenarioExpr:
		current.Description = value
	default:
		eval.IncompatibleDSL()
	}
}

// Timeout sets the current suite default or scenario-specific timeout from a
// Go duration literal.
func Timeout(value string) {
	duration, err := time.ParseDuration(value)
	if err != nil {
		eval.ReportError("invalid evaluation timeout %q: %s", value, err)
		return
	}
	if duration <= 0 {
		eval.ReportError("evaluation timeout %q must be greater than zero", value)
		return
	}
	switch current := eval.Current().(type) {
	case *evalexpr.SuiteExpr:
		current.Timeout = duration
	case *evalexpr.ScenarioExpr:
		current.Timeout = duration
	default:
		eval.IncompatibleDSL()
	}
}

// Input sets the text passed unchanged to the current scenario hook.
func Input(value string) {
	scenario, ok := eval.Current().(*evalexpr.ScenarioExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	scenario.Input = value
}

// Tags classifies the current scenario for explicit runner selection.
func Tags(values ...string) {
	scenario, ok := eval.Current().(*evalexpr.ScenarioExpr)
	if !ok {
		eval.IncompatibleDSL()
		return
	}
	scenario.Tags = append(scenario.Tags, values...)
}
