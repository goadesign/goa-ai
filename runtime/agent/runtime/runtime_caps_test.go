package runtime

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/policy"
)

func TestMergeCaps_DoesNotRaiseRunCaps(t *testing.T) {
	current := policy.CapsState{
		MaxToolCalls:                        10,
		RemainingToolCalls:                  2,
		MaxConsecutiveFailedToolCalls:       3,
		RemainingConsecutiveFailedToolCalls: 1,
	}
	decision := policy.CapsState{
		MaxToolCalls:                        20,
		RemainingToolCalls:                  5,
		MaxConsecutiveFailedToolCalls:       6,
		RemainingConsecutiveFailedToolCalls: 4,
	}

	merged := mergeCaps(current, decision)
	require.Equal(t, 10, merged.MaxToolCalls)
	require.Equal(t, 2, merged.RemainingToolCalls)
	require.Equal(t, 3, merged.MaxConsecutiveFailedToolCalls)
	require.Equal(t, 1, merged.RemainingConsecutiveFailedToolCalls)
}

func TestWithRunMaxToolCallsRejectsNonPositive(t *testing.T) {
	require.Panics(t, func() {
		WithRunMaxToolCalls(0)(&RunInput{})
	})
	require.Panics(t, func() {
		WithRunMaxToolCalls(-1)(&RunInput{})
	})
}

func TestWithRunMaxConsecutiveFailedToolCallsRejectsNonPositive(t *testing.T) {
	require.Panics(t, func() {
		WithRunMaxConsecutiveFailedToolCalls(0)(&RunInput{})
	})
	require.Panics(t, func() {
		WithRunMaxConsecutiveFailedToolCalls(-1)(&RunInput{})
	})
}

func TestMergeCaps_DoesNotRelaxExpiry(t *testing.T) {
	now := time.Now()
	current := policy.CapsState{ExpiresAt: now.Add(5 * time.Minute)}
	decision := policy.CapsState{ExpiresAt: now.Add(10 * time.Minute)}

	merged := mergeCaps(current, decision)
	require.Equal(t, current.ExpiresAt, merged.ExpiresAt)
}
