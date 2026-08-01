// Package expr defines the evaluated representation of generic eval suites.
// The Goa DSL engine populates these values before eval code generation runs.
package expr

import (
	"fmt"
	"regexp"
	"time"

	aieval "goa.design/goa-ai/eval"
	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
)

type (
	// RootExpr contains every generic evaluation suite in a design.
	RootExpr struct {
		// Suites are retained in declaration order.
		Suites []*SuiteExpr
	}

	// SuiteExpr describes one generated evaluation suite.
	SuiteExpr struct {
		eval.DSLFunc
		// Name is the stable suite identifier.
		Name string
		// Description explains the evaluated capability.
		Description string
		// Timeout applies to every scenario in the suite.
		Timeout time.Duration
		// Scenarios are the suite cases in declaration order.
		Scenarios []*ScenarioExpr
		// Calibrations prove the semantic judge before scenarios execute.
		Calibrations []*CalibrationExpr
	}

	// ScenarioExpr describes one text-input application scenario.
	ScenarioExpr struct {
		eval.DSLFunc
		// Name is the stable scenario identifier.
		Name string
		// Description explains the evaluated behavior.
		Description string
		// Input is passed unchanged to the generated hook method.
		Input string
		// Tags classify the scenario for runner selection.
		Tags []string
		// Timeout overrides the suite timeout when non-zero.
		Timeout time.Duration
		// Suite is the owning suite.
		Suite *SuiteExpr
	}

	// CalibrationExpr describes one labeled semantic judge example.
	CalibrationExpr struct {
		eval.DSLFunc
		// Name is the stable calibration identifier.
		Name string
		// Answer is the example model output.
		Answer string
		// Claim is the proposition being labeled.
		Claim string
		// Want is the required semantic label.
		Want aieval.Label
		// Suite is the owning suite.
		Suite *SuiteExpr
	}
)

var (
	// Root is the registered generic evaluation DSL root.
	Root *RootExpr
	idRE = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)
)

func init() {
	Root = new(RootExpr)
	if err := eval.Register(Root); err != nil {
		panic(err)
	}
}

// EvalName implements eval.Expression.
func (r *RootExpr) EvalName() string {
	return "evaluation suites root"
}

// DependsOn implements eval.Root. Generic suites do not depend on Goa service
// expressions or application-specific design roots.
func (r *RootExpr) DependsOn() []eval.Root {
	return nil
}

// Packages identifies the DSL package for source-aware diagnostics.
func (r *RootExpr) Packages() []string {
	return []string{"goa.design/goa-ai/eval/dsl"}
}

// WalkSets exposes suites, scenarios, and calibrations in evaluation order.
func (r *RootExpr) WalkSets(walk eval.SetWalker) {
	walk(eval.ToExpressionSet(r.Suites))
	for _, suite := range r.Suites {
		walk(eval.ToExpressionSet(suite.Scenarios))
		walk(eval.ToExpressionSet(suite.Calibrations))
	}
}

// Validate enforces identities that must be unique across the entire design.
func (r *RootExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	suites := make(map[string]*SuiteExpr, len(r.Suites))
	for _, suite := range r.Suites {
		if other, ok := suites[suite.Name]; ok {
			verr.Add(suite, "suite name %q duplicates %s", suite.Name, other.EvalName())
		}
		suites[suite.Name] = suite
	}
	return verr
}

// EvalName implements eval.Expression.
func (s *SuiteExpr) EvalName() string {
	return fmt.Sprintf("evaluation suite %q", s.Name)
}

// Validate enforces the complete static contract for generated suite data.
func (s *SuiteExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	validateID(verr, s, "suite", s.Name)
	if s.Description == "" {
		verr.Add(s, "suite description is required")
	}
	if s.Timeout <= 0 {
		verr.Add(s, "suite timeout must be greater than zero")
	}
	if len(s.Scenarios) == 0 {
		verr.Add(s, "suite must define at least one scenario")
	}
	scenarios := make(map[string]*ScenarioExpr, len(s.Scenarios))
	methods := make(map[string]*ScenarioExpr, len(s.Scenarios))
	for _, scenario := range s.Scenarios {
		if other, ok := scenarios[scenario.Name]; ok {
			verr.Add(scenario, "scenario name %q duplicates %s", scenario.Name, other.EvalName())
		}
		scenarios[scenario.Name] = scenario
		method := goacodegen.Goify(scenario.Name, true)
		if other, ok := methods[method]; ok {
			verr.Add(scenario, "scenario name %q and %q both generate hook method %q", other.Name, scenario.Name, method)
		}
		methods[method] = scenario
	}
	calibrations := make(map[string]*CalibrationExpr, len(s.Calibrations))
	for _, calibration := range s.Calibrations {
		if other, ok := calibrations[calibration.Name]; ok {
			verr.Add(calibration, "calibration name %q duplicates %s", calibration.Name, other.EvalName())
		}
		calibrations[calibration.Name] = calibration
	}
	return verr
}

// EvalName implements eval.Expression.
func (s *ScenarioExpr) EvalName() string {
	return fmt.Sprintf("scenario %q in suite %q", s.Name, s.Suite.Name)
}

// Validate enforces the scenario input and descriptive contracts.
func (s *ScenarioExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	validateID(verr, s, "scenario", s.Name)
	if s.Description == "" {
		verr.Add(s, "scenario description is required")
	}
	if s.Input == "" {
		verr.Add(s, "scenario input is required")
	}
	if s.Timeout < 0 {
		verr.Add(s, "scenario timeout must be greater than zero")
	}
	tags := make(map[string]struct{}, len(s.Tags))
	for _, tag := range s.Tags {
		validateID(verr, s, "tag", tag)
		if _, ok := tags[tag]; ok {
			verr.Add(s, "duplicate tag %q", tag)
		}
		tags[tag] = struct{}{}
	}
	return verr
}

// EvalName implements eval.Expression.
func (c *CalibrationExpr) EvalName() string {
	return fmt.Sprintf("calibration %q in suite %q", c.Name, c.Suite.Name)
}

// Validate enforces the labeled semantic example contract.
func (c *CalibrationExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	validateID(verr, c, "calibration", c.Name)
	if c.Answer == "" {
		verr.Add(c, "calibration answer is required")
	}
	if c.Claim == "" {
		verr.Add(c, "calibration claim is required")
	}
	switch c.Want {
	case aieval.Entailed, aieval.Contradicted, aieval.NotAddressed, aieval.Indeterminate:
	default:
		verr.Add(c, "calibration want must be a semantic eval label")
	}
	return verr
}

// validateID reports identifiers outside the canonical lower_snake_case
// vocabulary used for stable selection, reporting, and generated paths.
func validateID(verr *eval.ValidationErrors, expression eval.Expression, kind, value string) {
	if !idRE.MatchString(value) {
		verr.Add(expression, "%s name %q must be lower_snake_case", kind, value)
	}
}
