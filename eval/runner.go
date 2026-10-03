// Package eval composes product capture and offline assessment. Capture hooks and
// generated bindings have separate deadlines; both operations preserve selected
// declaration order and completed evidence when other scenarios fail.
package eval

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type (
	// Runner captures and assesses generated suites with bounded concurrency.
	Runner struct {
		engine *Engine
		config RunnerConfig
	}

	// RunnerConfig owns per-operation concurrency and report provenance.
	RunnerConfig struct {
		// MaxConcurrency is the positive maximum number of scenarios in flight
		// within one Capture or Assess operation, not across all callers.
		MaxConcurrency int
		// Reporter receives assessment lifecycle events and may be nil.
		Reporter Reporter
		// Provenance records product and evaluation revisions supplied by the caller.
		Provenance map[string]string
	}
)

// NewRunner creates a runner. Capture needs no engine. Assessment with a nil
// engine supports scenarios that declare only exact checks.
func NewRunner(engine *Engine, config RunnerConfig) (*Runner, error) {
	if config.MaxConcurrency <= 0 {
		return nil, errors.New("maximum evaluation concurrency must be greater than zero")
	}
	config.Provenance = maps.Clone(config.Provenance)
	return &Runner{engine: engine, config: config}, nil
}

// Run captures a suite once and assesses that same archive. The returned archive
// can be saved even when capture or assessment fails, then assessed independently.
func (r *Runner) Run(ctx context.Context, suite Suite) (Archive, Report, error) {
	archive, captureErr := r.Capture(ctx, suite)
	if archive.ID == "" {
		return archive, Report{}, captureErr
	}
	report, assessErr := r.Assess(ctx, suite, archive)
	return archive, report, errors.Join(captureErr, assessErr)
}

// RunScenarios validates IDs before capturing selected scenarios once.
func (r *Runner) RunScenarios(ctx context.Context, suite Suite, ids ...string) (Archive, Report, error) {
	selected, err := selectScenarios(suite.Scenarios, ids)
	if err != nil {
		return Archive{}, Report{SuiteID: suite.ID, Error: err.Error()}, err
	}
	suite.Scenarios = selected
	return r.Run(ctx, suite)
}

// RunTags validates tags before capturing scenarios matching any requested tag.
func (r *Runner) RunTags(ctx context.Context, suite Suite, tags ...string) (Archive, Report, error) {
	selected, err := selectTags(suite.Scenarios, tags)
	if err != nil {
		return Archive{}, Report{SuiteID: suite.ID, Error: err.Error()}, err
	}
	suite.Scenarios = selected
	return r.Run(ctx, suite)
}

// CaptureScenarios records only the named scenarios after validating all IDs.
func (r *Runner) CaptureScenarios(ctx context.Context, suite Suite, ids ...string) (Archive, error) {
	selected, err := selectScenarios(suite.Scenarios, ids)
	if err != nil {
		return Archive{}, err
	}
	suite.Scenarios = selected
	return r.Capture(ctx, suite)
}

// CaptureTags records scenarios matching any of the validated requested tags.
func (r *Runner) CaptureTags(ctx context.Context, suite Suite, tags ...string) (Archive, error) {
	selected, err := selectTags(suite.Scenarios, tags)
	if err != nil {
		return Archive{}, err
	}
	suite.Scenarios = selected
	return r.Capture(ctx, suite)
}

// Capture executes product hooks and saves their validated observation bytes.
// Exact predicates and semantic models are not called during this operation.
func (r *Runner) Capture(ctx context.Context, suite Suite) (Archive, error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/eval").Start(ctx, "eval.capture")
	defer span.End()
	if err := validateSuite(suite); err != nil {
		recordError(span, err)
		return Archive{}, err
	}
	for _, scenario := range suite.Scenarios {
		if scenario.Capture == nil {
			err := fmt.Errorf("scenario %q has no capture hook", scenario.ID)
			recordError(span, err)
			return Archive{}, err
		}
	}
	archive := Archive{
		SuiteID: suite.ID, StartedAt: time.Now(), Provenance: maps.Clone(r.config.Provenance),
		Observations: make([]Observation, len(suite.Scenarios)),
	}
	r.parallel(suite.Scenarios, func(index int) {
		archive.Observations[index] = captureScenario(ctx, suite.Scenarios[index])
	})
	archive.Duration = time.Since(archive.StartedAt)
	archive.ID = digest(archive)
	span.SetAttributes(attribute.String("eval.suite.id", suite.ID), attribute.Int("eval.observation.count", len(archive.Observations)))
	if err := ctx.Err(); err != nil {
		recordError(span, err)
		return archive, err
	}
	return archive, nil
}

