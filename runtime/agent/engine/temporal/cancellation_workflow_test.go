// Cancellation workflow checks use Temporal's recorded activities and timers.
// A false observation means work remains; a permanent error cannot become success.
package temporal

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

func TestCancellationWorkflowRecordsPendingObservations(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "settled", true: "permanent rejection"}[fail], func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			environment := suite.NewTestWorkflowEnvironment()
			request := engine.CancellationRequest{RunID: "source", Reason: "user_requested"}
			options := engine.ActivityOptions{
				Queue: "queue", StartToCloseTimeout: time.Minute,
				RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true, InitialInterval: time.Second},
			}
			implementation := &Engine{activityOptions: map[string]engine.ActivityOptions{"cleanup.observe": options}}
			observations := 0
			environment.RegisterActivityWithOptions(func(_ context.Context, got engine.CancellationRequest) (bool, error) {
				assert.Equal(t, request, got)
				observations++
				if fail {
					return false, temporalerrors.WrapActivity(engine.MarkActivityErrorNonRetryable(errors.New("owner rejected cancellation")))
				}
				return observations == 2, nil
			}, activity.RegisterOptions{Name: "cleanup.observe"})
			environment.ExecuteWorkflow(implementation.cancellationWorkflowHandler("cleanup", "cleanup.observe", options), request)
			if fail {
				require.ErrorContains(t, environment.GetWorkflowError(), "owner rejected cancellation")
				assert.Equal(t, 1, observations)
			} else {
				require.NoError(t, environment.GetWorkflowError())
				assert.Equal(t, 2, observations)
			}
		})
	}
}

func TestStartCancellationWorkflowRetainsAcceptedRequest(t *testing.T) {
	service := &testWorkflowService{}
	implementation, err := NewClient(Options{ClientOptions: &client.Options{}})
	require.NoError(t, err)
	implementation.client.Close()
	implementation.client = newWorkflowServiceClient(t, service)
	t.Cleanup(func() { require.NoError(t, implementation.Close()) })
	request := engine.CancellationRequest{RunID: "saved-run", Reason: "user_requested"}
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup.workflow", "owner.queue", request))
	accepted := service.startRequest()
	assert.Equal(t, "owner.queue", accepted.TaskQueue.Name)
	assert.Equal(t, "cleanup.workflow", accepted.WorkflowType.Name)
	assert.Zero(t, accepted.WorkflowRunTimeout.AsDuration())
	assert.Zero(t, accepted.WorkflowExecutionTimeout.AsDuration())
	assert.Equal(t, request, decodePayload[engine.CancellationRequest](t, accepted.Input.Payloads[0]))
	assert.Equal(t, request.Reason, decodePayload[string](t, accepted.Memo.Fields[cancellationReasonMemoKey]))
	assert.Len(t, decodePayload[[]byte](t, accepted.Memo.Fields[workflowStartRecipeMemoKey]), 32)
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup.workflow", "owner.queue", request))
	changed := request
	changed.Reason = "session_ended"
	err = implementation.StartCancellationWorkflow(t.Context(), "cleanup.workflow", "owner.queue", changed)
	var conflict *engine.CancellationConflictError
	require.ErrorAs(t, err, &conflict)
	err = implementation.StartCancellationWorkflow(t.Context(), "cleanup.workflow", "different.queue", request)
	assert.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}

// A server rollover hint keeps unfinished cleanup owned by the same workflow
// ID and request. The next execution retains the observation delay.
func TestCancellationWorkflowContinuesWithFreshHistory(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	environment := suite.NewTestWorkflowEnvironment()
	environment.SetContinueAsNewSuggested(true)
	request := engine.CancellationRequest{RunID: "source", Reason: "user_requested"}
	options := engine.ActivityOptions{
		Queue: "owner.queue", StartToCloseTimeout: time.Minute,
		RetryPolicy: engine.RetryPolicy{UnlimitedAttempts: true, InitialInterval: time.Second},
	}
	implementation := &Engine{activityOptions: map[string]engine.ActivityOptions{"cleanup.observe": options}}
	environment.RegisterActivityWithOptions(func(context.Context, engine.CancellationRequest) (bool, error) {
		return false, nil
	}, activity.RegisterOptions{Name: "cleanup.observe"})
	environment.ExecuteWorkflow(implementation.cancellationWorkflowHandler("cleanup", "cleanup.observe", options), request)
	var next *workflow.ContinueAsNewError
	require.ErrorAs(t, environment.GetWorkflowError(), &next)
	assert.Equal(t, "cleanup", next.WorkflowType.Name)
	assert.Equal(t, time.Second, next.BackoffStartInterval)
	assert.Equal(t, request, decodePayload[engine.CancellationRequest](t, next.Input.Payloads[0]))
}
