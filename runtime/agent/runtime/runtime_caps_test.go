package runtime

import (
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/policy"
)

func TestMergeCaps_DoesNotRaiseRunCaps(t *testing.T) {
	current := policy.CapsState{
		MaxToolCalls:           10,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       3,
		RemainingRecoveryTurns: 1,
	}
	decision := policy.CapsState{
		MaxToolCalls:           20,
		RemainingToolCalls:     5,
		MaxRecoveryTurns:       6,
		RemainingRecoveryTurns: 4,
	}

	merged := mergeCaps(current, decision)
	require.Equal(t, 10, merged.MaxToolCalls)
	require.Equal(t, 2, merged.RemainingToolCalls)
	require.Equal(t, 3, merged.MaxRecoveryTurns)
	require.Equal(t, 1, merged.RemainingRecoveryTurns)
}

func TestMergeCapsKeepsRecoveryMaximumAndRemainingConsistent(t *testing.T) {
	tests := []struct {
		name      string
		current   policy.CapsState
		decision  policy.CapsState
		wantMax   int
		wantTurns int
	}{
		{
			name:      "explicit exhaustion",
			current:   policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 3},
			decision:  policy.CapsState{MaxRecoveryTurns: 1},
			wantMax:   1,
			wantTurns: 0,
		},
		{
			name:      "tighter maximum and remaining",
			current:   policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 3},
			decision:  policy.CapsState{MaxRecoveryTurns: 2, RemainingRecoveryTurns: 1},
			wantMax:   2,
			wantTurns: 1,
		},
		{
			name:      "omitted update",
			current:   policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 2},
			decision:  policy.CapsState{},
			wantMax:   3,
			wantTurns: 2,
		},
		{
			name:      "decision cannot restore spent turns",
			current:   policy.CapsState{MaxRecoveryTurns: 2, RemainingRecoveryTurns: 0},
			decision:  policy.CapsState{MaxRecoveryTurns: 3, RemainingRecoveryTurns: 3},
			wantMax:   2,
			wantTurns: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			merged := mergeCaps(test.current, test.decision)
			require.Equal(t, test.wantMax, merged.MaxRecoveryTurns)
			require.Equal(t, test.wantTurns, merged.RemainingRecoveryTurns)
		})
	}
}

func TestWithRunMaxToolCallsRejectsNonPositive(t *testing.T) {
	require.Panics(t, func() {
		WithRunMaxToolCalls(0).apply(&runStart{})
	})
	require.Panics(t, func() {
		WithRunMaxToolCalls(-1).apply(&runStart{})
	})
}

func TestWithRunMaxRecoveryTurnsRejectsNonPositive(t *testing.T) {
	require.Panics(t, func() {
		WithRunMaxRecoveryTurns(0).apply(&runStart{})
	})
	require.Panics(t, func() {
		WithRunMaxRecoveryTurns(-1).apply(&runStart{})
	})
}
