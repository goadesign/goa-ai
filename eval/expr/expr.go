// Package expr defines the evaluated representation of generic eval suites.
// The Goa DSL engine populates these values before eval code generation runs.
package expr

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	goacodegen "goa.design/goa/v3/codegen"
	"goa.design/goa/v3/eval"
	goaexpr "goa.design/goa/v3/expr"

	agentexpr "goa.design/goa-ai/expr/agent"
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
		// Agent is the optional agent whose compile-time tool contracts are
		// reachable from the suite.
		Agent *agentexpr.AgentExpr
		// Timeout applies to every scenario in the suite.
		Timeout time.Duration
		// Scenarios are the suite cases in declaration order.
		Scenarios []*ScenarioExpr
	}

	// ScenarioExpr describes one application scenario.
	ScenarioExpr struct {
		eval.DSLFunc
		// Name is the stable scenario identifier.
		Name string
		// Description explains the evaluated behavior.
		Description string
		// Input declares the optional typed value passed to the generated
		// hook method.
		Input *goaexpr.AttributeExpr
		// Observation describes the evidence returned by the capture hook,
		// including unsuccessful product outcomes.
		Observation *goaexpr.AttributeExpr
		// Checks are exact assertions implemented by typed Go predicates.
		Checks []*CheckExpr
		// Requirements are semantic assertions fixed by the design.
		Requirements []*RequirementExpr
		// Tags classify the scenario for runner selection.
		Tags []string
		// Timeout overrides the suite timeout when non-zero.
		Timeout time.Duration
		// Suite is the owning suite.
		Suite *SuiteExpr
	}

	// CheckExpr declares one exact assertion over a captured observation.
	CheckExpr struct {
		// Name identifies the assertion within its scenario.
		Name string
		// Description explains what the predicate must establish.
		Description string
	}

	// RequirementExpr declares the content to assess and the captured facts
	// available to the assessor. Selectors are checked before code generation.
	RequirementExpr struct {
		eval.DSLFunc
		// Name identifies the requirement within its scenario.
		Name string
		// Statement describes the behavior the subject must establish.
		Statement string
		// Subject selects the content being assessed.
		Subject string
		// Evidence selects factual context, which cannot supply missing content.
		Evidence []string
		// ForEach selects an array whose elements are assessed independently.
		ForEach string
		// Scenario owns the observation schema and this requirement.
		Scenario *ScenarioExpr
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

// DependsOn implements eval.Root.
func (r *RootExpr) DependsOn() []eval.Root {
	return []eval.Root{goaexpr.Root, agentexpr.Root}
}

// Packages identifies the DSL package for source-aware diagnostics.
func (r *RootExpr) Packages() []string {
	return []string{"goa.design/goa-ai/eval/dsl"}
}

// WalkSets exposes suites and scenarios in evaluation order.
func (r *RootExpr) WalkSets(walk eval.SetWalker) {
	walk(eval.ToExpressionSet(r.Suites))
	for _, suite := range r.Suites {
		walk(eval.ToExpressionSet(suite.Scenarios))
		for _, scenario := range suite.Scenarios {
			walk(eval.ToExpressionSet(scenario.Requirements))
		}
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
	for _, scenario := range s.Scenarios {
		for _, check := range scenario.Checks {
			method := "Check" + goacodegen.Goify(scenario.Name, true) + goacodegen.Goify(check.Name, true)
			if other, exists := methods[method]; exists {
				verr.Add(scenario, "check %q and scenario %q both generate hook method %q", check.Name, other.Name, method)
			}
			methods[method] = scenario
		}
	}
	return verr
}

// SetDescription implements expr.DescriptionHolder.
func (s *SuiteExpr) SetDescription(description string) {
	s.Description = description
}

// SetTimeout implements expr.TimeoutHolder.
func (s *SuiteExpr) SetTimeout(duration string) error {
	timeout, err := parseTimeout(duration)
	if err != nil {
		return err
	}
	s.Timeout = timeout
	return nil
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
	if s.Input != nil && s.Input.Type == goaexpr.Empty {
		verr.Add(s, "scenario input must define at least one attribute")
	}
	if inputContainsUnion(s.Input, make(map[string]struct{})) {
		verr.Add(s, "scenario input does not support OneOf")
	}
	if s.Observation == nil || s.Observation.Type == goaexpr.Empty {
		verr.Add(s, "scenario Observation is required")
	} else if inputContainsUnion(s.Observation, make(map[string]struct{})) {
		verr.Add(s, "scenario observation does not support OneOf")
	} else if err := goacodegen.Walk(s.Observation, validateCapturedAttribute); err != nil {
		verr.Add(s, "%s", err)
	}
	if len(s.Checks)+len(s.Requirements) == 0 {
		verr.Add(s, "scenario must declare at least one Check or Requirement")
	}
	names := make(map[string]struct{}, len(s.Checks)+len(s.Requirements))
	methods := make(map[string]string, len(s.Checks))
	for _, check := range s.Checks {
		validateID(verr, s, "check", check.Name)
		if check.Description == "" {
			verr.Add(s, "check %q requires a description", check.Name)
		}
		if _, exists := names[check.Name]; exists {
			verr.Add(s, "duplicate assertion %q", check.Name)
		}
		names[check.Name] = struct{}{}
		method := goacodegen.Goify(check.Name, true)
		if other, exists := methods[method]; exists {
			verr.Add(s, "checks %q and %q generate the same method", other, check.Name)
		}
		methods[method] = check.Name
	}
	for _, requirement := range s.Requirements {
		if _, exists := names[requirement.Name]; exists {
			verr.Add(s, "duplicate assertion %q", requirement.Name)
		}
		names[requirement.Name] = struct{}{}
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

// SetDescription implements expr.DescriptionHolder.
func (s *ScenarioExpr) SetDescription(description string) {
	s.Description = description
}

// SetTimeout implements expr.TimeoutHolder.
func (s *ScenarioExpr) SetTimeout(duration string) error {
	timeout, err := parseTimeout(duration)
	if err != nil {
		return err
	}
	s.Timeout = timeout
	return nil
}

// EvalName identifies a semantic requirement in a design error.
func (r *RequirementExpr) EvalName() string {
	return fmt.Sprintf("requirement %q in %s", r.Name, r.Scenario.EvalName())
}

// Validate checks selectors against the actual observation shape. Field paths
// are resolved here, so generated assessment code never searches JSON by name.
func (r *RequirementExpr) Validate() error {
	verr := new(eval.ValidationErrors)
	validateID(verr, r, "requirement", r.Name)
	if r.Statement == "" {
		verr.Add(r, "requirement statement is required")
	}
	root := r.Scenario.Observation
	if root == nil {
		return verr
	}
	local := root
	if r.ForEach != "" {
		selected, err := Select(root, root, r.ForEach)
		if err != nil {
			verr.Add(r, "ForEach: %s", err)
			return verr
		}
		array := goaexpr.AsArray(selected.Type)
		if array == nil {
			verr.Add(r, "ForEach %q must select an array", r.ForEach)
			return verr
		}
		local = array.ElemType
	}
	if _, err := Select(root, local, r.Subject); err != nil {
		verr.Add(r, "Subject: %s", err)
	}
	seen := make(map[string]struct{}, len(r.Evidence))
	for _, selector := range r.Evidence {
		if _, err := Select(root, local, selector); err != nil {
			verr.Add(r, "Evidence: %s", err)
		}
		if _, exists := seen[selector]; exists {
			verr.Add(r, "duplicate Evidence selector %q", selector)
		}
		seen[selector] = struct{}{}
	}
	return verr
}

// Select resolves a field path against local, or root when it starts with "$".
// It returns the declared attribute or an error for missing fields, collection
// traversal, or an empty path. "$" selects the root; "@" selects the local value.
func Select(root, local *goaexpr.AttributeExpr, selector string) (*goaexpr.AttributeExpr, error) {
	if selector == "" {
		return nil, fmt.Errorf("field selector is required")
	}
	if selector == "$" {
		return root, nil
	}
	if selector == "@" {
		return local, nil
	}
	if strings.HasPrefix(selector, "$.") {
		local = root
		selector = strings.TrimPrefix(selector, "$.")
	}
	for _, name := range strings.Split(selector, ".") {
		object := goaexpr.AsObject(local.Type)
		if object == nil {
			return nil, fmt.Errorf("%q traverses a value that is not an object", selector)
		}
		field := object.Attribute(name)
		if field == nil {
			return nil, fmt.Errorf("%q selects unknown field %q", selector, name)
		}
		local = field
	}
	return local, nil
}

// validateID reports identifiers outside the canonical lower_snake_case
// vocabulary used for stable selection, reporting, and generated paths.
func validateID(verr *eval.ValidationErrors, expression eval.Expression, kind, value string) {
	if !idRE.MatchString(value) {
		verr.Add(expression, "%s name %q must be lower_snake_case", kind, value)
	}
}

// validateCapturedAttribute rejects defaults because decoding saved evidence
// must not fill in a value that the capture hook never recorded.
func validateCapturedAttribute(attribute *goaexpr.AttributeExpr) error {
	if attribute.DefaultValue != nil {
		return errors.New("observation fields cannot declare defaults; capture the actual value")
	}
	return nil
}

// parseTimeout parses a Go duration literal and rejects non-positive values,
// which have no meaning as evaluation deadlines.
func parseTimeout(duration string) (time.Duration, error) {
	timeout, err := time.ParseDuration(duration)
	if err != nil {
		return 0, err
	}
	if timeout <= 0 {
		return 0, fmt.Errorf("evaluation timeout must be greater than zero")
	}
	return timeout, nil
}

// inputContainsUnion reports whether an input schema requires Goa's separate
// sum-type generation pipeline.
func inputContainsUnion(attribute *goaexpr.AttributeExpr, seen map[string]struct{}) bool {
	if attribute == nil {
		return false
	}
	switch actual := attribute.Type.(type) {
	case goaexpr.UserType:
		if _, ok := seen[actual.ID()]; ok {
			return false
		}
		seen[actual.ID()] = struct{}{}
		return inputContainsUnion(actual.Attribute(), seen)
	case *goaexpr.Object:
		for _, named := range *actual {
			if inputContainsUnion(named.Attribute, seen) {
				return true
			}
		}
	case *goaexpr.Array:
		return inputContainsUnion(actual.ElemType, seen)
	case *goaexpr.Map:
		return inputContainsUnion(actual.KeyType, seen) ||
			inputContainsUnion(actual.ElemType, seen)
	case *goaexpr.Union:
		return true
	}
	return false
}
