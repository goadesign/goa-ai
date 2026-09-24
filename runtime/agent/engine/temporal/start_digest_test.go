package temporal

// The Temporal adapter reads only accepted engine metadata, preserves it in
// derived contexts, and carries typed start conflicts across both failure layers.

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
)

func TestTemporalAcceptedStartDigest(t *testing.T) {
	snapshot, err := startrecipe.SnapshotRequest(engine.WorkflowStartRequest{
		ID: "root", Workflow: "root", TaskQueue: "queue", Input: &api.RunInput{RunID: "root"},
	})
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		value any
		valid bool
	}{
		{"accepted", snapshot.Digest[:], true},
		{"absent", nil, false},
		{"short", []byte{1}, false},
		{"long", make([]byte, 33), false},
		{"zero", make([]byte, 32), false},
		{"wrong type", 123, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			if test.value != nil {
				require.NoError(t, env.SetMemoOnStart(map[string]any{startrecipe.MemoKey: test.value}))
			}
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				w := newTemporalWorkflowContext(&Engine{}, ctx)
				canceled, cancel := w.WithCancel()
				cancel()
				for _, derived := range []engine.WorkflowContext{w, canceled, canceled.Detached(), w.Detached()} {
					digest, err := derived.StartRequestDigest()
					if test.valid {
						if err != nil {
							return err
						}
						assert.Equal(t, snapshot.Digest, digest)
					} else if err == nil {
						return errors.New("missing or malformed proof was accepted")
					}
				}
				return nil
			})
			require.NoError(t, env.GetWorkflowError())
		})
	}
}

func TestTemporalStartConflictRejectsMalformedDetails(t *testing.T) {
	for _, value := range []any{123, ""} {
		encoded := temporal.GetDefaultFailureConverter().ErrorToFailure(temporal.NewNonRetryableApplicationError("conflict", startConflictErrorType, nil, value))
		err := mapStartConflictError(temporal.GetDefaultFailureConverter().FailureToError(encoded))
		require.Error(t, err)
		require.NotErrorIs(t, err, engine.ErrWorkflowStartConflict)
	}
	other := temporal.NewNonRetryableApplicationError("storage", "goa_ai_storage_contract", nil)
	assert.Same(t, other, mapStartConflictError(other))
}
