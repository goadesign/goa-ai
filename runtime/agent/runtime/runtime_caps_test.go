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

func TestWithRunMaxToolCallsRejectsNonPositive(t *testing.T) {
	require.Panics(t, func() {
		WithRunMaxToolCalls(0)(&RunInput{})
	})
	require.Panics(t, func() {
		WithRunMaxToolCalls(-1)(&RunInput{})
	})
}

func TestWithRunMaxRecoveryTurnsRejectsNonPositive(t *testing.T) {
	require.Panics(t, func() {
		WithRunMaxRecoveryTurns(0)(&RunInput{})
	})
	require.Panics(t, func() {
		WithRunMaxRecoveryTurns(-1)(&RunInput{})
	})
}