// Assess verifies saved identities, then uses only the archive's observations
// and the generated bindings. It never calls Capture, even when hooks exist.
func (r *Runner) Assess(ctx context.Context, suite Suite, archive Archive) (report Report, err error) {
	ctx, span := otel.Tracer("goa.design/goa-ai/eval").Start(ctx, "eval.assess")
	defer span.End()
	report = Report{
		SuiteID: suite.ID, ArchiveID: archive.ID, StartedAt: time.Now(),
		Policy: PolicyRecord{Strategy: strategyExact}, Provenance: maps.Clone(r.config.Provenance),
	}
	defer func() {
		report.Duration = time.Since(report.StartedAt)
		report.ID = digest(report)
	}()
	if r.engine != nil {
		report.Policy = r.engine.Policy()
	}
	selected, observations, err := assessmentSelection(suite, archive)
	if err != nil {
		report.Error = err.Error()
		recordError(span, err)
		return report, err
	}
	report.Scenarios = make([]ScenarioReport, len(selected))
	r.parallel(selected, func(index int) {
		report.Scenarios[index] = r.assessScenario(ctx, selected[index], observations[index])
	})
	report.Passed = true
	for _, scenario := range report.Scenarios {
		report.Passed = report.Passed && scenario.Passed
	}
	span.SetAttributes(attribute.String("eval.suite.id", suite.ID), attribute.Bool("eval.passed", report.Passed))
	if err := ctx.Err(); err != nil {
		report.Error = err.Error()
		recordError(span, err)
		return report, err
	}
	return report, nil
}

// captureScenario bounds one product call and makes a record even when it never
// starts. A product's expected failed outcome belongs in typed Data; Error means
// the capture operation itself could not finish correctly.
func captureScenario(ctx context.Context, scenario Scenario) (observation Observation) {
	observation = Observation{ScenarioID: scenario.ID, SchemaID: bytesDigest([]byte(scenario.Schema)), Input: slices.Clone(scenario.Input)}
	defer func() { observation.ID = digest(observation) }()
	if err := ctx.Err(); err != nil {
		observation.Error = err.Error()
		return observation
	}
	ctx, cancel := context.WithTimeout(ctx, scenario.Timeout)
	defer cancel()
	ctx, span := otel.Tracer("goa.design/goa-ai/eval").Start(ctx, "eval.capture.scenario",
		trace.WithAttributes(attribute.String("eval.scenario.id", scenario.ID)))
	defer span.End()
	observation.CapturedAt = time.Now()
	data, err := scenario.Capture(ctx)
	observation.Data = slices.Clone(data)
	observation.Duration = time.Since(observation.CapturedAt)
	if err == nil {
		err = ctx.Err()
	}
	if err == nil && len(data) == 0 {
		err = errors.New("capture returned no encoded observation")
	}
	if err != nil {
		observation.Error = err.Error()
		recordError(span, err)
	}
	return observation
}

// assessScenario decodes exactly one captured record, runs every exact predicate,
// and then asks the engine about the statically declared semantic requirements.
func (r *Runner) assessScenario(ctx context.Context, scenario Scenario, observation Observation) (report ScenarioReport) {
	report = ScenarioReport{ID: scenario.ID, ObservationID: observation.ID, CaptureDuration: observation.Duration}
	defer func() {
		if !report.StartedAt.IsZero() {
			report.Duration = time.Since(report.StartedAt)
		}
		if r.config.Reporter != nil {
			r.config.Reporter.ScenarioFinished(report)
		}
	}()
	if err := ctx.Err(); err != nil {
		report.Error = err.Error()
		return report
	}
	report.StartedAt = time.Now()
	if r.config.Reporter != nil {
		r.config.Reporter.ScenarioStarted(scenario.ID, report.StartedAt)
	}
	ctx, cancel := context.WithTimeout(ctx, scenario.Timeout)
	defer cancel()
	ctx, span := otel.Tracer("goa.design/goa-ai/eval").Start(ctx, "eval.assess.scenario",
		trace.WithAttributes(attribute.String("eval.scenario.id", scenario.ID)))
	defer span.End()
	if observation.Error != "" {
		report.Error = observation.Error
		recordError(span, errors.New(report.Error))
		return report
	}
	// Give generated decoders their own bytes so even a custom binding cannot
	// change the archive that future assessments will receive.
	binding, err := scenario.Bind(slices.Clone(observation.Data))
	if err == nil {
		err = validateBinding(scenario, binding)
	}
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		report.Error = fmt.Sprintf("bind captured observation: %v", err)
		recordError(span, err)
		return report
	}
	report.Checks = binding.Checks
	if len(scenario.Requirements) > 0 {
		if r.engine == nil {
			report.Error = "semantic assessment engine is required"
			recordError(span, errors.New(report.Error))
			return report
		}
		report.Requirements, report.Calls, err = r.engine.assess(ctx, observation.ID, scenario.Requirements, binding.Subjects)
		if err != nil {
			report.Error = err.Error()
			recordError(span, err)
			return report
		}
	}
	report.Passed = true
	for _, check := range report.Checks {
		report.Passed = report.Passed && check.Passed
	}
	for _, requirement := range report.Requirements {
		for _, instance := range requirement.Instances {
			report.Passed = report.Passed && instance.Passed()
		}
	}
	span.SetAttributes(attribute.Bool("eval.passed", report.Passed))
	return report
}

