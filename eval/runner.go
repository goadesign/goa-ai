// This file implements the execution engine for generated evaluation suites:
// scenario selection, judge calibration, bounded-concurrency orchestration,
// and declaration-order report assembly. The contract types it executes are
// defined in eval.go.

package eval

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type (
	// Runner executes generated suites using one semantic judge.
	Runner struct {
		judge          Judge
		reporter       Reporter
		maxConcurrency int
		now            func() time.Time
	}

	// RunnerConfig defines bounded execution and progressive reporting.
	RunnerConfig struct {
		// MaxConcurrency is the positive maximum number of scenarios in flight.
		MaxConcurrency int
		// Reporter receives progressive scenario lifecycle events. It may be
		// nil when only the returned report is needed.
		Reporter Reporter
	}
)

// calibrationTimeout bounds the fixed-size calibration judgment. The framework
// owns the calibration batch, so it owns the deadline that keeps a stalled
// model client from blocking a suite before any scenario runs.
const calibrationTimeout = 2 * time.Minute

var errCalibration = errors.New("judge calibration failed")

// NewRunner creates a suite runner with an explicit concurrency limit. A nil
// judge is valid for suites whose hooks return deterministic checks only.
func NewRunner(judge Judge, config RunnerConfig) (*Runner, error) {
	if config.MaxConcurrency <= 0 {
		return nil, errors.New("maximum evaluation concurrency must be greater than zero")
	}
	return &Runner{
		judge:          judge,
		reporter:       config.Reporter,
		maxConcurrency: config.MaxConcurrency,
		now:            time.Now,
	}, nil
}

// Run executes every scenario in suite.
func (r *Runner) Run(ctx context.Context, suite Suite) (Report, error) {
	return r.run(ctx, suite, suite.Scenarios)
}

// RunScenarios executes the named scenarios and reports them in suite
// declaration order. It rejects an empty selection, duplicate IDs, and IDs
// absent from the suite.
func (r *Runner) RunScenarios(ctx context.Context, suite Suite, ids ...string) (Report, error) {
	selected, err := selectScenarios(suite.Scenarios, ids)
	if err != nil {
		return r.selectionErrorReport(suite.ID, err), err
	}
	return r.run(ctx, suite, selected)
}

// RunTags executes scenarios carrying at least one requested tag and reports
// them in suite declaration order. It rejects empty, duplicate, and unknown
// tags.
func (r *Runner) RunTags(ctx context.Context, suite Suite, tags ...string) (Report, error) {
	selected, err := selectTags(suite.Scenarios, tags)
	if err != nil {
		return r.selectionErrorReport(suite.ID, err), err
	}
	return r.run(ctx, suite, selected)
}

// calibrationCases returns one unambiguous example for every framework-owned
// label so a judge that collapses distinct outcomes cannot pass calibration.
func calibrationCases() (string, []Claim, []Label) {
	output := "The pump is running. Two readings at the same time disagree about whether the fan is running."
	claims := []Claim{
		{ID: "calibration_entailed", Text: "The pump is running."},
		{ID: "calibration_contradicted", Text: "The pump is stopped."},
		{ID: "calibration_not_addressed", Text: "The compressor is running."},
		{ID: "calibration_indeterminate", Text: "The fan is running."},
	}
	expected := []Label{Entailed, Contradicted, NotAddressed, Indeterminate}
	return output, claims, expected
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

// run validates the judge, executes selected scenarios with bounded
// concurrency, and records reports in suite declaration order. Concurrent
// runs start scenarios with larger declared timeouts first so long cases do
// not leave the worker pool idle at the end.
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
	schedule := scenarioSchedule(selected, r.maxConcurrency)
	jobs := make(chan int)
	workerCount := min(r.maxConcurrency, len(selected))
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go r.runScenarios(ctx, selected, report.Scenarios, jobs, &workers)
	}
