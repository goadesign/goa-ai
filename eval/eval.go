// Package eval defines the runtime contracts shared by generated evaluation
// suites, application-owned hooks, runners, and semantic judges.
package eval

import (
	"context"
	"errors"
	"fmt"
	"sync"
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

	// Suite is a generated collection of scenarios.
	Suite struct {
		// ID is the stable lower_snake_case suite identifier.
		ID string
		// Description states the capability evaluated by the suite.
		Description string
		// Scenarios are the generated cases in declaration order.
		Scenarios []Scenario
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

	// Judge assigns exactly one semantic judgment to each supplied claim. A
	// runner may call Judge concurrently for independent scenarios.
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
		// StartedAt is when suite execution began.
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
		judge          Judge
		maxConcurrency int
		now            func() time.Time
	}

	// RunnerConfig defines how many independent scenarios a runner may execute
	// at once.
	RunnerConfig struct {
		// MaxConcurrency is the positive maximum number of scenarios in flight.
		MaxConcurrency int
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

// NewRunner creates a suite runner with an explicit concurrency limit. A nil
// judge is valid for suites whose hooks return deterministic checks only.
func NewRunner(judge Judge, config RunnerConfig) (*Runner, error) {
	if config.MaxConcurrency <= 0 {
		return nil, errors.New("maximum evaluation concurrency must be greater than zero")
	}
	return &Runner{
		judge:          judge,
		maxConcurrency: config.MaxConcurrency,
		now:            time.Now,
	}, nil
}

// Run executes every scenario in suite.
func (r *Runner) Run(ctx context.Context, suite Suite) (Report, error) {
	return r.run(ctx, suite, suite.Scenarios)
}

// RunScenarios executes the named scenarios in suite declaration order. It
// rejects an empty selection, duplicate IDs, and IDs absent from the suite.
func (r *Runner) RunScenarios(ctx context.Context, suite Suite, ids ...string) (Report, error) {
	selected, err := selectScenarios(suite.Scenarios, ids)
	if err != nil {
		return r.selectionErrorReport(suite.ID, err), err
	}
	return r.run(ctx, suite, selected)
}

// RunTags executes scenarios carrying at least one requested tag in suite
// declaration order. It rejects empty, duplicate, and unknown tags.
func (r *Runner) RunTags(ctx context.Context, suite Suite, tags ...string) (Report, error) {
	selected, err := selectTags(suite.Scenarios, tags)
	if err != nil {
		return r.selectionErrorReport(suite.ID, err), err
	}
	return r.run(ctx, suite, selected)
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

// run validates the judge, executes selected scenarios with bounded
// concurrency, and records reports in suite declaration order.
func (r *Runner) run(ctx context.Context, suite Suite, selected []Scenario) (Report, error) {
	started := r.now()
	report := Report{SuiteID: suite.ID, StartedAt: started}
	if len(selected) == 0 {
		err := errors.New("evaluation selection contains no scenarios")
		report.Error = err.Error()
		report.Duration = r.now().Sub(started)
		return report, err
	}
	if err := r.calibrate(ctx); err != nil {
		report.Error = err.Error()
		report.Duration = r.now().Sub(started)
		return report, err
	}

	report.Scenarios = make([]ScenarioReport, len(selected))
	jobs := make(chan int, len(selected))
	workerCount := min(r.maxConcurrency, len(selected))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go r.runScenarios(ctx, selected, report.Scenarios, jobs, &workers)
	}
	for index := range selected {
		jobs <- index
	}
	close(jobs)
	workers.Wait()

	report.Duration = r.now().Sub(started)
	report.Passed = true
	for _, scenario := range report.Scenarios {
		report.Passed = report.Passed && scenario.Passed
	}
	return report, nil
}

// calibrate proves every semantic label in one batch before any application
// scenario runs. The framework owns these examples because it owns the label
// meanings and the rule that only entailed claims pass.
func (r *Runner) calibrate(ctx context.Context) error {
	if r.judge == nil {
		return nil
	}
	assertions, claims, expected := calibrationCases()
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
	for index, want := range expected {
		id := assertions[index].ClaimID
		if got := byID[id]; got != want {
			return fmt.Errorf("%w: %s: got %s, want %s", errCalibration, id, got, want)
		}
	}
	return nil
}

// runScenario executes one hook under its generated timeout and evaluates its
// validated deterministic and semantic assertions.
func (r *Runner) runScenario(ctx context.Context, scenario Scenario) (report ScenarioReport) {
	started := r.now()
	report = ScenarioReport{ID: scenario.ID, StartedAt: started}
	defer func() {
		report.Duration = r.now().Sub(started)
	}()
	if err := ctx.Err(); err != nil {
		report.Error = err.Error()
		return report
	}
	scenarioCtx, cancel := context.WithTimeout(ctx, scenario.Timeout)
	defer cancel()
	result, err := scenario.Run(scenarioCtx, scenario.Input)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	if err := scenarioCtx.Err(); err != nil {
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
		if err := scenarioCtx.Err(); err != nil {
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

// runScenarios consumes scenario indexes from jobs and writes each report to
// its declaration-order slot. Every slot has exactly one worker.
func (r *Runner) runScenarios(
	ctx context.Context,
	scenarios []Scenario,
	reports []ScenarioReport,
	jobs <-chan int,
	workers *sync.WaitGroup,
) {
	defer workers.Done()
	for index := range jobs {
		reports[index] = r.runScenario(ctx, scenarios[index])
	}
}

// calibrationCases returns one unambiguous example for every framework-owned
// label so a judge that collapses distinct outcomes cannot pass calibration.
func calibrationCases() ([]Assertion, []Claim, []Label) {
	assertions := []Assertion{
		{
			ClaimID: "calibration_entailed",
			Output:  "The pump is running.",
			Claim:   "The pump is running.",
		},
		{
			ClaimID: "calibration_contradicted",
			Output:  "The pump is stopped.",
			Claim:   "The pump is running.",
		},
		{
			ClaimID: "calibration_not_addressed",
			Output:  "The valve is open.",
			Claim:   "The pump is running.",
		},
		{
			ClaimID: "calibration_indeterminate",
			Output:  "Two readings at the same time disagree about whether the pump is running.",
			Claim:   "The pump is running.",
		},
	}
	claims := make([]Claim, len(assertions))
	for index, assertion := range assertions {
		claims[index] = Claim{ID: assertion.ClaimID, Text: assertion.Claim}
	}
	expected := []Label{Entailed, Contradicted, NotAddressed, Indeterminate}
	return assertions, claims, expected
}

// selectScenarios validates exact IDs and returns matching scenarios in suite
// declaration order.
func selectScenarios(scenarios []Scenario, ids []string) ([]Scenario, error) {
	wanted, err := selectorSet("scenario", ids)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{}, len(scenarios))
	for _, scenario := range scenarios {
		known[scenario.ID] = struct{}{}
	}
	if err := validateKnownSelectors("scenario", ids, known); err != nil {
		return nil, err
	}
	selected := make([]Scenario, 0, len(wanted))
	for _, scenario := range scenarios {
		if _, ok := wanted[scenario.ID]; ok {
			selected = append(selected, scenario)
		}
	}
	return selected, nil
}

// selectTags validates tags and returns matching scenarios in suite declaration
// order using any-tag matching.
func selectTags(scenarios []Scenario, tags []string) ([]Scenario, error) {
	wanted, err := selectorSet("tag", tags)
	if err != nil {
		return nil, err
	}
	known := make(map[string]struct{})
	for _, scenario := range scenarios {
		for _, tag := range scenario.Tags {
			known[tag] = struct{}{}
		}
	}
	if err := validateKnownSelectors("tag", tags, known); err != nil {
		return nil, err
	}
	selected := make([]Scenario, 0, len(scenarios))
	for _, scenario := range scenarios {
		for _, tag := range scenario.Tags {
			if _, ok := wanted[tag]; ok {
				selected = append(selected, scenario)
				break
			}
		}
	}
	return selected, nil
}

// selectorSet validates one explicit selector list before any evaluation work.
func selectorSet(kind string, values []string) (map[string]struct{}, error) {
	if len(values) == 0 {
		return nil, fmt.Errorf("evaluation %s selection is empty", kind)
	}
	selected := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return nil, fmt.Errorf("evaluation %s is empty", kind)
		}
		if _, exists := selected[value]; exists {
			return nil, fmt.Errorf("duplicate evaluation %s %q", kind, value)
		}
		selected[value] = struct{}{}
	}
	return selected, nil
}

// validateKnownSelectors reports the first unknown value in caller order so
// invalid selections always produce the same diagnostic.
func validateKnownSelectors(kind string, values []string, known map[string]struct{}) error {
	for _, value := range values {
		if _, exists := known[value]; !exists {
			return fmt.Errorf("unknown evaluation %s %q", kind, value)
		}
	}
	return nil
}

// selectionErrorReport records a selection failure without starting a suite.
func (r *Runner) selectionErrorReport(suiteID string, err error) Report {
	started := r.now()
	return Report{
		SuiteID:   suiteID,
		StartedAt: started,
		Duration:  r.now().Sub(started),
		Error:     err.Error(),
	}
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
