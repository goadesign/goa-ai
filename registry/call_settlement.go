// Package registry durably settles claimed calls whose exact provider lease
// disappears or whose execution deadline arrives before terminal publication.
//
// Claim transitions enter a Redis deadline index atomically. Every registry
// replica runs this stateless scanner; the call-record Lua transition makes
// concurrent settlement safe and preserves one canonical terminal result.
package registry

import (
	"context"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	// callSettlementTracker scans the durable claim index and asks Redis to
	// commit outcome_unknown when ownership is lost or execution time ends.
	callSettlementTracker struct {
		store  *callAdmissionStore
		logger telemetry.Logger

		ctx       context.Context
		cancel    context.CancelFunc
		doneCh    chan struct{}
		closeOnce sync.Once
	}
)

const (
	callSettlementInterval  = 250 * time.Millisecond
	callSettlementBatchSize = 128
)

// newCallSettlementTracker starts registry-owned completion processing.
func newCallSettlementTracker(
	ctx context.Context,
	store *callAdmissionStore,
	logger telemetry.Logger,
) *callSettlementTracker {
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}
	trackerCtx, cancel := context.WithCancel(ctx) //nolint:gosec // Close stores and invokes cancel.
	tracker := &callSettlementTracker{
		store:  store,
		logger: logger,
		ctx:    trackerCtx,
		cancel: cancel,
		doneCh: make(chan struct{}),
	}
	go tracker.run()
	return tracker
}

// Close stops and joins the settlement scanner.
func (t *callSettlementTracker) Close() {
	t.closeOnce.Do(func() {
		t.cancel()
		<-t.doneCh
	})
}

// run settles due claims immediately at startup and after each bounded tick.
func (t *callSettlementTracker) run() {
	defer close(t.doneCh)
	t.settle()
	ticker := time.NewTicker(callSettlementInterval)
	defer ticker.Stop()
	for {
		select {
		case <-t.ctx.Done():
			return
		case <-ticker.C:
			t.settle()
		}
	}
}

// settle drains bounded batches so a burst cannot starve the next scheduler
// tick while still allowing normal backlogs to complete in one pass.
func (t *callSettlementTracker) settle() {
	for {
		if err := t.ctx.Err(); err != nil {
			return
		}
		settled, err := t.store.SettleLostClaims(t.ctx, callSettlementBatchSize)
		if err != nil {
			if t.ctx.Err() != nil {
				return
			}
			t.logger.Error(
				t.ctx,
				"settle lost tool call claims failed",
				"event", "settle_lost_tool_call_claims_failed",
				"component", "tool-registry-settlement",
				"err", err,
			)
			return
		}
		if settled < callSettlementBatchSize {
			return
		}
	}
}
