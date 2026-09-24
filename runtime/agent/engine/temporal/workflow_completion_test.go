package temporal

// Closed runs stop workflow retries. Activity rejection preserves the host's
// workflow retry policy, including a new attempt with a different input.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

type completedWorkflowRun struct {
	client.WorkflowRun
	err error
}

func TestClosedWorkflowStopsRetries(t *testing.T) {
	for _, attempts := range []int32{3, 0} {
		name := "finite"
		if attempts == 0 {
			name = "unlimited"
		}
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.SetTestTimeout(time.Second * 5)
			eng := &Engine{}
			handler := eng.temporalWorkflowHandler(func(engine.WorkflowContext, *api.RunInput) (*api.RunOutput, error) {
				calls.Add(1)
				return nil, engine.ErrWorkflowCompleted
			})
			env.RegisterWorkflowWithOptions(handler, workflow.RegisterOptions{Name: "closed"})
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
					WorkflowExecutionTimeout: time.Minute,
					RetryPolicy: &temporal.RetryPolicy{
						MaximumAttempts: attempts, InitialInterval: time.Millisecond,
					},
				})
				return workflow.ExecuteChildWorkflow(ctx, "closed", &api.RunInput{RunID: "run"}).Get(ctx, nil)
			})
			err := env.GetWorkflowError()
			require.Error(t, err)
			var appErr *temporal.ApplicationError
			require.ErrorAs(t, err, &appErr)
			assert.True(t, appErr.NonRetryable())
			assert.Equal(t, cancellationCompletedErrorType, appErr.Type())
			assert.EqualValues(t, 1, calls.Load())
			require.NotErrorIs(t, err, engine.ErrWorkflowCompleted)
			handle := &workflowHandle{run: &completedWorkflowRun{err: err}}
			output, waitErr := handle.Wait(t.Context())
			assert.Nil(t, output)
			assert.Same(t, err, waitErr)
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
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.SetTestTimeout(time.Second * 5)
			env.SetDataConverter(NewAgentDataConverter())
			eng := newTestEngine(t)
			eng.workerFactory = func(client.Client, string, worker.Options) worker.Worker {
				return &boundedCompletionWorker{env: env}
			}
			var reads, writes atomic.Int32
			failure := engine.MarkActivityErrorNonRetryable(errors.New("invalid selected record"))
			require.NoError(t, eng.RegisterPlannerActivity(t.Context(), "select", engine.ActivityOptions{},
				func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
					selected := "valid"
					if reads.Add(1) == 1 {
						selected = rejectedSelection
					}
					return &api.PlanActivityOutput{PublicationBatchID: selected}, nil
				}))
			require.NoError(t, eng.RegisterStorageActivity(t.Context(), "apply", engine.ActivityOptions{},
				func(_ context.Context, command *api.StorageActivityCommand) (*api.StorageActivityResult, error) {
					writes.Add(1)
					if command.Append.Records[0].EventKey == rejectedSelection {
						return nil, failure
					}
					return &api.StorageActivityResult{Append: &api.AppendRecordsResult{}}, nil
				}))
			require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
				Name: "retry",
				Handler: func(wfCtx engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
					selected, err := wfCtx.ExecutePlannerActivity(engine.PlannerActivityCall{
						Name: "select", Input: &api.PlanActivityInput{RunID: input.RunID},
						Options: engine.ActivityOptions{StartToCloseTimeout: time.Second},
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
						Options: engine.ActivityOptions{
							StartToCloseTimeout: time.Second, RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true},
						},
					})
					if err != nil {
						return nil, err
					}
					return &api.RunOutput{RunID: input.RunID}, nil
				},
			}))
			env.ExecuteWorkflow(func(ctx workflow.Context) (*api.RunOutput, error) {
				policy := &temporal.RetryPolicy{MaximumAttempts: 2, InitialInterval: time.Millisecond}
				if test.unlimited {
					policy.MaximumAttempts = 0
				}
				ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
					WorkflowExecutionTimeout: time.Minute,
					RetryPolicy:              policy,
				})
				var output *api.RunOutput
				err := workflow.ExecuteChildWorkflow(ctx, "retry", &api.RunInput{RunID: "run"}).Get(ctx, &output)
				return output, err
			})
			require.NoError(t, env.GetWorkflowError())
			var result *api.RunOutput
			require.NoError(t, env.GetWorkflowResult(&result))
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

func (r *completedWorkflowRun) Get(context.Context, any) error {
	return r.err
}
