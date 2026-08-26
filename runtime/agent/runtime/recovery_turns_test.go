package runtime

// recovery_turns_test.go verifies that each replacement planner activity
// consumes one recovery turn and successful budgeted work resets the
// consecutive budget.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestConsumeRecoveryTurn(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		caps          policy.CapsState
		wantRemaining int
		wantAllowed   bool
	}{
		{
			name:          "configured recovery consumes one turn",
			caps:          policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 3},
			wantRemaining: 2,
			wantAllowed:   true,
		},
		{
			name:          "final configured turn remains available",
			caps:          policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 1},
			wantRemaining: 0,
			wantAllowed:   true,
		},
		{
			name:          "exhausted cap rejects another recovery",
			caps:          policy.CapsState{MaxRecoveryTurns: 3},
			wantRemaining: 0,
		},
		{
			name:          "uncapped restored state permits recovery",
			caps:          policy.CapsState{},
			wantRemaining: 0,
			wantAllowed:   true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := tc.caps
			allowed := consumeRecoveryTurn(&caps)
			require.Equal(t, tc.wantAllowed, allowed)
			require.Equal(t, tc.wantRemaining, caps.RemainingRecoveryTurns)
		})
	}
}

func TestInitialCapsUseDefaultRecoveryTurns(t *testing.T) {
	t.Parallel()

	caps := initialCaps(RunPolicy{})

	require.Equal(t, defaultMaxRecoveryTurns, caps.MaxRecoveryTurns)
	require.Equal(t, defaultMaxRecoveryTurns, caps.RemainingRecoveryTurns)
}

func TestResetRecoveryTurns(t *testing.T) {
	t.Parallel()

	caps := policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 1}
	resetRecoveryTurns(&caps)
	require.Equal(t, 3, caps.RemainingRecoveryTurns)

	unconfigured := policy.CapsState{}
	resetRecoveryTurns(&unconfigured)
	require.Zero(t, unconfigured.RemainingRecoveryTurns)
}

func TestApplyLegacyFailureStreak(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		progress      bool
		failed        bool
		caps          policy.CapsState
		wantRemaining int
		wantExhausted bool
	}{
		{
			name:          "failed batch decrements",
			failed:        true,
			caps:          policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 3},
			wantRemaining: 2,
		},
		{
			name:          "mixed batch resets",
			progress:      true,
			failed:        true,
			caps:          policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 1},
			wantRemaining: 3,
		},
		{
			name:          "final failed batch exhausts",
			failed:        true,
			caps:          policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 1},
			wantExhausted: true,
		},
		{
			name:   "historically uncapped state remains uncapped",
			failed: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			caps := test.caps
			exhausted := applyLegacyFailureStreak(&caps, test.progress, test.failed)
			require.Equal(t, test.wantRemaining, caps.RemainingRecoveryTurns)
			require.Equal(t, test.wantExhausted, exhausted)
		})
	}
}

func TestBudgetedBatchOutcome(t *testing.T) {
	t.Parallel()

	budgeted := newAnyJSONSpec("ada.get_energy_rates", "ada")
	budgetedOK := newAnyJSONSpec("ada.get_weather_forecast", "ada")
	progressSpec := newBookkeepingSpec("tasks.progress.update")

	rt := New()
	seedTestToolSpecs(rt, budgeted, budgetedOK, progressSpec)

	record := func(name tools.Ident, failed bool) stepToolRecord {
		result := &planner.ToolResult{Name: name, ToolCallID: "call-" + string(name)}
		if failed {
			result.Failure = testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "boom")
		}
		return stepToolRecord{
			call:   ToolCall{Name: name, ToolCallID: "call-" + string(name)},
			result: result,
		}
	}

	cases := []struct {
		name         string
		records      []stepToolRecord
		wantProgress bool
		wantFailed   bool
	}{
		{
			name: "mixed parallel batch reports progress and failure",
			records: []stepToolRecord{
				record(budgeted.Name, true),
				record(budgetedOK.Name, false),
			},
			wantProgress: true,
			wantFailed:   true,
		},
		{
			name: "all budgeted failures report failure only",
			records: []stepToolRecord{
				record(budgeted.Name, true),
				record(budgetedOK.Name, true),
			},
			wantFailed: true,
		},
		{
			name: "bookkeeping success is not progress",
			records: []stepToolRecord{
				record(progressSpec.Name, false),
				record(budgeted.Name, true),
			},
			wantFailed: true,
		},
		{
			name: "bookkeeping-only batch reports neither",
			records: []stepToolRecord{
				record(progressSpec.Name, false),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			progress, failed := rt.budgetedBatchOutcome(tc.records)
			require.Equal(t, tc.wantProgress, progress)
			require.Equal(t, tc.wantFailed, failed)
		})
	}
}
