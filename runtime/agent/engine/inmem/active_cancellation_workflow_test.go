// Active cancellation uses the immutable route accepted with the owner, including
// child admission. Closed owners and missing owners do not create new jobs.
package inmem

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestStartActiveCancellationWorkflowUsesAcceptedRootAndChildRoutes(t *testing.T) {
	e := New()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	release := make(chan struct{})
	ready := make(chan struct{})
	seen := make(chan string, 2)
	for _, name := range []string{"parent", "child"} {
		require.NoError(t, e.RegisterCancellationWorkflow(ctx, name+".cancel", engine.ActivityOptions{
			Queue: name + ".queue", RetryPolicy: engine.RetryPolicy{InitialInterval: time.Millisecond},
		}, func(_ context.Context, request engine.CancellationRequest) (bool, error) {
			seen <- request.RunID
			return true, nil
		}))
	}
	require.NoError(t, e.RegisterWorkflow(ctx, engine.WorkflowDefinition{Name: "child", Handler: func(_ engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		close(ready)
		select {
		case <-release:
			return &api.RunOutput{Subject: input.Subject, RunID: input.RunID}, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}}))
	require.NoError(t, e.RegisterWorkflow(ctx, engine.WorkflowDefinition{Name: "parent", Handler: func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		child, err := w.StartChildWorkflow(w.Context(), engine.ChildWorkflowRequest{ID: "child-run", Workflow: "child", TaskQueue: "child.queue", Input: &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}, RunID: "child-run"}})
		if err != nil {
			return nil, err
		}
		if _, err = child.Get(w.Context()); err != nil {
			return nil, err
		}
		return &api.RunOutput{Subject: input.Subject, RunID: input.RunID}, nil
	}}))
	handle, err := e.StartWorkflow(ctx, engine.WorkflowStartRequest{ID: "parent-run", Workflow: "parent", TaskQueue: "parent.queue", Input: &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}, RunID: "parent-run"}})
	require.NoError(t, err)
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("child did not start")
	}
	for _, id := range []string{"parent-run", "child-run"} {
		request := engine.CancellationRequest{RunID: id, Reason: "user_requested"}
		require.NoError(t, e.StartActiveCancellationWorkflow(ctx, id, request, nil))
		require.NoError(t, e.StartActiveCancellationWorkflow(ctx, id, request, nil))
		select {
		case got := <-seen:
			assert.Equal(t, id, got)
		case <-ctx.Done():
			t.Fatal("cleanup was not accepted on owner route")
		}
		var conflict *engine.CancellationConflictError
		require.ErrorAs(t, e.StartActiveCancellationWorkflow(ctx, id, engine.CancellationRequest{RunID: id, Reason: "session_ended"}, nil), &conflict)
	}
	close(release)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
	require.ErrorIs(t, e.StartActiveCancellationWorkflow(ctx, "parent-run", engine.CancellationRequest{RunID: "another", Reason: "user_requested"}, nil), engine.ErrWorkflowCompleted)
	require.ErrorIs(t, e.StartActiveCancellationWorkflow(ctx, "missing", engine.CancellationRequest{RunID: "another", Reason: "user_requested"}, nil), engine.ErrWorkflowNotFound)
}
