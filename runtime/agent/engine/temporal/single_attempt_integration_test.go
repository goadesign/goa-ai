//go:build integration

// These tests inspect the retry policy and request digest delivered by a real
// Temporal server. The SDK test environment cannot supply the accepted policy.
package temporal

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

func TestTemporalServerSingleAttemptAndAcceptedCompletion(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy engine.RetryPolicy
		reject bool
	}{
		{name: "absent"},
		{name: "single", policy: engine.RetryPolicy{MaxAttempts: 1}},
		{name: "multiple", policy: engine.RetryPolicy{MaxAttempts: 2}, reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter, queue := localRequestEngine(t)
			var calls atomic.Int32
			require.NoError(t, adapter.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
				Name: "accepted-policy",
				Handler: func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
					calls.Add(1)
					if err := w.RequireSingleAttempt(); err != nil {
						return nil, err
					}
					return &api.RunOutput{Subject: input.Subject, RunID: input.RunID}, nil
				},
			}))
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			require.NoError(t, adapter.SealRegistration(ctx))
			request := engine.WorkflowStartRequest{
				ID: fmt.Sprintf("%s-%s", queue, test.name), Workflow: "accepted-policy", TaskQueue: queue,
				Input:       &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("generic.agent")}, RunID: fmt.Sprintf("%s-%s", queue, test.name)},
				RetryPolicy: test.policy,
			}
			snapshot, err := startrecipe.SnapshotRequest(request)
			require.NoError(t, err)
			handle, err := adapter.StartWorkflow(ctx, request)
			require.NoError(t, err)
			_, err = handle.Wait(ctx)
			if test.reject {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			completion, err := adapter.QueryRunCompletion(ctx, request.ID)
			require.NoError(t, err)
			assert.Equal(t, snapshot.Digest, completion.StartRequestDigest)
			assert.False(t, completion.CompletedAt.IsZero())
			assert.EqualValues(t, 1, calls.Load())
			if test.reject {
				assert.Equal(t, engine.RunStatusFailed, completion.Status)
				require.Error(t, completion.WorkflowError)
			} else {
				assert.Equal(t, engine.RunStatusCompleted, completion.Status)
				require.NotNil(t, completion.Output)
				assert.Equal(t, request.ID, completion.Output.RunID)
			}
		})
	}
}
