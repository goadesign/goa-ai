package eval

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type scriptedJudge struct {
	responses [][]Judgment
	errors    []error
	requests  [][]Assertion
}

func (j *scriptedJudge) Judge(_ context.Context, assertions []Assertion) ([]Judgment, error) {
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

func TestRunnerCalibratesThenRunsSequentially(t *testing.T) {
	judge := &scriptedJudge{
		responses: [][]Judgment{
			{{ClaimID: "entailed", Label: Entailed, Rationale: "Exact."}},
			{{ClaimID: "answer", Label: Entailed, Rationale: "Exact."}},
		},
		errors: []error{nil, nil},
	}
	var order []string
	suite := Suite{
		ID: "chat",
		Calibrations: []Calibration{{
			ID: "entailed", Answer: "The pump is on.", Claim: "The pump is on.", Want: Entailed,
		}},
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

	report, err := NewRunner(judge).Run(context.Background(), suite, "smoke")

	require.NoError(t, err)
	assert.True(t, report.Passed)
	require.Len(t, report.Scenarios, 1)
	assert.Equal(t, "first", report.Scenarios[0].ID)
	assert.Equal(t, []string{"one"}, order)
	require.Len(t, judge.requests, 2)
	assert.Equal(t, "The pump is on.", judge.requests[0][0].Output)
	assert.Equal(t, "Correct.", judge.requests[1][0].Output)
}

func TestRunnerAbortsAfterCalibrationFailure(t *testing.T) {
	judge := &scriptedJudge{
		responses: [][]Judgment{{{ClaimID: "entailed", Label: Contradicted, Rationale: "Wrong."}}},
		errors:    []error{nil},
	}
	run := false
	suite := Suite{
		ID: "chat",
		Calibrations: []Calibration{{
			ID: "entailed", Answer: "On.", Claim: "On.", Want: Entailed,
		}},
		Scenarios: []Scenario{{
			ID: "case", Timeout: time.Second,
			Run: func(context.Context, string) (Result, error) {
				run = true
				return Result{}, nil
			},
		}},
	}

	report, err := NewRunner(judge).Run(context.Background(), suite)

	require.ErrorIs(t, err, errCalibration)
	assert.False(t, report.Passed)
	assert.Contains(t, report.Error, "judge calibration failed")
	assert.False(t, run)
}

func TestRunnerRejectsEmptySelectionBeforeCalibration(t *testing.T) {
	judge := &scriptedJudge{}
	suite := Suite{ID: "chat", Scenarios: []Scenario{{
		ID: "case", Tags: []string{"production"}, Timeout: time.Second,
		Run: func(context.Context, string) (Result, error) {
			return Result{Checks: []Check{{Name: "ok", Passed: true}}}, nil
		},
	}}}

	report, err := NewRunner(judge).Run(context.Background(), suite, "missing")

	require.ErrorContains(t, err, "no scenarios")
	assert.Equal(t, "evaluation selection contains no scenarios", report.Error)
	assert.Empty(t, judge.requests)
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
			report, err := NewRunner(nil).Run(context.Background(), suite)
			require.NoError(t, err)
			require.Len(t, report.Scenarios, 1)
			assert.False(t, report.Passed)
			assert.Equal(t, test.wantErr, report.Scenarios[0].Error)
		})
	}
}

func TestRunnerRequiresJudgeForSemanticAssertions(t *testing.T) {
	t.Run("calibration", func(t *testing.T) {
		suite := Suite{
			ID: "chat",
			Calibrations: []Calibration{{
				ID: "entailed", Answer: "On.", Claim: "On.", Want: Entailed,
			}},
			Scenarios: []Scenario{{
				ID: "case", Timeout: time.Second,
				Run: func(context.Context, string) (Result, error) {
					return Result{Checks: []Check{{Name: "ok", Passed: true}}}, nil
				},
			}},
		}

		_, err := NewRunner(nil).Run(context.Background(), suite)

		assert.ErrorContains(t, err, "semantic judge is required")
	})

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

		report, err := NewRunner(nil).Run(context.Background(), suite)

		require.NoError(t, err)
		require.Len(t, report.Scenarios, 1)
		assert.Equal(t, "semantic judge is required", report.Scenarios[0].Error)
	})
}
