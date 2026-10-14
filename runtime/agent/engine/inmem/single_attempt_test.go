package inmem

// These checks use accepted engine requests to prove that high-level handlers
// can reject workflow retries without changing custom handlers' retry behavior.

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

func TestRequireSingleAttemptUsesAcceptedPolicy(t *testing.T) {
	for _, test := range []struct {
		name   string
		policy engine.RetryPolicy
		reject bool
	}{
		{name: "absent"},
		{name: "single", policy: engine.RetryPolicy{MaxAttempts: 1}},
		{name: "multiple", policy: engine.RetryPolicy{MaxAttempts: 2, InitialInterval: time.Millisecond}, reject: true},
		{name: "unlimited", policy: engine.RetryPolicy{UnlimitedAttempts: true, InitialInterval: time.Millisecond}, reject: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			e := New()
			var calls atomic.Int32
			require.NoError(t, e.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{Name: "check", Handler: func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
				calls.Add(1)
				canceled, cancel := w.WithCancel()
				cancel()
				for _, derived := range []engine.WorkflowContext{w, canceled, canceled.Detached(), w.Detached()} {
					err := derived.RequireSingleAttempt()
					if test.reject {
						assert.True(t, temporalerrors.IsRequestValidation(err))
					} else {
						assert.NoError(t, err)
					}
				}
				if err := w.RequireSingleAttempt(); err != nil {
					return nil, err
				}
				return &api.RunOutput{Subject: input.Subject, RunID: input.RunID}, nil
			}}))
			request := engine.WorkflowStartRequest{ID: "run", Workflow: "check", TaskQueue: "queue", RetryPolicy: test.policy, Input: &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("test.agent")}, RunID: "run"}}
			snapshot, err := startrecipe.SnapshotRequest(request)
			require.NoError(t, err)
			handle, err := e.StartWorkflow(t.Context(), request)
			require.NoError(t, err)
			_, err = handle.Wait(t.Context())
			if test.reject {
				assert.True(t, temporalerrors.IsRequestValidation(err))
			} else {
				require.NoError(t, err)
			}
			assert.EqualValues(t, 1, calls.Load())
			completion, err := e.QueryRunCompletion(t.Context(), "run")
			require.NoError(t, err)
			assert.Equal(t, snapshot.Digest, completion.StartRequestDigest)
		})
	}
}
