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
	require.NoError(t, implementation.StartCancellationWorkflow(ctx, "cleanup", "queue", request, nil))
	cancel()
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, nil))
	id, _, err := startrecipe.CancellationRecipe("cleanup", "queue", request, nil)
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
	assert.ErrorAs(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", changed, nil), &conflict)
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
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, nil))
	id, _, err := startrecipe.CancellationRecipe("cleanup", "queue", request, nil)
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

func TestCancellationWorkflowTargetBelongsToExactJobIdentity(t *testing.T) {
	implementation := New().(*eng)
	require.NoError(t, implementation.RegisterCancellationWorkflow(t.Context(), "cleanup", engine.ActivityOptions{
		Queue: "queue", RetryPolicy: engine.RetryPolicy{InitialInterval: time.Millisecond},
	}, func(context.Context, engine.CancellationRequest) (bool, error) {
		return true, nil
	}))
	request := engine.CancellationRequest{RunID: "root", Reason: "user_requested"}
	target := &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, target))
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, target))
	err := implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-b"})
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}

func TestCancellationWorkflowWithoutTargetKeepsAcceptedRecipe(t *testing.T) {
	implementation := New().(*eng)
	for _, route := range []struct{ name, queue string }{
		{name: "cleanup", queue: "queue"},
		{name: "other", queue: "queue"},
		{name: "other-queue", queue: "other"},
	} {
		require.NoError(t, implementation.RegisterCancellationWorkflow(t.Context(), route.name, engine.ActivityOptions{
			Queue: route.queue, RetryPolicy: engine.RetryPolicy{InitialInterval: time.Millisecond},
		}, func(context.Context, engine.CancellationRequest) (bool, error) {
			return true, nil
		}))
	}
	request := engine.CancellationRequest{RunID: "root", Reason: "user_requested"}
	target := &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, target))
	target.BuildID = "caller-mutated"
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, nil))
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, nil))
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}))
	assert.ErrorIs(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, target), engine.ErrWorkflowStartConflict)
	assert.ErrorIs(t, implementation.StartCancellationWorkflow(t.Context(), "other", "queue", request, nil), engine.ErrWorkflowStartConflict)
	assert.ErrorIs(t, implementation.StartCancellationWorkflow(t.Context(), "other-queue", "other", request, nil), engine.ErrWorkflowStartConflict)
	request.Reason = "session_ended"
	var conflict *engine.CancellationConflictError
	assert.ErrorAs(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, nil), &conflict)
}
