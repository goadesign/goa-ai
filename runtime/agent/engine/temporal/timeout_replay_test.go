package temporal

// Synthetic saved histories cover both sides of child failure delivery. The
// production workflow still emits a failure after an activity times out; a
// parent receives old generic failures and native SDK failures without running
// the child again. Converter tests separately pin exact saved failure bytes.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	failurepb "go.temporal.io/api/failure/v1"
	historypb "go.temporal.io/api/history/v1"
	sdktemporal "go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	"google.golang.org/protobuf/proto"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
)

func TestProductionWorkflowReplaysCompletedNativeTimeout(t *testing.T) {
	for _, oldGeneric := range []bool{false, true} {
		name := "native completion"
		if oldGeneric {
			name = "old generic completion"
		}
		t.Run(name, func(t *testing.T) {
			plannerStub, handler := productionReplayWorkflow(t)
			history := syntheticProductionReplayHistory(t, &api.PlanActivityOutput{}, false)
			history.Events = history.Events[:28]
			history.Events[24] = &historypb.HistoryEvent{
				EventId: 25, EventType: enumspb.EVENT_TYPE_ACTIVITY_TASK_TIMED_OUT,
				Attributes: &historypb.HistoryEvent_ActivityTaskTimedOutEventAttributes{
					ActivityTaskTimedOutEventAttributes: &historypb.ActivityTaskTimedOutEventAttributes{
						ScheduledEventId: 23, StartedEventId: 24,
						RetryState: enumspb.RETRY_STATE_MAXIMUM_ATTEMPTS_REACHED,
						Failure:    nativeTimeoutFailure(),
					},
				},
			}
			terminal, err := NewAgentDataConverter().ToPayloads(&api.StorageActivityResult{Terminal: &api.RecordWriteResult{}})
			require.NoError(t, err)
			savedFailure := nativeTimeoutFailure()
			if oldGeneric {
				savedFailure = savedGenericTimeoutFailure()
			}
			history.Events = append(history.Events,
				activityTaskScheduledEvent(29, productionReplayRecord, nil),
				activityTaskStartedEvent(30, 29),
				activityTaskCompletedEvent(31, 29, 30, terminal),
				workflowTaskScheduledEvent(32), workflowTaskStartedEvent(33), workflowTaskCompletedEvent(34, 32, 33),
				&historypb.HistoryEvent{
					EventId: 35, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_FAILED,
					Attributes: &historypb.HistoryEvent_WorkflowExecutionFailedEventAttributes{
						WorkflowExecutionFailedEventAttributes: &historypb.WorkflowExecutionFailedEventAttributes{
							WorkflowTaskCompletedEventId: 34, Failure: savedFailure,
						},
					},
				},
			)
			var returned error
			capture := func(ctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
				out, failure := handler(ctx, input)
				returned = failure
				return out, failure
			}
			retainedFailure := proto.Clone(savedFailure)
			history = deserializeReplayHistory(t, history)
			replayProductionWorkflow(t, capture, history)
			assert.Zero(t, plannerStub.calls.Load())
			assert.True(t, temporalerrors.IsNativeTimeout(returned))
			// Replaying an already-closed workflow checks command compatibility;
			// it does not replace the failure stored when that workflow closed.
			assert.True(t, proto.Equal(retainedFailure, history.Events[34].GetWorkflowExecutionFailedEventAttributes().Failure))
		})
	}
}

func TestTemporalChildHandleReplaysSavedTimeoutFailures(t *testing.T) {
	for _, oldGeneric := range []bool{false, true} {
		name := "native"
		if oldGeneric {
			name = "saved generic"
		}
		t.Run(name, func(t *testing.T) {
			failure := nativeTimeoutFailure()
			if oldGeneric {
				failure = savedGenericTimeoutFailure()
			}
			history := retainedChildCompletionHistory()
			succeeded := history.Events[6].GetChildWorkflowExecutionCompletedEventAttributes()
			history.Events[6] = &historypb.HistoryEvent{
				EventId: 7, EventType: enumspb.EVENT_TYPE_CHILD_WORKFLOW_EXECUTION_FAILED,
				Attributes: &historypb.HistoryEvent_ChildWorkflowExecutionFailedEventAttributes{
					ChildWorkflowExecutionFailedEventAttributes: &historypb.ChildWorkflowExecutionFailedEventAttributes{
						Namespace: succeeded.Namespace, InitiatedEventId: 5, StartedEventId: 6,
						WorkflowExecution: succeeded.WorkflowExecution, WorkflowType: succeeded.WorkflowType, Failure: failure,
					},
				},
			}
			var received error
			parent := func(ctx workflow.Context) (int, error) {
				ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{WorkflowID: retainedChildWorkflowID, TaskQueue: "child.queue"})
				handle := &temporalChildHandle{future: workflow.ExecuteChildWorkflow(ctx, "retained.child"), ctx: ctx}
				_, received = handle.Get(context.Background())
				return 0, nil
			}
			replayer, err := worker.NewWorkflowReplayerWithOptions(worker.WorkflowReplayerOptions{DataConverter: NewAgentDataConverter()})
			require.NoError(t, err)
			replayer.RegisterWorkflowWithOptions(parent, workflow.RegisterOptions{Name: "retained.parent"})
			require.NoError(t, replayer.ReplayWorkflowHistory(nil, history))
			require.Error(t, received)
			assert.Equal(t, !oldGeneric, temporalerrors.IsNativeTimeout(received))
			assert.Contains(t, received.Error(), "activity StartToClose timeout")
			if oldGeneric {
				var app *sdktemporal.ApplicationError
				require.ErrorAs(t, received, &app)
				assert.Equal(t, "goa_ai.generic_error.v3", app.Type())
			}
		})
	}
}

func nativeTimeoutFailure() *failurepb.Failure {
	return &failurepb.Failure{
		Message: "activity StartToClose timeout",
		FailureInfo: &failurepb.Failure_TimeoutFailureInfo{TimeoutFailureInfo: &failurepb.TimeoutFailureInfo{
			TimeoutType: enumspb.TIMEOUT_TYPE_START_TO_CLOSE,
		}},
	}
}

func savedGenericTimeoutFailure() *failurepb.Failure {
	return &failurepb.Failure{
		Message: "activity StartToClose timeout",
		FailureInfo: &failurepb.Failure_ApplicationFailureInfo{ApplicationFailureInfo: &failurepb.ApplicationFailureInfo{
			Type: "goa_ai.generic_error.v3",
			Details: &commonpb.Payloads{Payloads: []*commonpb.Payload{{
				Metadata: map[string][]byte{"encoding": []byte("json/plain")},
				Data:     []byte(`{"OriginalType":"","Message":"activity StartToClose timeout","Retryable":true}`),
			}}},
		}},
	}
}
