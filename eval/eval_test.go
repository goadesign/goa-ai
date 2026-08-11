package eval

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedJudge struct {
	mu        sync.Mutex
	responses [][]Judgment
	errors    []error
	requests  [][]Assertion
	onJudge   func()
}

func (j *scriptedJudge) Judge(_ context.Context, assertions []Assertion) ([]Judgment, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.onJudge != nil {
		j.onJudge()
	}
	j.requests = append(j.requests, assertions)
	index := len(j.requests) - 1
	return j.responses[index], j.errors[index]
}

type recordingReporter struct {
	mu       sync.Mutex
	started  []string
	finished []ScenarioReport
}

func (r *recordingReporter) ScenarioStarted(id string, _ time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.started = append(r.started, id)
}

func (r *recordingReporter) ScenarioFinished(report ScenarioReport) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.finished = append(r.finished, report)
}

// deadlineJudge records whether the calibration call carried a deadline and
// answers every batch with valid calibration judgments.
type deadlineJudge struct {
	sawDeadline bool
}

func (j *deadlineJudge) Judge(ctx context.Context, _ []Assertion) ([]Judgment, error) {
	_, j.sawDeadline = ctx.Deadline()
	return calibrationJudgments(), nil
}

type concurrentJudge struct {
	started chan struct{}
	release chan struct{}
}

