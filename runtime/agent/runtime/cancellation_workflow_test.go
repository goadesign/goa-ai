// These checks exercise cancellation against engine and storage responses.
// Closed engine executions cannot leave a retrying job hiding unfinished work.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// closedCancellationEngine reports the actual engine outcome after it
	// rejects delivery, including a failure that storage has not recorded.
	closedCancellationEngine struct {
		engine.Engine
		completion engine.RunCompletion
		queryErr   error
		queries    []string
	}
)

func (e *closedCancellationEngine) RequestCancellation(context.Context, string, engine.CancellationRequest) error {
	return engine.ErrWorkflowCompleted
}

func (e *closedCancellationEngine) QueryRunCompletion(_ context.Context, id string) (engine.RunCompletion, error) {
	e.queries = append(e.queries, id)
	return e.completion, e.queryErr
}

func TestCancellationWorkflowRejectsClosedEngineWithRunningStorage(t *testing.T) {
	for _, outcome := range []engine.RunStatus{engine.RunStatusCompleted, engine.RunStatusFailed, engine.RunStatusCanceled, engine.RunStatusTimedOut, engine.RunStatusRunning} {
		t.Run(string(outcome), func(t *testing.T) {
			store := newTestStore()
			admitRunForTest(t, store, session.RunMeta{AgentID: "agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning})
			backend := &closedCancellationEngine{Engine: engineinmem.New(), completion: engine.RunCompletion{Status: outcome, WorkflowError: errors.New("execution stopped")}}
			runtime := New(store, WithEngine(backend))
			settled, err := runtime.cancellationActivity(t.Context(), engine.CancellationRequest{RunID: "run", Reason: run.CancellationReasonUserRequested})
			assert.False(t, settled)
			assert.Equal(t, []string{"run"}, backend.queries)
			if outcome == engine.RunStatusRunning {
				assert.NoError(t, err)
			} else {
				require.Error(t, err)
				assert.True(t, engine.IsActivityErrorNonRetryable(err))
				assert.ErrorContains(t, err, "storage still reports running")
			}
		})
	}
}

func TestCancellationWorkflowClassifiesDeliveryFailures(t *testing.T) {
	temporary := errors.New("storage temporarily unavailable")
	for _, test := range []struct {
		name      string
		err       error
		permanent bool
	}{
		{"temporary", temporary, false},
		{"contract", storage.NewContractError(errors.New("saved owner mismatch")), true},
		{"different reason", &engine.CancellationConflictError{RunID: "run", Reason: "session_ended"}, true},
		{"different preparation", &engine.WorkflowStartConflictError{ID: "run"}, true},
		{"deleted run", session.ErrRunNotFound, true},
		{"purged session", session.ErrSessionPurged, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := classifyCancellationActivityError(test.err)
			require.ErrorIs(t, err, test.err)
			assert.Equal(t, test.permanent, engine.IsActivityErrorNonRetryable(err))
		})
	}
}
