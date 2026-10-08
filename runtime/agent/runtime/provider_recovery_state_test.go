package runtime

// These tests use measured workflow intervals to check shared allowance and
// active-clock rules without starting an engine or calling a model provider.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestProviderRecoveryAllowanceCountsOverlapOnce(t *testing.T) {
	at := func(seconds int) time.Time { return time.Unix(int64(seconds), 0) }
	tests := []struct {
		name      string
		intervals [][2]int
		remaining time.Duration
	}{
		{"overlapping failures", [][2]int{{1, 6}, {3, 8}}, 3 * time.Second},
		{"wait overlaps failure", [][2]int{{1, 6}, {4, 9}, {8, 10}}, time.Second},
		{"replayed observation", [][2]int{{1, 6}, {1, 6}, {1, 4}}, 5 * time.Second},
		{"disjoint intervals", [][2]int{{1, 4}, {6, 9}}, 4 * time.Second},
		{"late evidence joins intervals", [][2]int{{8, 10}, {1, 3}, {2, 9}}, time.Second},
		{"entry without elapsed time", [][2]int{{1, 1}}, 10 * time.Second},
		{"initial failure overrun", [][2]int{{1, 15}}, 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			a := providerRecoveryAllowance{initial: 10 * time.Second}
			for _, interval := range test.intervals {
				a.confirm(providerRecoveryInterval{start: at(interval[0]), end: at(interval[1])})
			}
			assert.Equal(t, test.remaining, a.remaining())
			assert.Equal(t, test.remaining == 0, a.exhausted)
		})
	}
}

func TestProviderRecoveryAllowanceSharesHeldCoverage(t *testing.T) {
	now := time.Unix(100, 0)
	a := providerRecoveryAllowance{initial: 10 * time.Second}
	first := a.reserve(now, time.Time{})
	require.NotNil(t, first)
	second := a.reserve(now.Add(3*time.Second), time.Time{})
	require.NotNil(t, second)
	assert.Equal(t, now.Add(10*time.Second), first.interval.end)
	assert.Equal(t, first.interval.end, second.interval.end)
	assert.Equal(t, 10*time.Second, a.remaining())
	assert.False(t, a.exhausted)

	// An expired but unresolved phase still holds its capacity. Expiry does
	// not establish whether an activity ran or whether a reply was lost.
	assert.Nil(t, a.reserve(now.Add(11*time.Second), time.Time{}))
	a.release(first)
	third := a.reserve(now.Add(11*time.Second), time.Time{})
	require.NotNil(t, third)
	assert.Equal(t, now.Add(14*time.Second), third.interval.end)
}

func TestProviderRecoveryAllowanceLateFailureDoesNotRenewPermission(t *testing.T) {
	now := time.Unix(100, 0)
	a := providerRecoveryAllowance{initial: 10 * time.Second}
	hold := a.reserve(now, time.Time{})
	require.NotNil(t, hold)
	expiry := hold.interval.end

	a.confirm(providerRecoveryInterval{start: now.Add(-4 * time.Second), end: now})
	assert.Equal(t, 6*time.Second, a.remaining())
	assert.False(t, a.exhausted)
	assert.Nil(t, a.reserve(now.Add(time.Second), time.Time{}))
	assert.Equal(t, expiry, hold.interval.end)

	a.confirm(providerRecoveryInterval{start: now.Add(-12 * time.Second), end: now})
	assert.Zero(t, a.remaining())
	assert.True(t, a.exhausted)
	a.release(hold)
	assert.Nil(t, a.reserve(now, time.Time{}))
	assert.Equal(t, expiry, hold.interval.end)
	assert.Equal(t, 12*time.Second, a.confirmed.duration())
}

func TestProviderRecoveryAllowanceRestoresOnlyRemainder(t *testing.T) {
	now := time.Unix(100, 0)
	a := providerRecoveryAllowance{initial: 10 * time.Second}
	a.confirm(providerRecoveryInterval{start: now, end: now.Add(7 * time.Second)})
	restored := providerRecoveryAllowance{initial: a.remaining()}
	hold := restored.reserve(now.Add(time.Hour), time.Time{})
	require.NotNil(t, hold)
	assert.Equal(t, 3*time.Second, hold.interval.end.Sub(hold.interval.start))
}

func TestProviderRecoveryAllowanceClipsExistingPhaseLimit(t *testing.T) {
	now := time.Unix(100, 0)
	for _, seconds := range []int{0, 3, 10, 11} {
		a := providerRecoveryAllowance{initial: 10 * time.Second}
		hold := a.reserve(now, now.Add(time.Duration(seconds)*time.Second))
		if seconds == 0 {
			assert.Nil(t, hold)
			continue
		}
		require.NotNil(t, hold)
		assert.Equal(t, time.Duration(min(seconds, 10))*time.Second, hold.interval.end.Sub(now))
	}
}

func TestProviderRecoveryClockKeepsHealthyAndUnknownBranchesActive(t *testing.T) {
	start := time.Unix(100, 0)
	clock := providerRecoveryClock{}
	first := clock.branch(start)
	second := clock.branch(start)
	clock.beginWait(start)
	first.paused = first.paused.add(providerRecoveryInterval{start: start, end: start.Add(10 * time.Second)})
	assert.Zero(t, clock.observe(start.Add(5*time.Second)))

	// The second branch completes through its ordinary future. Only the
	// subsequent period, when the remaining branch is paused, earns credit.
	second.end = start.Add(5 * time.Second)
	assert.Equal(t, 5*time.Second, clock.observe(start.Add(10*time.Second)))
	clock.endWait(start.Add(10 * time.Second))
	assert.Zero(t, clock.observe(start.Add(20*time.Second)))
}

func TestProviderRecoveryClockIntersectsBranchPauses(t *testing.T) {
	start := time.Unix(100, 0)
	clock := providerRecoveryClock{}
	first := clock.branch(start)
	second := clock.branch(start)
	first.paused = first.paused.add(providerRecoveryInterval{start: start, end: start.Add(8 * time.Second)})
	second.paused = second.paused.add(providerRecoveryInterval{start: start.Add(3 * time.Second), end: start.Add(10 * time.Second)})
	clock.beginWait(start.Add(time.Second))
	clock.endWait(start.Add(6 * time.Second))
	assert.Equal(t, 3*time.Second, clock.observe(start.Add(10*time.Second)))
	assert.Zero(t, clock.observe(start.Add(10*time.Second)))
}

func TestProviderRecoveryClockRetainsWholeFailureAndTerminalWinner(t *testing.T) {
	start := time.Unix(100, 0)
	clock := providerRecoveryClock{}
	clock.own = clock.own.add(providerRecoveryInterval{start: start, end: start.Add(12 * time.Second)})
	assert.Equal(t, 12*time.Second, clock.observe(start.Add(12*time.Second)))
	clock.closed = true
	clock.own = clock.own.add(providerRecoveryInterval{start: start, end: start.Add(20 * time.Second)})
	assert.Zero(t, clock.observe(start.Add(20*time.Second)))
	assert.Equal(t, 12*time.Second, clock.credited.duration())
}
