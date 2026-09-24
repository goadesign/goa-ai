package inmem

// Accepted root and child snapshots supply the digest even after their context
// is canceled or detached. No workflow code recomputes it from mutable input.

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

func TestAcceptedStartDigestSurvivesContextChanges(t *testing.T) {
	e := New()
	child := engine.ChildWorkflowRequest{
		ID: "child", Workflow: "child", TaskQueue: "queue",
		Input: &api.RunInput{RunID: "child", Metadata: map[string]any{"value": "original child"}},
	}
	childSnapshot, err := startrecipe.SnapshotChildRequest(child)
	require.NoError(t, err)
	request := engine.WorkflowStartRequest{
		ID: "root", Workflow: "root", TaskQueue: "queue",
		Input: &api.RunInput{RunID: "root", Metadata: map[string]any{"value": "original root"}},
	}
	rootSnapshot, err := startrecipe.SnapshotRequest(request)
	require.NoError(t, err)
	require.NotEqual(t, rootSnapshot.Digest, childSnapshot.Digest)
	for _, spec := range []struct {
		name   string
		digest [32]byte
	}{
		{"root", rootSnapshot.Digest}, {"child", childSnapshot.Digest},
	} {
		require.NoError(t, e.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
			Name: spec.name,
			Handler: func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
				input.Metadata["value"] = "mutated working input"
				digest, err := w.StartRequestDigest()
				if !assert.NoError(t, err) {
					return nil, err
				}
				assert.Equal(t, spec.digest, digest)
				canceled, cancel := w.WithCancel()
				cancel()
				for _, ctx := range []engine.WorkflowContext{canceled, canceled.Detached(), w.Detached()} {
					digest, err := ctx.StartRequestDigest()
					require.NoError(t, err)
					assert.Equal(t, spec.digest, digest)
				}
				if spec.name == "root" {
					handle, err := w.StartChildWorkflow(w.Context(), child)
					if err != nil {
						return nil, err
					}
					if _, err := handle.Get(w.Context()); err != nil {
						return nil, err
					}
				}
				return &api.RunOutput{RunID: input.RunID}, nil
			},
		}))
	}
	handle, err := e.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	_, err = new(wfCtx).StartRequestDigest()
	require.ErrorContains(t, err, "digest is missing")
}

func TestAcceptedStartConflictStopsWorkflowRetry(t *testing.T) {
	e := New()
	var calls atomic.Int32
	require.NoError(t, e.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "conflict",
		Handler: func(_ engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
			calls.Add(1)
			return nil, &engine.WorkflowStartConflictError{ID: input.RunID}
		},
	}))
	handle, err := e.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "run", Workflow: "conflict", TaskQueue: "queue", Input: &api.RunInput{RunID: "run"},
		RetryPolicy: engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Millisecond},
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = handle.Wait(ctx)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
	assert.EqualValues(t, 1, calls.Load())
}
