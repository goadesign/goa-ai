package runtime

// A delayed closed-start reply must not admit cancellation or block the
// workflow finalizer. Channels choose the ordering without timing assumptions.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
)

type (
	closedReplyControl struct {
		entered chan struct{}
		release chan struct{}
		waiting chan struct{}
		calls   atomic.Int32
		wait    sync.Once
	}

	closedReplyContext struct {
		*routeWorkflowContext
		control *closedReplyControl
	}
)

func TestClosedStartReplyRacesCancellation(t *testing.T) {
	f := newClosedStartFixture(t, storageCommandRootStart, session.RunStatusCompleted)
	control := &closedReplyControl{entered: make(chan struct{}), release: make(chan struct{}), waiting: make(chan struct{})}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second*5)
	defer cancel()
	f.context.ctx = ctx
	wfCtx := &closedReplyContext{f.context, control}
	finished := make(chan error, 1)
	go func() {
		_, err := f.runtime.ExecuteWorkflow(wfCtx, f.input)
		finished <- err
	}()
	require.Eventually(t, func() bool {
		select {
		case <-control.entered:
			return true
		default:
			return false
		}
	}, time.Second, time.Millisecond)
	require.NotNil(t, f.context.cancellationHandler)
	err := f.context.cancellationHandler(wfCtx, engine.CancellationRequest{
		RunID: "run", Reason: run.CancellationReasonUserRequested,
	})
	require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
	close(control.release)
	require.ErrorIs(t, <-finished, engine.ErrWorkflowCompleted)
	f.assertUnchanged(t)
}

func TestClosedStartCancellationAndFinalizationOrders(t *testing.T) {
	for _, cancellationFirst := range []bool{true, false} {
		name := "finalization first"
		if cancellationFirst {
			name = "cancellation first"
		}
		t.Run(name, func(t *testing.T) {
			f := newClosedStartFixture(t, storageCommandRootStart, session.RunStatusSuspended)
			control := &closedReplyControl{entered: make(chan struct{}), release: make(chan struct{}), waiting: make(chan struct{})}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second*5)
			defer cancel()
			f.context.ctx = ctx
			wfCtx := &closedReplyContext{f.context, control}
			state := &workflowFinalizationState{}
			if !cancellationFirst {
				accepted, err := state.beginFinalization(wfCtx)
				require.NoError(t, err)
				assert.False(t, accepted)
			}
			canceled := make(chan error, 1)
			go func() {
				canceled <- f.runtime.handleWorkflowCancellation(state, wfCtx, f.input, f.command, engine.CancellationRequest{
					RunID: "run", Reason: run.CancellationReasonUserRequested,
				})
			}()
			finalized := make(chan error, 1)
			if cancellationFirst {
				select {
				case <-control.entered:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
				go func() {
					accepted, err := state.beginFinalization(wfCtx)
					if accepted {
						err = context.Canceled
					}
					state.finishFinalization()
					finalized <- err
				}()
			}
			select {
			case <-control.waiting:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if cancellationFirst {
				close(control.release)
				require.NoError(t, <-finalized)
			} else {
				state.finishFinalization()
			}
			require.ErrorIs(t, <-canceled, engine.ErrWorkflowCompleted)
			assert.Equal(t, workflowFinalizationFinished, state.phase.Load())
			if cancellationFirst {
				f.assertUnchanged(t)
			} else {
				assert.Zero(t, control.calls.Load())
			}
		})
	}
}

func (c *closedReplyContext) Context() context.Context {
	return engine.WithWorkflowContext(c.ctx, c)
}

func (c *closedReplyContext) Detached() engine.WorkflowContext {
	return &closedReplyContext{c.withContext(context.WithoutCancel(c.ctx)), c.control}
}

func (c *closedReplyContext) WithCancel() (engine.WorkflowContext, func()) {
	ctx, cancel := context.WithCancel(c.ctx)
	return &closedReplyContext{c.withContext(ctx), c.control}, cancel
}

func (c *closedReplyContext) ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	result, err := c.routeWorkflowContext.ExecuteStorageActivity(call)
	if err != nil {
		return nil, err
	}
	if c.control.calls.Add(1) == 1 {
		close(c.control.entered)
		<-c.control.release
	}
	return result, nil
}

func (c *closedReplyContext) Await(condition func() bool) error {
	if !condition() {
		c.control.wait.Do(func() { close(c.control.waiting) })
	}
	return c.routeWorkflowContext.Await(condition)
}
