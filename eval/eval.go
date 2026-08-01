// Package eval defines the runtime contracts shared by generated evaluation
// suites, application-owned hooks, runners, and semantic judges.
package eval

import (
	"context"
	"errors"
	"fmt"
	"time"
)

type (
	// Label is the semantic relationship between an answer and a claim.
	Label string

	// Check records one deterministic assertion made by an application hook.
	Check struct {
		// Name identifies the asserted invariant within its scenario.
		Name string `json:"name"`
		// Passed reports whether the invariant held.
		Passed bool `json:"passed"`
		// Diagnostic explains a failed assertion.
		Diagnostic string `json:"diagnostic,omitempty"`
	}

	// Claim describes one semantic assertion to judge against model output.
	Claim struct {
		// ID uniquely identifies the claim within its scenario.
		ID string `json:"id"`
		// Text is the proposition the output is expected to entail.
		Text string `json:"text"`
	}

	// Artifact links supporting evidence produced while executing a scenario.
	Artifact struct {
		// Name identifies the evidence within its scenario.
		Name string `json:"name"`
		// URI locates the durable evidence.
		URI string `json:"uri"`
	}

	// Result is returned by an application hook after executing one scenario.
	Result struct {
		// Checks are deterministic assertions over typed application evidence.
		Checks []Check `json:"checks,omitempty"`
		// Claims are semantic assertions to judge against Output.
		Claims []Claim `json:"claims,omitempty"`
		// Output is the model-authored answer evaluated by Claims.
		Output string `json:"output,omitempty"`
		// Artifacts link durable evidence used to diagnose this result.
		Artifacts []Artifact `json:"artifacts,omitempty"`
	}

	// Scenario is one generated evaluation case and its application hook.
	Scenario struct {
		// ID is the stable lower_snake_case scenario identifier.
		ID string
		// Description states the behavior the scenario evaluates.
		Description string
		// Input is the text passed to the application hook.
		Input string
		// Tags classify the scenario for explicit runner selection.
		Tags []string
		// Timeout is the maximum duration of the hook and its judgments.
		Timeout time.Duration
		// Run executes the application-owned scenario behavior.
		Run func(context.Context, string) (Result, error)
	}

	// Calibration is a labeled example that proves a judge before scenarios run.
	Calibration struct {
		// ID is the stable lower_snake_case calibration identifier.
		ID string
		// Answer is the example output presented to the judge.
		Answer string
		// Claim is the proposition labeled by Want.
		Claim string
		// Want is the required judgment for the example.
		Want Label
	}

	// Suite is an immutable generated collection of scenarios and calibrations.
	Suite struct {
		// ID is the stable lower_snake_case suite identifier.
		ID string
		// Description states the capability evaluated by the suite.
		Description string
		// Scenarios are the generated cases in declaration order.
		Scenarios []Scenario
		// Calibrations are the generated judge examples in declaration order.
		Calibrations []Calibration
	}

	// Judgment is the semantic label assigned to one claim.
	Judgment struct {
		// ClaimID identifies the judged claim.
		ClaimID string `json:"claim_id"`
		// Label is the answer-to-claim relationship.
		Label Label `json:"label"`
		// Rationale concisely explains the label.
		Rationale string `json:"rationale"`
	}

	// Assertion binds one output to one semantic claim for batched judging.
	Assertion struct {
		// ClaimID uniquely identifies the assertion within the request.
		ClaimID string `json:"claim_id"`
		// Output is the model-authored text being assessed.
		Output string `json:"output"`
		// Claim is the proposition to classify against Output.
		Claim string `json:"claim"`
	}

	// Judge assigns exactly one semantic judgment to each supplied claim.
	Judge interface {
		Judge(context.Context, []Assertion) ([]Judgment, error)
	}

	// ScenarioReport records the outcome and evidence for one scenario.
	ScenarioReport struct {
		// ID identifies the scenario.
		ID string `json:"id"`
		// StartedAt is when execution began.
		StartedAt time.Time `json:"started_at"`
		// Duration is the total scenario duration.
		Duration time.Duration `json:"duration"`
		// Result is the validated hook result, when execution reached it.
		Result *Result `json:"result,omitempty"`
		// Judgments contains one entry per semantic claim.
		Judgments []Judgment `json:"judgments,omitempty"`
		// Error describes an execution, protocol, or judging failure.
		Error string `json:"error,omitempty"`
		// Passed reports whether all deterministic and semantic assertions passed.
		Passed bool `json:"passed"`
	}

	// Report records a complete suite run.
	Report struct {
		// SuiteID identifies the generated suite.
		SuiteID string `json:"suite_id"`
		// StartedAt is when calibration began.
		StartedAt time.Time `json:"started_at"`
		// Duration is the total suite duration.
		Duration time.Duration `json:"duration"`
		// Scenarios contains results in generated declaration order.
		Scenarios []ScenarioReport `json:"scenarios"`
		// Error describes a suite-level selection or calibration failure.
		Error string `json:"error,omitempty"`
		// Passed reports whether calibration and all selected scenarios passed.
		Passed bool `json:"passed"`
	}

	// Runner executes generated suites using one semantic judge.
	Runner struct {
		judge Judge
		now   func() time.Time
	}
)