// parallel runs at most the configured number of scenarios at once. Workers
// still visit canceled slots so every selected case receives a terminal record.
func (r *Runner) parallel(scenarios []Scenario, work func(int)) {
	jobs := make(chan int)
	var workers sync.WaitGroup
	for range min(r.config.MaxConcurrency, len(scenarios)) {
		workers.Go(func() {
			for index := range jobs {
				work(index)
			}
		})
	}
	for _, index := range scenarioSchedule(scenarios, r.config.MaxConcurrency) {
		jobs <- index
	}
	close(jobs)
	workers.Wait()
}

// assessmentSelection matches archived scenarios to the current suite before
// any predicate or model runs. Requirements may change; observation schemas may
// not, because that would reinterpret the saved evidence.
func assessmentSelection(suite Suite, archive Archive) ([]Scenario, []Observation, error) {
	if err := validateSuite(suite); err != nil {
		return nil, nil, err
	}
	if err := archive.Validate(); err != nil {
		return nil, nil, err
	}
	if archive.SuiteID != suite.ID {
		return nil, nil, errors.New("capture archive belongs to a different suite")
	}
	byID := make(map[string]Observation, len(archive.Observations))
	for _, observation := range archive.Observations {
		byID[observation.ScenarioID] = observation
	}
	selected := make([]Scenario, 0, len(byID))
	observations := make([]Observation, 0, len(byID))
	for _, scenario := range suite.Scenarios {
		observation, exists := byID[scenario.ID]
		if !exists {
			continue
		}
		if observation.SchemaID != bytesDigest([]byte(scenario.Schema)) {
			return nil, nil, fmt.Errorf("observation schema changed for scenario %q", scenario.ID)
		}
		selected = append(selected, scenario)
		observations = append(observations, observation)
		delete(byID, scenario.ID)
	}
	if len(byID) > 0 {
		return nil, nil, errors.New("capture archive contains scenarios absent from this suite")
	}
	return selected, observations, nil
}

func validateSuite(suite Suite) error {
	if suite.ID == "" || len(suite.Scenarios) == 0 {
		return errors.New("evaluation suite requires an ID and at least one scenario")
	}
	seen := make(map[string]bool, len(suite.Scenarios))
	requirements := make(map[string]bool)
	for _, scenario := range suite.Scenarios {
		if scenario.ID == "" || seen[scenario.ID] || scenario.Timeout <= 0 || scenario.Schema == "" || scenario.Bind == nil {
			return fmt.Errorf("invalid compiled scenario %q", scenario.ID)
		}
		seen[scenario.ID] = true
		if len(scenario.CheckNames)+len(scenario.Requirements) == 0 {
			return fmt.Errorf("scenario %q has no declared assertions", scenario.ID)
		}
		checks := make(map[string]bool, len(scenario.CheckNames))
		for _, check := range scenario.CheckNames {
			if check == "" || checks[check] {
				return fmt.Errorf("invalid declared check %q", check)
			}
			checks[check] = true
		}
		for _, requirement := range scenario.Requirements {
			if requirement.ID == "" || requirement.Statement == "" || requirement.Subject == "" || requirements[requirement.ID] {
				return fmt.Errorf("invalid declared requirement %q", requirement.ID)
			}
			if requirement.SchemaID != bytesDigest([]byte(scenario.Schema)) {
				return fmt.Errorf("requirement %q does not identify its observation schema", requirement.ID)
			}
			requirements[requirement.ID] = true
		}
	}
	return nil
}

func recordError(span trace.Span, err error) {
	span.RecordError(err)
	span.SetStatus(codes.Error, err.Error())
}
