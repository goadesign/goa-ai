// Cancellation jobs accept one request and keep observing after the caller's
// context ends. Pending observations and failed attempts remain distinct.
package inmem

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

func TestCancellationWorkflowOwnsPendingWorkAfterCallerLeaves(t *testing.T) {
	implementation := New().(*eng)
	request := engine.CancellationRequest{RunID: "source", Reason: "user_requested"}
	var observations atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, implementation.RegisterCancellationWorkflow(t.Context(), "cleanup", engine.ActivityOptions{
		Queue: "queue", StartToCloseTimeout: time.Second,
		RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true, InitialInterval: time.Millisecond},
	}, func(ctx context.Context, got engine.CancellationRequest) (bool, error) {
		assert.Equal(t, request, got)
		assert.NoError(t, ctx.Err())
		switch observations.Add(1) {
		case 1:
			return false, errors.New("temporary transport failure")
		case 2:
			return false, nil
		default:
			return true, nil
		}
	}))
	require.NoError(t, implementation.StartCancellationWorkflow(ctx, "cleanup", "queue", request))
	cancel()
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request))
	id, _, err := startrecipe.CancellationRecipe("cleanup", "queue", request)
	require.NoError(t, err)
	implementation.mu.RLock()
	job := implementation.cancellationJobs[id]
	implementation.mu.RUnlock()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("cancellation job did not settle")
	}
	require.NoError(t, job.err)
	assert.EqualValues(t, 3, observations.Load())
	changed := request
	changed.Reason = "session_ended"
	var conflict *engine.CancellationConflictError
	assert.ErrorAs(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", changed), &conflict)
}

func TestCancellationWorkflowKeepsPermanentFailure(t *testing.T) {
	implementation := New().(*eng)
	request := engine.CancellationRequest{RunID: "source", Reason: "user_requested"}
	rejection := engine.MarkActivityErrorNonRetryable(errors.New("owner rejected cancellation"))
	var observations atomic.Int32
	require.NoError(t, implementation.RegisterCancellationWorkflow(t.Context(), "cleanup", engine.ActivityOptions{
		Queue: "queue", RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true, InitialInterval: time.Millisecond},
	}, func(context.Context, engine.CancellationRequest) (bool, error) {
		observations.Add(1)
		return false, rejection
	}))
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request))
	id, _, err := startrecipe.CancellationRecipe("cleanup", "queue", request)
	require.NoError(t, err)
	implementation.mu.RLock()
	job := implementation.cancellationJobs[id]
	implementation.mu.RUnlock()
	select {
	case <-job.done:
	case <-time.After(time.Second):
		t.Fatal("rejected job did not close")
	}
	require.ErrorIs(t, job.err, rejection)
	assert.EqualValues(t, 1, observations.Load())
}
