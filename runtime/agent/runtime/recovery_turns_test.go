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

	require.Equal(t, policy.DefaultMaxRecoveryTurns, caps.MaxRecoveryTurns)
	require.Equal(t, policy.DefaultMaxRecoveryTurns, caps.RemainingRecoveryTurns)
}

func TestResetRecoveryTurns(t *testing.T) {
	t.Parallel()

	caps := policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 1}
	resetRecoveryTurns(&caps)
	require.Equal(t, 3, caps.RemainingRecoveryTurns)
}

func TestSuccessfulBudgetedResult(t *testing.T) {
	t.Parallel()

	budgeted := newAnyJSONSpec("catalog.search", "catalog")
	budgetedOK := newAnyJSONSpec("catalog.lookup", "catalog")
	progressSpec := newBookkeepingSpec("runs.progress.update")

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
	}{
		{
			name: "mixed parallel batch reports progress",
			records: []stepToolRecord{
				record(budgeted.Name, true),
				record(budgetedOK.Name, false),
			},
			wantProgress: true,
		},
		{
			name: "all budgeted failures do not report progress",
			records: []stepToolRecord{
				record(budgeted.Name, true),
				record(budgetedOK.Name, true),
			},
		},
		{
			name: "bookkeeping success is not progress",
			records: []stepToolRecord{
				record(progressSpec.Name, false),
				record(budgeted.Name, true),
			},
		},
		{
			name: "bookkeeping-only batch does not report progress",
			records: []stepToolRecord{
				record(progressSpec.Name, false),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.wantProgress, rt.hasSuccessfulBudgetedResult(tc.records))
		})
	}
}
