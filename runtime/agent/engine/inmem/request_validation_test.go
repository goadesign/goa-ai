package inmem

// In-memory execution must stop explicit request rejections just like Temporal.
// These tests exercise its actual retry loops without changing how an outer
// provider error or an unclassified error owns its existing retry behavior.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestRequestValidationInMemoryRetryOwnership(t *testing.T) {
	native := model.NewRequestValidationError(errors.New("request rejected locally"))
	codec := temporal.GetDefaultFailureConverter()
	saved := codec.FailureToError(codec.ErrorToFailure(temporalerrors.Wrap(native)))
	custom := temporal.NewApplicationErrorWithOptions("application owns retries", "custom",
		temporal.ApplicationErrorOptions{Cause: native})
	for _, test := range []struct {
		name     string
		err      error
		attempts int
	}{
		{"native", native, 1},
		{"wrapped", fmt.Errorf("prepare: %w", native), 1},
		{"saved", saved, 1},
		{"direct custom owner", custom, 3},
		// The Temporal writer preserves only a direct custom error's override.
		{"wrapped custom cause", fmt.Errorf("outer context: %w", custom), 1},
		{"unknown", errors.New("unclassified execution error"), 3},
		{"outer provider", model.NewProviderError("provider", "complete", 503,
			model.ProviderErrorKindUnavailable, "unavailable", "provider owns failure", "", true, native), 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			retry := engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Millisecond}
			var activityAttempts int
			_, err := executeActivityWithRetry(t.Context(), 0, retry, func(context.Context) (int, error) {
				activityAttempts++
				return 0, test.err
			})
			require.ErrorIs(t, err, test.err)
			assert.Equal(t, test.attempts, activityAttempts)
			implementation := New()
			var workflowAttempts atomic.Int32
			require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
				Name: "request-workflow",
				Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
					workflowAttempts.Add(1)
					return nil, test.err
				},
			}))
			handle, err := implementation.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
				ID: "run", Workflow: "request-workflow", TaskQueue: "queue",
				Input: &api.RunInput{RunID: "run"}, RetryPolicy: retry,
			})
			require.NoError(t, err)
			_, err = handle.Wait(t.Context())
			require.ErrorIs(t, err, test.err)
			assert.EqualValues(t, test.attempts, workflowAttempts.Load())
		})
	}
}

func TestRequestValidationInMemoryChildStopsAfterOneAttempt(t *testing.T) {
	implementation := New()
	var childAttempts atomic.Int32
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "child",
		Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
			childAttempts.Add(1)
			return nil, model.NewRequestValidationError(errors.New("child request rejected locally"))
		},
	}))
	require.NoError(t, implementation.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "parent",
		Handler: func(ctx engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
			child, err := ctx.StartChildWorkflow(ctx.Context(), engine.ChildWorkflowRequest{
				ID: "child-run", Workflow: "child", TaskQueue: "queue",
				Input:       &api.RunInput{RunID: "child-run"},
				RetryPolicy: engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Millisecond},
			})
			if err != nil {
				return nil, err
			}
			return child.Get(ctx.Context())
		},
	}))
	handle, err := implementation.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: "parent-run", Workflow: "parent", TaskQueue: "queue", Input: &api.RunInput{RunID: "parent-run"},
	})
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	assert.True(t, temporalerrors.IsRequestValidation(err))
	assert.EqualValues(t, 1, childAttempts.Load())
}
