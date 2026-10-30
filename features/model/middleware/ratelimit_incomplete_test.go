// Package middleware tests incomplete token-usage settlement across failed and
// early-closed rate-limited model calls.
package middleware

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

// TestUsageReconciledRateLimiterIncompleteFailureDoesNotRefundReservation
// verifies that partial usage retained on a failed model call charges the
// larger of the initial reservation and the observed partial token total.
func TestUsageReconciledRateLimiterIncompleteFailureDoesNotRefundReservation(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want float64
	}{
		{
			name: "partial error usage below reservation keeps reservation",
			err: model.RetainIncompleteUsage(errors.New("round 2 failed"), model.TokenUsage{
				InputTokens: 12, OutputTokens: 3, TotalTokens: 15,
			}),
			want: 200,
		},
		{
			name: "partial error usage above reservation charges overrun",
			err: model.RetainIncompleteUsage(errors.New("round 2 failed"), model.TokenUsage{
				InputTokens: 220, OutputTokens: 30, TotalTokens: 250,
			}),
			want: 150,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 400, 400)
			require.NoError(t, err)
			provider, err := l.WrapProvider(&fakeCountingClient{
				fakeClient: fakeClient{completeErr: tc.err},
				count:      model.TokenCount{InputTokens: 100, Exact: false},
			})
			require.NoError(t, err)
			_, err = provider.Complete(t.Context(), &model.Request{MaxTokens: 100})
			require.Error(t, err)
			l.mu.Lock()
			balance := l.balance
			l.mu.Unlock()
			require.InDelta(t, tc.want, balance, 0.1)
		})
	}
}

// TestUsageReconciledRateLimiterEarlyCloseAfterInitialInputChunkDoesNotRefund
// verifies that closing a stream after an input-only usage chunk keeps the
// initial reservation rather than refunding unmeasured output tokens.
func TestUsageReconciledRateLimiterEarlyCloseAfterInitialInputChunkDoesNotRefund(t *testing.T) {
	l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 400, 400)
	require.NoError(t, err)
	provider, err := l.WrapProvider(&fakeCountingClient{
		fakeClient: fakeClient{stream: &closeTrackingStreamer{
			chunk: model.UsageChunk{Usage: model.TokenUsage{InputTokens: 15}},
		}},
		count: model.TokenCount{InputTokens: 100, Exact: false},
	})
	require.NoError(t, err)
	stream, err := provider.Stream(t.Context(), &model.Request{MaxTokens: 100})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	l.mu.Lock()
	balance := l.balance
	l.mu.Unlock()
	require.InDelta(t, 200, balance, 0.1)
}