dispatch:
	for position, index := range schedule {
		select {
		case jobs <- index:
		case <-ctx.Done():
			for _, pending := range schedule[position:] {
				report.Scenarios[pending] = r.canceledScenarioReport(selected[pending].ID, ctx.Err())
			}
			break dispatch
		}
	}
	close(jobs)
	workers.Wait()

	report.Duration = r.now().Sub(started)
	report.Passed = true
	for _, scenario := range report.Scenarios {
		report.Passed = report.Passed && scenario.Passed
	}
	if err := ctx.Err(); err != nil {
		report.Error = err.Error()
		return report, err
	}
	return report, nil
}

// calibrate proves every semantic label in one batch before any application
// scenario runs. The framework owns these examples because it owns the label
// meanings and the rule that only entailed claims pass. The batch is fixed
// size, so the framework also owns its deadline: without one a blocking model
// client would stall the whole suite before any scenario starts.
func (r *Runner) calibrate(ctx context.Context) error {
	if r.judge == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, calibrationTimeout)
	defer cancel()
	output, claims, expected := calibrationCases()
	judgments, err := r.judge.Judge(ctx, output, claims)
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
		id := claims[index].ID
		if got := byID[id]; got != want {
			return fmt.Errorf("%w: %s: got %s, want %s", errCalibration, id, got, want)
		}
	}
	return nil
}

// runScenario executes one hook under its generated timeout and evaluates its
// validated deterministic and semantic assertions.
func (r *Runner) runScenario(ctx context.Context, scenario Scenario) (report ScenarioReport) {
	if err := ctx.Err(); err != nil {
		return r.canceledScenarioReport(scenario.ID, err)
	}
	started := r.now()
	report = ScenarioReport{ID: scenario.ID, StartedAt: started}
	defer func() {
		report.Duration = r.now().Sub(started)
		if r.reporter != nil {
			r.reporter.ScenarioFinished(report)
		}
	}()
	if r.reporter != nil {
		r.reporter.ScenarioStarted(scenario.ID, started)
	}
	scenarioCtx, cancel := context.WithTimeout(ctx, scenario.Timeout)
	defer cancel()
	result, err := scenario.Run(scenarioCtx)
	if err != nil {
		report.Error = err.Error()
		return report
	}
	if err := scenarioCtx.Err(); err != nil {
		report.Error = err.Error()
		return report
	}
	if err := validateResult(result); err != nil {
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
		judgments, err := r.judgeClaims(scenarioCtx, result)
		if err != nil {
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

// judgeClaims labels every claim of a validated result. An empty output
// neither establishes nor contradicts any claim, so the runner labels each
// claim not_addressed itself; otherwise it batches the claims to the semantic
// judge and validates the one-to-one response.
func (r *Runner) judgeClaims(ctx context.Context, result Result) ([]Judgment, error) {
	if result.Output == "" {
		judgments := make([]Judgment, len(result.Claims))
		for i, claim := range result.Claims {
			judgments[i] = Judgment{
				ClaimID:   claim.ID,
				Label:     NotAddressed,
				Rationale: "the scenario produced no output to judge",
			}
		}
		return judgments, nil
	}
	judgments, err := r.judge.Judge(ctx, result.Output, result.Claims)
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateJudgments(result.Claims, judgments); err != nil {
		return nil, err
	}
	return judgments, nil
}

// scenarioSchedule returns report-slot indexes in dispatch order. Serial runs
// retain declaration order; concurrent runs use stable longest-timeout-first
// scheduling while reports continue to use their original slots.
func scenarioSchedule(scenarios []Scenario, maxConcurrency int) []int {
	schedule := make([]int, len(scenarios))
	for index := range scenarios {
		schedule[index] = index
	}
	if maxConcurrency == 1 {
		return schedule
	}
	sort.SliceStable(schedule, func(left, right int) bool {
		return scenarios[schedule[left]].Timeout > scenarios[schedule[right]].Timeout
	})
	return schedule
}

// runScenarios consumes scenario indexes from jobs and writes each index once
// to its declaration-order report slot.
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

// canceledScenarioReport records and reports a scenario whose hook never
// started because the suite context was canceled.
func (r *Runner) canceledScenarioReport(id string, err error) ScenarioReport {
	report := ScenarioReport{
		ID:    id,
		Error: err.Error(),
	}
	if r.reporter != nil {
		r.reporter.ScenarioFinished(report)
	}
	return report
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
