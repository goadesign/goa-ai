package runtime

// failure_streak_test.go verifies the consecutive-failure cap counts planner
// decision points, not individual parallel calls: any budgeted success resets
// the streak, an all-failure batch consumes exactly one unit, and bookkeeping
// results never move the counter.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestApplyFailureStreak(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name          string
		caps          policy.CapsState
		progress      bool
		failed        bool
		wantRemaining int
		wantTripped   bool
	}{
		{
			name:          "budgeted success resets streak",
			caps:          policy.CapsState{MaxConsecutiveFailedToolCalls: 3, RemainingConsecutiveFailedToolCalls: 1},
			progress:      true,
			failed:        true,
			wantRemaining: 3,
		},
		{
			name:          "all-failure batch consumes one unit",
			caps:          policy.CapsState{MaxConsecutiveFailedToolCalls: 3, RemainingConsecutiveFailedToolCalls: 3},
			failed:        true,
			wantRemaining: 2,
		},
		{
			name:          "final all-failure batch trips the cap",
			caps:          policy.CapsState{MaxConsecutiveFailedToolCalls: 3, RemainingConsecutiveFailedToolCalls: 1},
			failed:        true,
			wantRemaining: 0,
			wantTripped:   true,
		},
		{
			name:          "bookkeeping-only batch leaves the counter unchanged",
			caps:          policy.CapsState{MaxConsecutiveFailedToolCalls: 3, RemainingConsecutiveFailedToolCalls: 2},
			wantRemaining: 2,
		},
		{
			name:          "unset cap never trips",
			caps:          policy.CapsState{},
			failed:        true,
			wantRemaining: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			caps := tc.caps
			tripped := applyFailureStreak(&caps, tc.progress, tc.failed)
			require.Equal(t, tc.wantTripped, tripped)
			require.Equal(t, tc.wantRemaining, caps.RemainingConsecutiveFailedToolCalls)
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
			result.Error = planner.NewToolError("boom")
		}
		return stepToolRecord{
			call:   planner.ToolRequest{Name: name, ToolCallID: "call-" + string(name)},
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
