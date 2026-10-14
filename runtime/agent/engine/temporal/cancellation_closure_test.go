// Cancellation rejected during finalization waits for the exact Temporal
// execution's close event. Workflow results do not replace the cancellation
// response, and failed history reads never prove closure.
package temporal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	cancellationClosureClient struct {
		*cancellationClient
		history           client.HistoryEventIterator
		workflowID, runID string
		longPoll          bool
		filter            enumspb.HistoryEventFilterType
	}
	cancellationCloseHistory struct {
		available        bool
		event            *historypb.HistoryEvent
		err              error
		entered, release chan struct{}
		ctx              context.Context
	}
)

func (c *cancellationClosureClient) GetWorkflowHistory(ctx context.Context, workflowID, runID string, longPoll bool, filter enumspb.HistoryEventFilterType) client.HistoryEventIterator {
	c.workflowID, c.runID, c.longPoll, c.filter = workflowID, runID, longPoll, filter
	if history, ok := c.history.(*cancellationCloseHistory); ok {
		history.ctx = ctx
	}
	return c.history
}

func (h *cancellationCloseHistory) HasNext() bool {
	if h.entered != nil {
		close(h.entered)
		select {
		case <-h.release:
		case <-h.ctx.Done():
			h.err = h.ctx.Err()
		}
	}
	return h.available
}

func (h *cancellationCloseHistory) Next() (*historypb.HistoryEvent, error) {
	return h.event, h.err
}

func TestCancellationFinalizationWaitsForExactExecutionClosure(t *testing.T) {
	for _, cancelCaller := range []bool{false, true} {
		name := "closed execution"
		if cancelCaller {
			name = "caller stops waiting"
		}
		t.Run(name, func(t *testing.T) {
			history := &cancellationCloseHistory{
				available: true, event: &historypb.HistoryEvent{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED},
				entered: make(chan struct{}), release: make(chan struct{}),
			}
			fake := &cancellationClosureClient{cancellationClient: &cancellationClient{
				updateHandle: &cancellationUpdateHandle{err: temporal.NewNonRetryableApplicationError("closing", cancellationCompletedErrorType, nil)},
			}, history: history}
			implementation := &Engine{client: fake}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- implementation.RequestCancellation(ctx, "run", engine.CancellationRequest{RunID: "selected-child", Reason: "user_requested"})
			}()
			select {
			case <-history.entered:
			case <-ctx.Done():
				t.Fatal("close-event read did not start")
			}
			select {
			case err := <-done:
				t.Fatalf("cancellation returned before closure: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if cancelCaller {
				cancel()
				require.ErrorIs(t, <-done, context.Canceled)
			} else {
				close(history.release)
				require.ErrorIs(t, <-done, engine.ErrWorkflowCompleted)
			}
			assert.Equal(t, "run", fake.workflowID)
			assert.Equal(t, "temporal-run", fake.runID)
			assert.True(t, fake.longPoll)
			assert.Equal(t, enumspb.HISTORY_EVENT_FILTER_TYPE_CLOSE_EVENT, fake.filter)
		})
	}
}

func TestCancellationFinalizationRequiresCloseEvidence(t *testing.T) {
	readErr := errors.New("history unavailable")
	for _, tc := range []struct {
		name    string
		history *cancellationCloseHistory
		want    error
		message string
	}{
		{name: "completed", history: &cancellationCloseHistory{available: true, event: &historypb.HistoryEvent{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED}}, want: engine.ErrWorkflowCompleted},
		{name: "failed", history: &cancellationCloseHistory{available: true, event: &historypb.HistoryEvent{EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED}}, want: engine.ErrWorkflowCompleted},
		{name: "read failure", history: &cancellationCloseHistory{available: true, err: readErr}, want: readErr},
		{name: "empty history", history: &cancellationCloseHistory{}, message: "has no close event"},
		{name: "missing event", history: &cancellationCloseHistory{available: true}, message: "returned no close event"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &cancellationClosureClient{cancellationClient: &cancellationClient{
				updateHandle: &cancellationUpdateHandle{err: temporal.NewNonRetryableApplicationError("closing", cancellationCompletedErrorType, nil)},
			}, history: tc.history}
			implementation := &Engine{client: fake}
			err := implementation.RequestCancellation(t.Context(), "run", engine.CancellationRequest{RunID: "run", Reason: "user_requested"})
			if tc.want != nil {
				assert.ErrorIs(t, err, tc.want)
			} else {
				assert.ErrorContains(t, err, tc.message)
			}
		})
	}
}
