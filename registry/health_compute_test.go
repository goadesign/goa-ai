package registry

// health_compute_test.go covers computeToolsetHealth, the pure owner of the
// staleness rule. These tables replace former property tests that exercised
// the same arithmetic through Redis, replicated maps, and pool nodes; the
// distributed plumbing is covered separately by the integration-tagged tests.

import (
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestComputeToolsetHealth(t *testing.T) {
	t.Parallel()

	const (
		token     = "registration-1"
		threshold = 300 * time.Millisecond
	)
	now := time.Unix(1_700_000_000, 0)
	record := func(provider string, registered, lastPong time.Time) healthRecord {
		r := healthRecord{
			ProviderID:         provider,
			RegistrationToken:  token,
			RegisteredUnixNano: registered.UnixNano(),
		}
		if !lastPong.IsZero() {
			r.LastPongUnixNano = lastPong.UnixNano()
		}
		return r
	}
	registered := now.Add(-time.Minute)

	cases := []struct {
		name        string
		records     []healthRecord
		wantHealthy bool
		wantCounts  [2]int // ProviderCount, HealthyProviderCount
		wantID      string
	}{
		{
			name:        "no records",
			records:     nil,
			wantHealthy: false,
			wantCounts:  [2]int{0, 0},
		},
		{
			name: "fresh pong is healthy",
			records: []healthRecord{
				record("provider-a", registered, now.Add(-threshold/2)),
			},
			wantHealthy: true,
			wantCounts:  [2]int{1, 1},
			wantID:      "provider-a",
		},
		{
			name: "pong exactly at threshold is healthy",
			records: []healthRecord{
				record("provider-a", registered, now.Add(-threshold)),
			},
			wantHealthy: true,
			wantCounts:  [2]int{1, 1},
			wantID:      "provider-a",
		},
		{
			name: "pong past threshold is unhealthy",
			records: []healthRecord{
				record("provider-a", registered, now.Add(-threshold-time.Nanosecond)),
			},
			wantHealthy: false,
			wantCounts:  [2]int{1, 0},
			wantID:      "provider-a",
		},
		{
			name: "registered provider without pong is counted but not healthy",
			records: []healthRecord{
				record("provider-a", registered, time.Time{}),
			},
			wantHealthy: false,
			wantCounts:  [2]int{1, 0},
			wantID:      "provider-a",
		},
		{
			name: "fresh pong restores health after staleness",
			records: []healthRecord{
				record("provider-a", registered, now.Add(-threshold/4)),
			},
			wantHealthy: true,
			wantCounts:  [2]int{1, 1},
			wantID:      "provider-a",
		},
		{
			name: "other registration epoch is ignored",
			records: []healthRecord{
				{
					ProviderID:         "provider-old",
					RegistrationToken:  "registration-0",
					RegisteredUnixNano: registered.UnixNano(),
					LastPongUnixNano:   now.UnixNano(),
				},
			},
			wantHealthy: false,
			wantCounts:  [2]int{0, 0},
		},
		{
			name: "any fresh provider keeps the toolset healthy",
			records: []healthRecord{
				record("provider-stale", registered, now.Add(-time.Hour)),
				record("provider-fresh", registered, now.Add(-threshold/2)),
			},
			wantHealthy: true,
			wantCounts:  [2]int{2, 1},
			wantID:      "provider-fresh",
		},
		{
			name: "freshest pong wins provider identity",
			records: []healthRecord{
				record("provider-b", registered, now.Add(-2*time.Second)),
				record("provider-a", registered, now.Add(-time.Second)),
			},
			wantHealthy: false,
			wantCounts:  [2]int{2, 0},
			wantID:      "provider-a",
		},
		{
			// A newly registered provider that has not ponged yet must not
			// steal provider identity from the provider with the freshest
			// pong, in any record order — Health feeds records in replicated
			// map iteration order.
			name: "ponged provider outranks newer registration without pong",
			records: []healthRecord{
				record("provider-a", registered, now.Add(-threshold/2)),
				record("provider-new", now.Add(-time.Second), time.Time{}),
			},
			wantHealthy: true,
			wantCounts:  [2]int{2, 1},
			wantID:      "provider-a",
		},
		{
			name: "newest registration wins identity when no provider ponged",
			records: []healthRecord{
				record("provider-old", registered, time.Time{}),
				record("provider-new", now.Add(-time.Second), time.Time{}),
			},
			wantHealthy: false,
			wantCounts:  [2]int{2, 0},
			wantID:      "provider-new",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			// The result must not depend on record order: Health feeds
			// records in replicated-map iteration order.
			for _, records := range [][]healthRecord{tc.records, reversed(tc.records)} {
				health := computeToolsetHealth(records, token, now, threshold)

				assert.Equal(t, tc.wantHealthy, health.Healthy)
				assert.Equal(t, tc.wantCounts[0], health.ProviderCount)
				assert.Equal(t, tc.wantCounts[1], health.HealthyProviderCount)
				assert.Equal(t, tc.wantID, health.ProviderID)
				assert.Equal(t, threshold, health.StalenessThreshold)
				if !health.LastPong.IsZero() {
					assert.Equal(t, now.Sub(health.LastPong), health.Age)
				}
			}
		})
	}
}

// reversed returns a reversed copy of records without mutating the input.
func reversed(records []healthRecord) []healthRecord {
	out := slices.Clone(records)
	slices.Reverse(out)
	return out
}

// The staleness window scales linearly with the missed-ping allowance:
// deriveStalenessThreshold yields (missedPingThreshold + 1) * pingInterval —
// a provider may miss the allowed pings and still answer the next one. This
// pins the exact derivation NewHealthTracker uses so a config change cannot
// silently shrink the window providers have to respond.
func TestComputeToolsetHealthThresholdScalesWithMissedPings(t *testing.T) {
	t.Parallel()

	const pingInterval = 100 * time.Millisecond
	now := time.Unix(1_700_000_000, 0)

	for _, missed := range []int{1, 2, 5, 10} {
		threshold := deriveStalenessThreshold(pingInterval, missed)
		assert.Equal(t, time.Duration(missed+1)*pingInterval, threshold, "missedPingThreshold=%d", missed)

		record := healthRecord{
			ProviderID:         "provider-a",
			RegistrationToken:  "registration-1",
			RegisteredUnixNano: now.Add(-time.Minute).UnixNano(),
			LastPongUnixNano:   now.Add(-threshold).UnixNano(),
		}
		atWindow := computeToolsetHealth([]healthRecord{record}, "registration-1", now, threshold)
		assert.True(t, atWindow.Healthy, "pong at the window edge must be healthy (missed=%d)", missed)

		record.LastPongUnixNano = now.Add(-threshold - time.Nanosecond).UnixNano()
		pastWindow := computeToolsetHealth([]healthRecord{record}, "registration-1", now, threshold)
		assert.False(t, pastWindow.Healthy, "pong past the window must be unhealthy (missed=%d)", missed)
	}
}
