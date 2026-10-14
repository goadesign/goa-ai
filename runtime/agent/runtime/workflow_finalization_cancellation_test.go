// These tests deliver cancellation through the real engine callback queue while
// workflow finalization is paused. A late request must allow terminal work to
// finish before the caller observes that cancellation admission has closed.
package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestWorkflowFinalizationLateCancellationAllowsCompletion(t *testing.T) {
	testWorkflowFinalizationLateCancellation(t, false)
}

func TestWorkflowFinalizationSurvivesCancellationCallerLeaving(t *testing.T) {
	testWorkflowFinalizationLateCancellation(t, true)
}

func testWorkflowFinalizationLateCancellation(t *testing.T, cancelCaller bool) {
	backend := engineinmem.New()
	state := &workflowFinalizationState{}
	started, entered, returned, release := make(chan struct{}), make(chan struct{}), make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	terminalErr := errors.New("original terminal failure")
	// A failed assertion still releases the deliberately paused workflow.
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		state.finishFinalization()
	})
	require.NoError(t, backend.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "finalization.workflow", TaskQueue: "finalization.queue",
		Handler: func(wf engine.WorkflowContext, _ *RunInput) (*RunOutput, error) {
			if err := wf.SetCancellationHandler(func(_ engine.WorkflowContext, _ engine.CancellationRequest) error {
				close(entered)
				defer close(returned)
				return state.beginCancellation()
			}); err != nil {
				return nil, err
			}
			if _, err := state.beginFinalization(wf); err != nil {
				return nil, err
			}
			close(started)
			if err := wf.Await(func() bool {
				select {
				case <-release:
					return true
				default:
					return false
				}
			}); err != nil {
				return nil, err
			}
			state.finishFinalization()
			return nil, terminalErr
		},
	}))
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	handle, err := backend.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID: "finalizing", Workflow: "finalization.workflow", TaskQueue: "finalization.queue",
		Input: &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent("test.agent"))}, RunID: "finalizing"},
	})
	require.NoError(t, err)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("workflow did not enter finalization")
	}
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	requested := make(chan error, 1)
	go func() {
		requested <- backend.(engine.CancellationRequester).RequestCancellation(requestCtx, "finalizing", engine.CancellationRequest{
			RunID: "finalizing", Reason: run.CancellationReasonUserRequested,
		})
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("late cancellation did not enter the workflow callback")
	}
	select {
	case <-returned:
	case <-ctx.Done():
		t.Fatal("late callback blocked workflow finalization")
	}
	select {
	case err := <-requested:
		t.Fatalf("cancellation returned before terminal completion: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	if cancelCaller {
		cancelRequest()
		require.ErrorIs(t, <-requested, context.Canceled)
	}
	releaseOnce.Do(func() { close(release) })
	_, err = handle.Wait(ctx)
	require.ErrorIs(t, err, terminalErr)
	if !cancelCaller {
		require.ErrorIs(t, <-requested, engine.ErrWorkflowCompleted)
	}
	require.ErrorIs(t, backend.(engine.CancellationRequester).RequestCancellation(ctx, "finalizing", engine.CancellationRequest{
		RunID: "finalizing", Reason: run.CancellationReasonUserRequested,
	}), engine.ErrWorkflowCompleted)
}