func (j *concurrentJudge) Judge(ctx context.Context, assertions []Assertion) ([]Judgment, error) {
	if len(assertions) == 4 && assertions[0].ClaimID == "calibration_entailed" {
		return calibrationJudgments(), nil
	}
	j.started <- struct{}{}
	select {
	case <-j.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	judgments := make([]Judgment, len(assertions))
	for index, assertion := range assertions {
		judgments[index] = Judgment{
			ClaimID:   assertion.ClaimID,
			Label:     Entailed,
			Rationale: "The output states the claim.",
		}
	}
	return judgments, nil
}

func TestValidateResult(t *testing.T) {
	tests := []struct {
		name    string
		result  Result
		wantErr string
	}{
		{
			name:   "passing check",
			result: Result{Checks: []Check{{Name: "typed_result", Passed: true}}},
		},
		{
			name: "claims and evidence",
			result: Result{
				Claims:    []Claim{{ID: "complete", Text: "The answer is complete."}},
				Output:    "All records are listed.",
				Artifacts: []Artifact{{Name: "transcript", URI: "s3://evals/run"}},
			},
		},
		{name: "no assertions", result: Result{}, wantErr: "at least one check or claim"},
		{
			name:    "failed check without diagnostic",
			result:  Result{Checks: []Check{{Name: "typed_result"}}},
			wantErr: "requires a diagnostic",
		},
		{
			name: "passing check with diagnostic",
			result: Result{
				Checks: []Check{{Name: "typed_result", Passed: true, Diagnostic: "ignored"}},
			},
			wantErr: "must not have a diagnostic",
		},
		{
			name: "duplicate checks",
			result: Result{Checks: []Check{
				{Name: "typed_result", Passed: true},
				{Name: "typed_result", Passed: true},
			}},
			wantErr: "duplicate check",
		},
		{
			name:   "claims without output",
			result: Result{Claims: []Claim{{ID: "complete", Text: "Complete."}}},
		},
		{
			name: "duplicate claims",
			result: Result{
				Claims: []Claim{
					{ID: "complete", Text: "Complete."},
					{ID: "complete", Text: "Still complete."},
				},
				Output: "Done.",
			},
			wantErr: "duplicate claim",
		},
		{
			name: "invalid artifact",
			result: Result{
				Checks:    []Check{{Name: "typed_result", Passed: true}},
				Artifacts: []Artifact{{Name: "transcript"}},
			},
			wantErr: "artifact name and URI are required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateResult(test.result)
			if test.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestValidateJudgments(t *testing.T) {
	claims := []Claim{{ID: "first", Text: "First."}, {ID: "second", Text: "Second."}}
	tests := []struct {
		name      string
		judgments []Judgment
		wantErr   string
	}{
		{
			name: "complete",
			judgments: []Judgment{
				{ClaimID: "second", Label: NotAddressed, Rationale: "Absent."},
				{ClaimID: "first", Label: Entailed, Rationale: "Explicit."},
			},
		},
		{
			name:      "missing",
			judgments: []Judgment{{ClaimID: "first", Label: Entailed, Rationale: "Explicit."}},
			wantErr:   "1 judgments for 2 claims",
		},
		{
			name: "unknown",
			judgments: []Judgment{
				{ClaimID: "first", Label: Entailed, Rationale: "Explicit."},
				{ClaimID: "third", Label: Entailed, Rationale: "Explicit."},
			},
			wantErr: "unknown claim",
		},
		{
			name: "duplicate",
			judgments: []Judgment{
				{ClaimID: "first", Label: Entailed, Rationale: "Explicit."},
				{ClaimID: "first", Label: Entailed, Rationale: "Explicit."},
			},
			wantErr: "duplicate judgment",
		},
		{
			name: "invalid label",
			judgments: []Judgment{
				{ClaimID: "first", Label: "maybe", Rationale: "Unclear."},
				{ClaimID: "second", Label: Entailed, Rationale: "Explicit."},
			},
			wantErr: "invalid label",
		},
		{
			name: "missing rationale",
			judgments: []Judgment{
				{ClaimID: "first", Label: Entailed},
				{ClaimID: "second", Label: Entailed, Rationale: "Explicit."},
			},
			wantErr: "requires a rationale",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateJudgments(claims, test.judgments)
			if test.wantErr == "" {
				assert.NoError(t, err)
				return
			}
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestValidateJudgmentsRejectsDuplicateClaims(t *testing.T) {
	claims := []Claim{{ID: "claim", Text: "First."}, {ID: "claim", Text: "Second."}}

	err := ValidateJudgments(claims, []Judgment{{ClaimID: "claim", Label: Entailed, Rationale: "Explicit."}})

	assert.ErrorContains(t, err, "duplicate claim")
}

func TestNewRunnerRequiresPositiveConcurrency(t *testing.T) {
	_, err := NewRunner(nil, RunnerConfig{})

	assert.ErrorContains(t, err, "greater than zero")
}

func TestRunnerRejectsEmptySuite(t *testing.T) {
	report, err := mustRunner(t, nil, 1).Run(context.Background(), Suite{ID: "empty"})

	require.ErrorContains(t, err, "contains no scenarios")
	assert.Contains(t, report.Error, "contains no scenarios")
	assert.Empty(t, report.Scenarios)
}

func TestRunnerCalibratesEveryLabelThenRuns(t *testing.T) {
	judge := &scriptedJudge{
		responses: [][]Judgment{
			calibrationJudgments(),
			{{ClaimID: "answer", Label: Entailed, Rationale: "Exact."}},
		},
		errors: []error{nil, nil},
	}
	var order []string
	suite := Suite{
		ID: "chat",
		Scenarios: []Scenario{
			{
				ID: "first", Tags: []string{"smoke"}, Timeout: time.Second,
				Run: func(context.Context) (Result, error) {
					order = append(order, "one")
					return Result{
						Checks: []Check{{Name: "typed", Passed: true}},
						Claims: []Claim{{ID: "answer", Text: "The answer is correct."}},
						Output: "Correct.",
					}, nil
				},
			},
			{
				ID: "second", Tags: []string{"extended"}, Timeout: time.Second,
				Run: func(context.Context) (Result, error) {
					order = append(order, "two")
					return Result{Checks: []Check{{Name: "typed", Passed: true}}}, nil
				},
			},
		},
	}

	runner := mustRunner(t, judge, 1)
	report, err := runner.RunScenarios(context.Background(), suite, "first")

	require.NoError(t, err)
	assert.True(t, report.Passed)
	require.Len(t, report.Scenarios, 1)
	assert.Equal(t, "first", report.Scenarios[0].ID)
	assert.Equal(t, []string{"one"}, order)
	require.Len(t, judge.requests, 2)
	assert.Equal(t, []Assertion{
		{ClaimID: "calibration_entailed", Output: "The pump is running.", Claim: "The pump is running."},
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
	}, judge.requests[0])
	assert.Equal(t, "Correct.", judge.requests[1][0].Output)
}

func TestRunnerBoundsCalibrationWithDeadline(t *testing.T) {
	judge := &deadlineJudge{}
	suite := Suite{
		ID: "chat",
		Scenarios: []Scenario{
			{
				ID: "first", Timeout: time.Second,
				Run: func(context.Context) (Result, error) {
					return Result{Checks: []Check{{Name: "typed", Passed: true}}}, nil
				},
			},
		},
	}

	runner := mustRunner(t, judge, 1)
	_, err := runner.Run(context.Background(), suite)

	require.NoError(t, err)
	assert.True(t, judge.sawDeadline, "calibration must run under a bounded context")
}

func TestRunnerRejectsJudgeThatCollapsesLabels(t *testing.T) {
	judge := &scriptedJudge{
		responses: [][]Judgment{{
			{ClaimID: "calibration_entailed", Label: Entailed, Rationale: "Always entailed."},
			{ClaimID: "calibration_contradicted", Label: Entailed, Rationale: "Always entailed."},
			{ClaimID: "calibration_not_addressed", Label: Entailed, Rationale: "Always entailed."},
			{ClaimID: "calibration_indeterminate", Label: Entailed, Rationale: "Always entailed."},
		}},
		errors: []error{nil},
	}
	run := false
	suite := Suite{
		ID: "chat",
		Scenarios: []Scenario{{
			ID: "case", Timeout: time.Second,
			Run: func(context.Context) (Result, error) {
				run = true
				return Result{}, nil
			},
		}},
	}

	report, err := mustRunner(t, judge, 1).Run(context.Background(), suite)

	require.ErrorIs(t, err, errCalibration)
	assert.False(t, report.Passed)
	assert.Contains(t, report.Error, "judge calibration failed")
	assert.False(t, run)
}

func TestRunnerRecordsNonEntailedSemanticOutcome(t *testing.T) {
	judge := &scriptedJudge{
		responses: [][]Judgment{
			calibrationJudgments(),
			{{ClaimID: "complete", Label: Contradicted, Rationale: "The answer states the opposite."}},
		},
		errors: []error{nil, nil},
	}
	suite := Suite{ID: "chat", Scenarios: []Scenario{{
		ID: "case", Timeout: time.Second,
		Run: func(context.Context) (Result, error) {
			return Result{
				Claims: []Claim{{ID: "complete", Text: "The inventory is complete."}},
				Output: "The inventory is incomplete.",
			}, nil
		},
	}}}

	report, err := mustRunner(t, judge, 1).Run(context.Background(), suite)

	require.NoError(t, err)
	require.Len(t, report.Scenarios, 1)
	assert.False(t, report.Passed)
	assert.Empty(t, report.Scenarios[0].Error)
	assert.Equal(t, Contradicted, report.Scenarios[0].Judgments[0].Label)
}

func TestRunnerLabelsClaimsNotAddressedWhenOutputEmpty(t *testing.T) {
	// Only the calibration response is scripted: judging an empty output
	// would exhaust the script and fail, proving the runner labels the
	// claims itself.
	judge := &scriptedJudge{responses: [][]Judgment{calibrationJudgments()}, errors: []error{nil}}
	suite := Suite{ID: "chat", Scenarios: []Scenario{{
		ID: "case", Timeout: time.Second,
		Run: func(context.Context) (Result, error) {
			return Result{
				Checks: []Check{{Name: "terminal", Passed: false, Diagnostic: "run failed"}},
				Claims: []Claim{{ID: "complete", Text: "The inventory is complete."}},
			}, nil
		},
	}}}

	report, err := mustRunner(t, judge, 1).Run(context.Background(), suite)

	require.NoError(t, err)
	require.Len(t, report.Scenarios, 1)
	scenario := report.Scenarios[0]
	assert.False(t, scenario.Passed)
	assert.Empty(t, scenario.Error)
	require.Len(t, scenario.Judgments, 1)
	assert.Equal(t, NotAddressed, scenario.Judgments[0].Label)
	assert.NotEmpty(t, scenario.Judgments[0].Rationale)
}

func TestRunnerRecordsJudgeErrorsAtOwningBoundary(t *testing.T) {
	t.Run("calibration", func(t *testing.T) {
		want := errors.New("judge unavailable")
		judge := &scriptedJudge{responses: [][]Judgment{nil}, errors: []error{want}}
		hookCalled := false
		suite := Suite{ID: "chat", Scenarios: []Scenario{{
			ID: "case", Timeout: time.Second,
			Run: func(context.Context) (Result, error) {
				hookCalled = true
				return Result{Checks: []Check{{Name: "ok", Passed: true}}}, nil
			},
		}}}

		report, err := mustRunner(t, judge, 1).Run(context.Background(), suite)

		require.ErrorIs(t, err, errCalibration)
		require.ErrorIs(t, err, want)
		assert.Contains(t, report.Error, want.Error())
		assert.False(t, hookCalled)
	})

	t.Run("calibration protocol", func(t *testing.T) {
		judge := &scriptedJudge{
			responses: [][]Judgment{{
				{ClaimID: "calibration_entailed", Label: Entailed, Rationale: "Exact."},
			}},
			errors: []error{nil},
		}

		report, err := mustRunner(t, judge, 1).Run(
			context.Background(),
			Suite{ID: "chat", Scenarios: selectionSuite().Scenarios[:1]},
		)

		require.ErrorIs(t, err, errCalibration)
		assert.Contains(t, report.Error, "got 1 judgments for 4 claims")
	})

	t.Run("scenario", func(t *testing.T) {
		want := errors.New("judge unavailable")
		judge := &scriptedJudge{
			responses: [][]Judgment{calibrationJudgments(), nil},
			errors:    []error{nil, want},
		}
		suite := Suite{ID: "chat", Scenarios: []Scenario{{
			ID: "case", Timeout: time.Second,
			Run: func(context.Context) (Result, error) {
				return Result{
					Claims: []Claim{{ID: "complete", Text: "The inventory is complete."}},
					Output: "Complete.",
				}, nil
			},
		}}}

		report, err := mustRunner(t, judge, 1).Run(context.Background(), suite)

		require.NoError(t, err)
		require.Len(t, report.Scenarios, 1)
		assert.Equal(t, want.Error(), report.Scenarios[0].Error)
		assert.False(t, report.Passed)
	})
}

func TestRunnerValidatesExactScenarioSelection(t *testing.T) {
	suite := selectionSuite()
	tests := []struct {
		name    string
		ids     []string
		wantIDs []string
		wantErr string
	}{
		{name: "declaration order", ids: []string{"third", "first"}, wantIDs: []string{"first", "third"}},
		{name: "empty", wantErr: "selection is empty"},
		{name: "empty ID", ids: []string{""}, wantErr: "scenario is empty"},
		{name: "duplicate", ids: []string{"first", "first"}, wantErr: "duplicate"},
		{name: "unknown", ids: []string{"missing"}, wantErr: "unknown"},
		{
			name:    "first unknown in caller order",
			ids:     []string{"missing_second", "missing_first"},
			wantErr: `unknown evaluation scenario "missing_second"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := mustRunner(t, nil, 1).RunScenarios(context.Background(), suite, test.ids...)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				assert.Contains(t, report.Error, test.wantErr)
				assert.Empty(t, report.Scenarios)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.wantIDs, reportScenarioIDs(report))
		})
	}
}

func TestRunnerValidatesTagSelection(t *testing.T) {
	suite := selectionSuite()
	tests := []struct {
		name    string
		tags    []string
		wantIDs []string
		wantErr string
	}{
		{name: "any tag", tags: []string{"slow", "smoke"}, wantIDs: []string{"first", "second", "third"}},
		{name: "empty", wantErr: "selection is empty"},
		{name: "duplicate", tags: []string{"smoke", "smoke"}, wantErr: "duplicate"},
		{name: "unknown", tags: []string{"missing"}, wantErr: "unknown"},
		{
			name:    "first unknown in caller order",
			tags:    []string{"missing_second", "missing_first"},
			wantErr: `unknown evaluation tag "missing_second"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report, err := mustRunner(t, nil, 1).RunTags(context.Background(), suite, test.tags...)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				assert.Contains(t, report.Error, test.wantErr)
				assert.Empty(t, report.Scenarios)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, test.wantIDs, reportScenarioIDs(report))
		})
	}
}

func TestRunnerRejectsSelectionBeforeJudgeOrHooks(t *testing.T) {
	judgeCalled := false
	judge := &scriptedJudge{onJudge: func() { judgeCalled = true }}
	hookCalled := false
	suite := Suite{ID: "chat", Scenarios: []Scenario{{
		ID: "known", Timeout: time.Second,
		Run: func(context.Context) (Result, error) {
			hookCalled = true
			return Result{Checks: []Check{{Name: "ok", Passed: true}}}, nil
		},
	}}}

	report, err := mustRunner(t, judge, 1).RunScenarios(context.Background(), suite, "missing")

	require.ErrorContains(t, err, "unknown evaluation scenario")
	assert.False(t, judgeCalled)
	assert.False(t, hookCalled)
	assert.Empty(t, report.Scenarios)
}

func TestRunnerBoundsConcurrencyAndPreservesReportOrder(t *testing.T) {
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	scenarios := make([]Scenario, 4)
	for index := range scenarios {
		scenarios[index] = Scenario{
			ID:      fmt.Sprintf("case_%d", index),
			Timeout: time.Second,
			Run: func(context.Context) (Result, error) {
				started <- struct{}{}
				<-release
				return Result{Checks: []Check{{Name: "ok", Passed: true}}}, nil
			},
		}
	}
	type runOutcome struct {
		report Report
		err    error
	}
	finished := make(chan runOutcome, 1)
	runner := mustRunner(t, nil, 2)
	go func() {
		report, err := runner.Run(context.Background(), Suite{ID: "chat", Scenarios: scenarios})
		finished <- runOutcome{report: report, err: err}
	}()

	<-started
	<-started
	select {
	case <-started:
		t.Fatal("runner exceeded the configured concurrency limit")
	default:
	}
	close(release)
	outcome := <-finished
	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"case_0", "case_1", "case_2", "case_3"}, reportScenarioIDs(outcome.report))
	assert.True(t, outcome.report.Passed)
}

func TestRunnerJudgesIndependentScenariosConcurrently(t *testing.T) {
	judge := &concurrentJudge{
		started: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	scenarios := make([]Scenario, 2)
	for index := range scenarios {
		id := fmt.Sprintf("case_%d", index)
		scenarios[index] = Scenario{
			ID:      id,
			Timeout: time.Second,
			Run: func(context.Context) (Result, error) {
				return Result{
					Claims: []Claim{{ID: id, Text: "The answer is complete."}},
					Output: "The answer is complete.",
				}, nil
			},
		}
	}
	type runOutcome struct {
		report Report
		err    error
	}
	runner := mustRunner(t, judge, 2)
	finished := make(chan runOutcome, 1)
	go func() {
		report, err := runner.Run(
			context.Background(),
			Suite{ID: "chat", Scenarios: scenarios},
		)
		finished <- runOutcome{report: report, err: err}
	}()

	for range 2 {
		select {
		case <-judge.started:
		case <-time.After(time.Second):
			close(judge.release)
			t.Fatal("runner did not invoke independent scenario judgments concurrently")
		}
	}
	close(judge.release)
	outcome := <-finished
	require.NoError(t, outcome.err)
	assert.True(t, outcome.report.Passed)
}

func TestRunnerCancellationStopsNewHooksAndReportsLifecycle(t *testing.T) {
	reporter := new(recordingReporter)
	runner, err := NewRunner(nil, RunnerConfig{MaxConcurrency: 2, Reporter: reporter})
	require.NoError(t, err)

	hookStarted := make(chan struct{}, 3)
	scenarios := make([]Scenario, 3)
	for index := range scenarios {
		scenarios[index] = Scenario{
			ID:      fmt.Sprintf("case_%d", index),
			Timeout: time.Minute,
			Run: func(ctx context.Context) (Result, error) {
				hookStarted <- struct{}{}
				<-ctx.Done()
				return Result{}, ctx.Err()
			},
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	type runOutcome struct {
		report Report
		err    error
	}
	finished := make(chan runOutcome, 1)
	go func() {
		report, err := runner.Run(ctx, Suite{ID: "chat", Scenarios: scenarios})
		finished <- runOutcome{report: report, err: err}
	}()

	<-hookStarted
	<-hookStarted
	cancel()
	outcome := <-finished

	require.ErrorIs(t, outcome.err, context.Canceled)
	assert.Equal(t, context.Canceled.Error(), outcome.report.Error)
	require.Len(t, outcome.report.Scenarios, 3)
	notStarted := 0
	for _, report := range outcome.report.Scenarios {
		assert.Equal(t, context.Canceled.Error(), report.Error)
		if report.StartedAt.IsZero() {
			notStarted++
		}
	}
	assert.Equal(t, 1, notStarted)
	reporter.mu.Lock()
	defer reporter.mu.Unlock()
	assert.Len(t, reporter.started, 2)
	require.Len(t, reporter.finished, 3)
	finishedIDs := make([]string, len(reporter.finished))
	for index, report := range reporter.finished {
		finishedIDs[index] = report.ID
	}
	assert.ElementsMatch(t, []string{"case_0", "case_1", "case_2"}, finishedIDs)
}

func TestRunnerContinuesAfterScenarioFailure(t *testing.T) {
	want := errors.New("chat stream failed")
	suite := Suite{
		ID: "chat",
		Scenarios: []Scenario{
			{
				ID: "failed", Timeout: time.Second,
				Run: func(context.Context) (Result, error) {
					return Result{}, want
				},
			},
			{
				ID: "passed", Timeout: time.Second,
				Run: func(context.Context) (Result, error) {
					return Result{Checks: []Check{{Name: "ok", Passed: true}}}, nil
				},
			},
		},
	}

	report, err := mustRunner(t, nil, 2).Run(context.Background(), suite)

	require.NoError(t, err)
	require.Len(t, report.Scenarios, 2)
	assert.Equal(t, want.Error(), report.Scenarios[0].Error)
	assert.True(t, report.Scenarios[1].Passed)
	assert.False(t, report.Passed)
}

func TestRunnerClassifiesHookAndProtocolErrors(t *testing.T) {
	hookErr := errors.New("chat stream failed")
	tests := []struct {
		name    string
		run     func(context.Context) (Result, error)
		wantErr string
	}{
		{
			name: "hook error",
			run: func(context.Context) (Result, error) {
				return Result{}, hookErr
			},
			wantErr: hookErr.Error(),
		},
		{
			name: "invalid result",
			run: func(context.Context) (Result, error) {
				return Result{}, nil
			},
			wantErr: "result must contain at least one check or claim",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			suite := Suite{ID: "chat", Scenarios: []Scenario{{
				ID: "case", Timeout: time.Second, Run: test.run,
			}}}
			report, err := mustRunner(t, nil, 1).Run(context.Background(), suite)
			require.NoError(t, err)
			require.Len(t, report.Scenarios, 1)
			assert.False(t, report.Passed)
			assert.Equal(t, test.wantErr, report.Scenarios[0].Error)
		})
	}
}

func TestRunnerRequiresJudgeForSemanticAssertions(t *testing.T) {
	t.Run("scenario claim", func(t *testing.T) {
		suite := Suite{ID: "chat", Scenarios: []Scenario{{
			ID: "case", Timeout: time.Second,
			Run: func(context.Context) (Result, error) {
				return Result{
					Output: "On.",
					Claims: []Claim{{ID: "on", Text: "It is on."}},
				}, nil
			},
		}}}

		report, err := mustRunner(t, nil, 1).Run(context.Background(), suite)

		require.NoError(t, err)
		require.Len(t, report.Scenarios, 1)
		assert.Equal(t, "semantic judge is required", report.Scenarios[0].Error)
	})
}

func TestScenarioDurationIncludesSemanticJudging(t *testing.T) {
	started := time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
	now := started
	judge := &scriptedJudge{
		responses: [][]Judgment{{{ClaimID: "answer", Label: Entailed, Rationale: "Exact."}}},
		errors:    []error{nil},
		onJudge: func() {
			now = now.Add(2 * time.Second)
		},
	}
	runner := mustRunner(t, judge, 1)
	runner.now = func() time.Time {
		return now
	}
	scenario := Scenario{
		ID: "case", Timeout: time.Minute,
		Run: func(context.Context) (Result, error) {
			now = now.Add(time.Second)
			return Result{
				Output: "Complete.",
				Claims: []Claim{{ID: "answer", Text: "The answer is complete."}},
			}, nil
		},
	}

	report := runner.runScenario(context.Background(), scenario)

	assert.Equal(t, 3*time.Second, report.Duration)
}

func calibrationJudgments() []Judgment {
	return []Judgment{
		{ClaimID: "calibration_entailed", Label: Entailed, Rationale: "Exact match."},
		{ClaimID: "calibration_contradicted", Label: Contradicted, Rationale: "Opposite state."},
		{ClaimID: "calibration_not_addressed", Label: NotAddressed, Rationale: "Different equipment."},
		{ClaimID: "calibration_indeterminate", Label: Indeterminate, Rationale: "Conflicting evidence."},
	}
}

func mustRunner(t *testing.T, judge Judge, maxConcurrency int) *Runner {
	t.Helper()
	runner, err := NewRunner(judge, RunnerConfig{MaxConcurrency: maxConcurrency})
	require.NoError(t, err)
	return runner
}

func reportScenarioIDs(report Report) []string {
	ids := make([]string, len(report.Scenarios))
	for index, scenario := range report.Scenarios {
		ids[index] = scenario.ID
	}
	return ids
}

func selectionSuite() Suite {
	passing := func(context.Context) (Result, error) {
		return Result{Checks: []Check{{Name: "ok", Passed: true}}}, nil
	}
	return Suite{
		ID: "chat",
		Scenarios: []Scenario{
			{ID: "first", Tags: []string{"smoke"}, Timeout: time.Second, Run: passing},
			{ID: "second", Tags: []string{"slow"}, Timeout: time.Second, Run: passing},
			{ID: "third", Tags: []string{"smoke", "slow"}, Timeout: time.Second, Run: passing},
		},
	}
}
