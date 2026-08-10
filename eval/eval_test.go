package eval

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedJudge struct {
	responses [][]Judgment
	errors    []error
	requests  [][]Assertion
	onJudge   func()
}

func (j *scriptedJudge) Judge(_ context.Context, assertions []Assertion) ([]Judgment, error) {
	if j.onJudge != nil {
		j.onJudge()
	}
	j.requests = append(j.requests, assertions)
	index := len(j.requests) - 1
	return j.responses[index], j.errors[index]
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
			name:    "claims without output",
			result:  Result{Claims: []Claim{{ID: "complete", Text: "Complete."}}},
			wantErr: "claims require output",
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
			err := ValidateResult(test.result)
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
				ID: "first", Input: "one", Tags: []string{"smoke"}, Timeout: time.Second,
				Run: func(_ context.Context, input string) (Result, error) {
					order = append(order, input)
					return Result{
						Checks: []Check{{Name: "typed", Passed: true}},
						Claims: []Claim{{ID: "answer", Text: "The answer is correct."}},
						Output: "Correct.",
					}, nil
				},
			},
			{
				ID: "second", Input: "two", Tags: []string{"extended"}, Timeout: time.Second,
				Run: func(_ context.Context, input string) (Result, error) {
					order = append(order, input)
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
			Run: func(context.Context, string) (Result, error) {
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
		Run: func(context.Context, string) (Result, error) {
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
			Run: func(context.Context, string) (Result, error) {
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
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	outcome := <-finished
	require.NoError(t, outcome.err)
	assert.Equal(t, []string{"case_0", "case_1", "case_2", "case_3"}, reportScenarioIDs(outcome.report))
	assert.True(t, outcome.report.Passed)
}

func TestRunnerContinuesAfterScenarioFailure(t *testing.T) {
	want := errors.New("chat stream failed")
	suite := Suite{
		ID: "chat",
		Scenarios: []Scenario{
			{
				ID: "failed", Timeout: time.Second,
				Run: func(context.Context, string) (Result, error) {
					return Result{}, want
				},
			},
			{
				ID: "passed", Timeout: time.Second,
				Run: func(context.Context, string) (Result, error) {
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
		run     func(context.Context, string) (Result, error)
		wantErr string
	}{
		{
			name: "hook error",
			run: func(context.Context, string) (Result, error) {
				return Result{}, hookErr
			},
			wantErr: hookErr.Error(),
		},
		{
			name: "invalid result",
			run: func(context.Context, string) (Result, error) {
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
			Run: func(context.Context, string) (Result, error) {
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
		Run: func(context.Context, string) (Result, error) {
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
	passing := func(context.Context, string) (Result, error) {
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
