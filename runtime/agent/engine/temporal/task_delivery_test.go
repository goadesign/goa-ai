// These tests run the real tool-activity wrapper and workflow retry options.
// Temporary activity failures retain the accepted input under engine retries;
// permanent input rejection stops after one attempt even under that policy.
package temporal

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestToolActivityRetryPolicyKeepsRejectedInputFinal(t *testing.T) {
	for _, permanent := range []bool{false, true} {
		name := "temporary"
		if permanent {
			name = "permanent"
		}
		t.Run(name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			env.SetTestTimeout(5 * time.Second)
			env.SetDataConverter(NewAgentDataConverter())
			eng := newTestEngine(t)
			eng.workerFactory = func(_ client.Client, _ string, options worker.Options) worker.Worker {
				env.SetWorkerOptions(options)
				return &boundedCompletionWorker{env: env}
			}
			operation, err := tooloperation.NewTaskCancel("")
			require.NoError(t, err)
			var attempts atomic.Int32
			require.NoError(t, eng.RegisterExecuteToolActivity(t.Context(), "task.cancel", engine.ActivityOptions{RetryPolicy: engine.RetryPolicy{MaxAttempts: 1}}, func(_ context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
				assert.EqualValues(t, 3, input.ExecutionSequence)
				id, selected := input.ExecutionContinuation.AsTaskCancel()
				assert.True(t, selected)
				assert.Empty(t, id)
				attempt := attempts.Add(1)
				if permanent {
					return nil, engine.MarkActivityErrorNonRetryable(errors.New("authorization rejected"))
				}
				if attempt <= 2 {
					return nil, errors.New("temporary connection loss")
				}
				pending, err := tooloperation.NewPendingTaskWait(id, nil)
				if err != nil {
					return nil, err
				}
				return &api.ToolOutput{PendingExecution: pending}, nil
			}))
			require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{Name: "task.delivery", Handler: func(wf engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
				future, err := wf.ExecuteToolActivityAsync(engine.ToolActivityCall{Name: "task.cancel", Input: &api.ToolInput{ExecutionSequence: 3, ExecutionContinuation: operation}, Options: engine.ActivityOptions{StartToCloseTimeout: time.Second, RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true, InitialInterval: time.Millisecond}}})
				if err != nil {
					return nil, err
				}
				out, err := future.Get(wf.Context())
				if err != nil {
					return nil, err
				}
				id, _, waiting := out.PendingExecution.AsTaskWait()
				assert.True(t, waiting)
				assert.Empty(t, id)
				return &api.RunOutput{RunID: "task-run"}, nil
			}}))
			env.ExecuteWorkflow("task.delivery", &api.RunInput{RunID: "task-run"})
			if permanent {
				err := env.GetWorkflowError()
				require.ErrorContains(t, err, "authorization rejected")
				assert.EqualValues(t, 1, attempts.Load())
			} else {
				require.NoError(t, env.GetWorkflowError())
				assert.EqualValues(t, 3, attempts.Load())
			}
		})
	}
}
