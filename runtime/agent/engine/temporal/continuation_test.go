package temporal

// A saved continuation decision selects a workflow branch without re-reading
// mutable storage during replay. The synthetic history contains the actual
// boolean activity payload and an explicit queue.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

func TestContinuationActivityRecordedDecisionReplay(t *testing.T) {
	for _, available := range []bool{false, true} {
		var observed bool
		handler := func(ctx workflow.Context) error {
			wf := &temporalWorkflowContext{engine: &Engine{}, ctx: ctx}
			answer, err := wf.ExecuteContinuationActivity(engine.ContinuationActivityCall{
				Name: "pages", Input: &api.ContinuationActivityInput{RunID: "run", HistoryEndID: "end"},
				Options: engine.ActivityOptions{Queue: productionReplayTaskQueue, StartToCloseTimeout: time.Minute},
			})
			observed = answer
			return err
		}
		input, err := NewAgentDataConverter().ToPayloads(&api.ContinuationActivityInput{RunID: "run", HistoryEndID: "end"})
		require.NoError(t, err)
		result, err := NewAgentDataConverter().ToPayloads(available)
		require.NoError(t, err)
		history := &historypb.History{Events: []*historypb.HistoryEvent{
			workflowExecutionStartedEvent("continuation", productionReplayTaskQueue, nil),
			workflowTaskScheduledEvent(2), workflowTaskStartedEvent(3), workflowTaskCompletedEvent(4, 2, 3),
			activityTaskScheduledEvent(5, "pages", input), activityTaskStartedEvent(6, 5), activityTaskCompletedEvent(7, 5, 6, result),
			workflowTaskScheduledEvent(8), workflowTaskStartedEvent(9), workflowTaskCompletedEvent(10, 8, 9),
			workflowExecutionCompletedEvent(11, 10),
		}}
		replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{DataConverter: NewAgentDataConverter()})
		require.NoError(t, err)
		replayer.RegisterWorkflowWithOptions(handler, workflow.RegisterOptions{Name: "continuation"})
		require.NoError(t, replayer.ReplayWorkflowHistory(nil, history))
		assert.Equal(t, available, observed)
	}
}

func TestContinuationActivityRejectsMissingAnswerAndPreservesFailure(t *testing.T) {
	for _, readFailure := range []bool{false, true} {
		var suite testsuite.WorkflowTestSuite
		env := suite.NewTestWorkflowEnvironment()
		env.SetDataConverter(NewAgentDataConverter())
		env.RegisterActivityWithOptions(func(context.Context, *api.ContinuationActivityInput) (*bool, error) {
			if readFailure {
				return nil, errors.New("storage unavailable")
			}
			return nil, nil
		}, activity.RegisterOptions{Name: "pages"})
		env.ExecuteWorkflow(func(ctx workflow.Context) error {
			wf := &temporalWorkflowContext{engine: &Engine{}, ctx: ctx}
			_, err := wf.ExecuteContinuationActivity(engine.ContinuationActivityCall{
				Name: "pages", Input: &api.ContinuationActivityInput{},
				Options: engine.ActivityOptions{StartToCloseTimeout: time.Minute, RetryPolicy: engine.RetryPolicy{MaxAttempts: 1}},
			})
			return err
		})
		require.Error(t, env.GetWorkflowError())
		if readFailure {
			assert.ErrorContains(t, env.GetWorkflowError(), "storage unavailable")
		} else {
			assert.ErrorContains(t, env.GetWorkflowError(), "payload count 0 does not match destination count 1")
		}
	}
}

func TestContinuationActivityRegistrationRetainsContractFailure(t *testing.T) {
	eng := newTestEngine(t)
	captured := &diagnosticWorker{activities: make(map[string]any)}
	eng.workerFactory = func(client.Client, string, worker.Options) worker.Worker { return captured }
	require.NoError(t, eng.RegisterContinuationActivity(t.Context(), "pages", engine.ActivityOptions{},
		func(context.Context, *api.ContinuationActivityInput) (bool, error) {
			return false, engine.MarkActivityErrorNonRetryable(errors.New("selected history owner mismatch"))
		}))
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestActivityEnvironment()
	env.SetDataConverter(NewAgentDataConverter())
	env.RegisterActivityWithOptions(captured.activities["pages"], activity.RegisterOptions{Name: "pages"})
	_, err := env.ExecuteActivity("pages", &api.ContinuationActivityInput{RunID: "run"})
	require.Error(t, err)
	var failure *temporal.ApplicationError
	require.ErrorAs(t, err, &failure)
	assert.True(t, failure.NonRetryable())
	assert.False(t, temporalerrors.Retryable(err))
	assert.Contains(t, failure.Message(), "selected history owner mismatch")
}
