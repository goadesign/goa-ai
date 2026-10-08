package runtime

// These tests connect runtime recovery actors through real issued children.
// They check that forwarding preserves root spending while each parent measures
// its own pause, including time during which a healthy sibling still runs.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
)

func TestProviderRecoveryRuntimeDeepChildKeepsHealthySiblingActive(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	implementation := engineinmem.New()
	leafStarted := make(chan struct{})
	healthyFinished := make(chan struct{})
	var root, mid, leaf *providerRecoveryActor
	var midBeforeRecovery time.Duration
	childRequest := func(id string) engine.ChildWorkflowRequest {
		return engine.ChildWorkflowRequest{
			ID: id, Workflow: id, TaskQueue: "test", Input: &RunInput{RunID: id},
		}
	}
	handlers := []engine.WorkflowDefinition{
		{
			Name: "healthy",
			Handler: func(w engine.WorkflowContext, _ *RunInput) (*RunOutput, error) {
				timer, err := w.NewTimer(w.Context(), 5*time.Millisecond)
				if err != nil {
					return nil, err
				}
				_, err = timer.Get(w.Context())
				return &RunOutput{RunID: "healthy"}, err
			},
		},
		{
			Name: "leaf",
			Handler: func(native engine.WorkflowContext, _ *RunInput) (out *RunOutput, resultErr error) {
				w, err := installProviderRecovery(native, nil)
				if err != nil {
					return nil, err
				}
				leaf = w.actor
				defer func() {
					if err := leaf.close(w, resultErr); err != nil {
						resultErr = err
					}
				}()
				started := w.Now()
				close(leafStarted)
				select {
				case <-healthyFinished:
				case <-w.Context().Done():
					return nil, w.Context().Err()
				}
				recovery, err := leaf.openLocal(w, recoveryOwnerFailure("failed-plan", started, w.Now()))
				if err != nil {
					return nil, err
				}
				if err := recovery.wait(w, 10*time.Millisecond); err != nil {
					return nil, err
				}
				if _, err := recovery.attempt(w, time.Time{}); err != nil {
					return nil, err
				}
				message, err := recovery.state.succeed()
				if err != nil {
					return nil, err
				}
				if err := recovery.send(w, message); err != nil {
					return nil, err
				}
				if err := recovery.await(w, providerRecoveryRequestComplete); err != nil {
					return nil, err
				}
				return &RunOutput{RunID: "leaf"}, nil
			},
		},
		{
			Name: "mid",
			Handler: func(native engine.WorkflowContext, _ *RunInput) (out *RunOutput, resultErr error) {
				w, err := installProviderRecovery(native, &providerRecoveryBudget{Remaining: time.Second})
				if err != nil {
					return nil, err
				}
				mid = w.actor
				defer func() {
					if err := mid.close(w, resultErr); err != nil {
						resultErr = err
					}
				}()
				child, err := w.StartChildWorkflow(w.Context(), childRequest("leaf"))
				if err != nil {
					return nil, err
				}
				select {
				case <-leafStarted:
				case <-w.Context().Done():
					return nil, w.Context().Err()
				}
				healthy, err := w.StartChildWorkflow(w.Context(), childRequest("healthy"))
				if err != nil {
					return nil, err
				}
				if _, err := healthy.Get(w.Context()); err != nil {
					return nil, err
				}
				midBeforeRecovery = mid.elapsed
				close(healthyFinished)
				_, err = child.Get(w.Context())
				return &RunOutput{RunID: "mid"}, err
			},
		},
		{
			Name: "root",
			Handler: func(native engine.WorkflowContext, _ *RunInput) (out *RunOutput, resultErr error) {
				w, err := installProviderRecovery(native, &providerRecoveryBudget{Remaining: time.Minute})
				if err != nil {
					return nil, err
				}
				root = w.actor
				defer func() {
					if err := root.close(w, resultErr); err != nil {
						resultErr = err
					}
				}()
				child, err := w.StartChildWorkflow(w.Context(), childRequest("mid"))
				if err != nil {
					return nil, err
				}
				_, err = child.Get(w.Context())
				return &RunOutput{RunID: "root"}, err
			},
		},
	}
	for _, definition := range handlers {
		require.NoError(t, implementation.RegisterWorkflow(ctx, definition))
	}
	handle, err := implementation.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID: "root", Workflow: "root", TaskQueue: "test", Input: &RunInput{RunID: "root"},
	})
	require.NoError(t, err)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
	require.NotNil(t, root)
	require.NotNil(t, mid)
	require.NotNil(t, leaf)
	assert.Zero(t, midBeforeRecovery)
	assert.Positive(t, root.elapsed, "the root receives its immediate child's measured pause")
	assert.LessOrEqual(t, root.elapsed, mid.elapsed)
	assert.Less(t, mid.elapsed, leaf.elapsed, "the healthy sibling consumes active parent time")
	assert.Equal(t, time.Second, mid.local.Remaining, "forwarding does not debit an explicit local policy")
	assert.Equal(t, leaf.clock.own.duration(), root.local.allowance.confirmed.duration())
	assert.Equal(t, time.Minute-leaf.clock.own.duration(), root.local.Remaining)
	assert.Empty(t, root.local.allowance.holds)
	assert.True(t, root.owner.canCheckpoint())
	assert.Len(t, root.owner.requests, 1)
}