const (
	// Entailed means the answer establishes the claim.
	Entailed Label = "entailed"
	// Contradicted means the answer establishes the claim is false.
	Contradicted Label = "contradicted"
	// NotAddressed means the answer neither establishes nor contradicts the claim.
	NotAddressed Label = "not_addressed"
	// Indeterminate means the answer is too ambiguous to classify.
	Indeterminate Label = "indeterminate"
)

var errCalibration = errors.New("judge calibration failed")

// NewRunner creates a suite runner that uses judge for semantic assertions.
func NewRunner(judge Judge) *Runner {
	return &Runner{judge: judge, now: time.Now}
}

// Run validates the judge against every calibration, then executes selected
// scenarios sequentially. An empty tag selection runs the complete suite.
func (r *Runner) Run(ctx context.Context, suite Suite, tags ...string) (Report, error) {
	started := r.now()
	report := Report{SuiteID: suite.ID, StartedAt: started}
	wanted := make(map[string]struct{}, len(tags))
	for _, tag := range tags {
		wanted[tag] = struct{}{}
	}
	selectedScenarios := make([]Scenario, 0, len(suite.Scenarios))
	for _, scenario := range suite.Scenarios {
		if selected(scenario.Tags, wanted) {
			selectedScenarios = append(selectedScenarios, scenario)
		}
	}
	if len(selectedScenarios) == 0 {
		err := errors.New("evaluation selection contains no scenarios")
		report.Error = err.Error()
		report.Duration = r.now().Sub(started)
		return report, err
	}
	if err := r.calibrate(ctx, suite.Calibrations); err != nil {
		report.Error = err.Error()
		report.Duration = r.now().Sub(started)
		return report, err
	}

	for _, scenario := range selectedScenarios {
		report.Scenarios = append(report.Scenarios, r.runScenario(ctx, scenario))
	}
	report.Duration = r.now().Sub(started)
	report.Passed = true
	for _, scenario := range report.Scenarios {
		report.Passed = report.Passed && scenario.Passed
	}
	return report, nil
}

// ValidateResult enforces the hook-to-runner boundary contract.
func ValidateResult(result Result) error {
	if len(result.Checks) == 0 && len(result.Claims) == 0 {
		return errors.New("result must contain at least one check or claim")
	}
	checks := make(map[string]struct{}, len(result.Checks))
	for _, check := range result.Checks {
		if check.Name == "" {
			return errors.New("check name is required")
		}
		if _, exists := checks[check.Name]; exists {
			return fmt.Errorf("duplicate check %q", check.Name)
		}
		checks[check.Name] = struct{}{}
		if check.Passed && check.Diagnostic != "" {
			return fmt.Errorf("passed check %q must not have a diagnostic", check.Name)
		}
		if !check.Passed && check.Diagnostic == "" {
			return fmt.Errorf("failed check %q requires a diagnostic", check.Name)
		}
	}
	if len(result.Claims) > 0 && result.Output == "" {
		return errors.New("claims require output")
	}
	claims := make(map[string]struct{}, len(result.Claims))
	for _, claim := range result.Claims {
		if claim.ID == "" || claim.Text == "" {
			return errors.New("claim ID and text are required")
		}
		if _, exists := claims[claim.ID]; exists {
			return fmt.Errorf("duplicate claim %q", claim.ID)
		}
		claims[claim.ID] = struct{}{}
	}
	artifacts := make(map[string]struct{}, len(result.Artifacts))
	for _, artifact := range result.Artifacts {
		if artifact.Name == "" || artifact.URI == "" {
			return errors.New("artifact name and URI are required")
		}
		if _, exists := artifacts[artifact.Name]; exists {
			return fmt.Errorf("duplicate artifact %q", artifact.Name)
		}
		artifacts[artifact.Name] = struct{}{}
	}
	return nil
}

