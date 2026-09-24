package inmem

// Closed runs stop workflow retries. A rejected activity only stops retries
// of that call; a fresh workflow attempt can obtain a different valid input.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestClosedWorkflowStopsRetries(t *testing.T) {
	for _, unlimited := range []bool{false, true} {
		name := "finite"
		if unlimited {
			name = "unlimited"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			eng := New()
			var calls atomic.Int32
			require.NoError(t, eng.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "closed",
				Handler: func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
					calls.Add(1)
					return nil, engine.ErrWorkflowCompleted
				},
			}))
			policy := engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Millisecond}
			if unlimited {
				policy.MaxAttempts, policy.UnlimitedAttempts = 0, true
			}
			handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
				ID: "run", Workflow: "closed", TaskQueue: "test",
				Input: &api.RunInput{RunID: "run"}, RetryPolicy: policy,
			})
			require.NoError(t, err)
			result, err := handle.Wait(ctx)
			require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
			assert.Nil(t, result)
			assert.EqualValues(t, 1, calls.Load())
		})
	}
}

func TestActivityRejectionAllowsFreshWorkflowAttempt(t *testing.T) {
	const rejectedSelection = "invalid"
	for _, test := range []struct {
		name            string
		localValidation bool
		unlimited       bool
	}{
		{"activity rejection/finite", false, false},
		{"activity rejection/unlimited", false, true},
		{"decoded result rejection/finite", true, false},
		{"decoded result rejection/unlimited", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second*5)
			defer cancel()
			eng := New()
			var reads, writes atomic.Int32
			failure := engine.MarkActivityErrorNonRetryable(errors.New("invalid selected record"))
			require.NoError(t, eng.RegisterPlannerActivity(ctx, "select", engine.ActivityOptions{},
				func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
					selected := "valid"
					if reads.Add(1) == 1 {
						selected = rejectedSelection
					}
					return &api.PlanActivityOutput{PublicationBatchID: selected}, nil
				}))
			require.NoError(t, eng.RegisterStorageActivity(ctx, "apply", engine.ActivityOptions{},
				func(_ context.Context, command *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
					writes.Add(1)
					if command.Append.Records[0].EventKey == rejectedSelection {
						return nil, failure
					}
					return &api.StorageActivityResult{Append: &api.AppendRecordsResult{}}, nil
				}))
			require.NoError(t, eng.RegisterWorkflow(ctx, engine.WorkflowDefinition{
				Name: "retry",
				Handler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
					selected, err := wfCtx.ExecutePlannerActivity(engine.PlannerActivityCall{
						Name: "select", Input: &api.PlanActivityInput{RunID: input.RunID},
					})
					if err != nil {
						return nil, err
					}
					id := selected.PublicationBatchID
					if test.localValidation && id == rejectedSelection {
						return nil, failure
					}
					_, err = wfCtx.ExecuteStorageActivity(engine.StorageActivityCall{
						Name: "apply", Command: &api.StorageActivityCommand{Append: &api.AppendRecordsCommand{
							Records: []*api.RecordActivityInput{{EventKey: id}},
						}},
						Options: engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true}},
					})
					if err != nil {
						return nil, err
					}
					return &api.RunOutput{RunID: input.RunID}, nil
				},
			}))
			policy := engine.RetryPolicy{MaxAttempts: 2, InitialInterval: time.Millisecond}
			if test.unlimited {
				policy.MaxAttempts, policy.UnlimitedAttempts = 0, true
			}
			handle, err := eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
				ID: "run", Workflow: "retry", TaskQueue: "test", Input: &api.RunInput{RunID: "run"},
				RetryPolicy: policy,
			})
			require.NoError(t, err)
			result, err := handle.Wait(ctx)
			require.NoError(t, err)
			assert.Equal(t, "run", result.RunID)
			assert.EqualValues(t, 2, reads.Load())
			expectedWrites := int32(2)
			if test.localValidation {
				expectedWrites = 1
			}
			assert.Equal(t, expectedWrites, writes.Load())
		})
	}
}