// ValidateJudgments enforces an exact one-to-one relationship between claims
// and semantic judge output.
func ValidateJudgments(claims []Claim, judgments []Judgment) error {
	wanted := make(map[string]struct{}, len(claims))
	for _, claim := range claims {
		if claim.ID == "" || claim.Text == "" {
			return errors.New("claim ID and text are required")
		}
		if _, exists := wanted[claim.ID]; exists {
			return fmt.Errorf("duplicate claim %q", claim.ID)
		}
		wanted[claim.ID] = struct{}{}
	}
	if len(judgments) != len(wanted) {
		return fmt.Errorf("got %d judgments for %d claims", len(judgments), len(wanted))
	}
	seen := make(map[string]struct{}, len(judgments))
	for _, judgment := range judgments {
		if _, exists := wanted[judgment.ClaimID]; !exists {
			return fmt.Errorf("judgment references unknown claim %q", judgment.ClaimID)
		}
		if _, exists := seen[judgment.ClaimID]; exists {
			return fmt.Errorf("duplicate judgment for claim %q", judgment.ClaimID)
		}
		if !validLabel(judgment.Label) {
			return fmt.Errorf("judgment for claim %q has invalid label %q", judgment.ClaimID, judgment.Label)
		}
		if judgment.Rationale == "" {
			return fmt.Errorf("judgment for claim %q requires a rationale", judgment.ClaimID)
		}
		seen[judgment.ClaimID] = struct{}{}
	}
	return nil
}

// calibrate proves the judge contract with one batched request before any
// application scenario is allowed to run.
func (r *Runner) calibrate(ctx context.Context, calibrations []Calibration) error {
	if len(calibrations) == 0 {
		return nil
	}
	if r.judge == nil {
		return fmt.Errorf("%w: semantic judge is required", errCalibration)
	}
	assertions := make([]Assertion, len(calibrations))
	claims := make([]Claim, len(calibrations))
	for i, calibration := range calibrations {
		assertions[i] = Assertion{
			ClaimID: calibration.ID,
			Output:  calibration.Answer,
			Claim:   calibration.Claim,
		}
		claims[i] = Claim{ID: calibration.ID, Text: calibration.Claim}
	}
	judgments, err := r.judge.Judge(ctx, assertions)
	if err != nil {
		return fmt.Errorf("%w: %w", errCalibration, err)
	}
	if err := ValidateJudgments(claims, judgments); err != nil {
		return fmt.Errorf("%w: %w", errCalibration, err)
	}
	byID := make(map[string]Label, len(judgments))
	for _, judgment := range judgments {
		byID[judgment.ClaimID] = judgment.Label
	}
	for _, calibration := range calibrations {
		if got := byID[calibration.ID]; got != calibration.Want {
			return fmt.Errorf("%w: %s: got %s, want %s", errCalibration, calibration.ID, got, calibration.Want)
		}
	}
	return nil
}

// runScenario executes one hook under its generated timeout and evaluates its
// validated deterministic and semantic assertions.
func (r *Runner) runScenario(ctx context.Context, scenario Scenario) ScenarioReport {
	started := r.now()
	report := ScenarioReport{ID: scenario.ID, StartedAt: started}
	scenarioCtx, cancel := context.WithTimeout(ctx, scenario.Timeout)
	defer cancel()
	result, err := scenario.Run(scenarioCtx, scenario.Input)
	report.Duration = r.now().Sub(started)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	if err := ValidateResult(result); err != nil {
		report.Error = err.Error()
		return report
	}
	report.Result = &result
	passed := true
	for _, check := range result.Checks {
		passed = passed && check.Passed
	}
	if len(result.Claims) > 0 {
		if r.judge == nil {
			report.Error = "semantic judge is required"
			return report
		}
		assertions := make([]Assertion, len(result.Claims))
		for i, claim := range result.Claims {
			assertions[i] = Assertion{ClaimID: claim.ID, Output: result.Output, Claim: claim.Text}
		}
		judgments, err := r.judge.Judge(scenarioCtx, assertions)
		if err != nil {
			report.Error = err.Error()
			return report
		}
		if err := ValidateJudgments(result.Claims, judgments); err != nil {
			report.Error = err.Error()
			return report
		}
		report.Judgments = judgments
		for _, judgment := range judgments {
			passed = passed && judgment.Label == Entailed
		}
	}
	report.Passed = passed
	return report
}

// selected reports whether a scenario contains at least one explicitly
// requested tag. With no requested tags, every scenario is selected.
func selected(tags []string, wanted map[string]struct{}) bool {
	if len(wanted) == 0 {
		return true
	}
	for _, tag := range tags {
		if _, ok := wanted[tag]; ok {
			return true
		}
	}
	return false
}

// validLabel reports whether a label belongs to the closed judge vocabulary.
func validLabel(label Label) bool {
	switch label {
	case Entailed, Contradicted, NotAddressed, Indeterminate:
		return true
	default:
		return false
	}
}
