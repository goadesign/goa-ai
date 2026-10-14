// Active cancellation observes execution identity, status and route together.
// The original workflow may close after that observation without losing cleanup.
package temporal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"goa.design/goa-ai/runtime/agent/engine"
)

type activeCancellationService struct {
	testWorkflowService
	owner *workflowpb.WorkflowExecutionInfo
}

func (s *activeCancellationService) DescribeWorkflowExecution(ctx context.Context, request *workflowservice.DescribeWorkflowExecutionRequest) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	if request.Execution.WorkflowId == "owner" {
		if s.owner == nil {
			return nil, (&serviceerror.NotFound{Message: "owner unavailable"}).Status().Err()
		}
		return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: s.owner}, nil
	}
	return s.testWorkflowService.DescribeWorkflowExecution(ctx, request)
}

func TestStartActiveCancellationWorkflowObservesAcceptedTemporalRoute(t *testing.T) {
	for _, state := range []enumspb.WorkflowExecutionStatus{
		enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
		enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED,
		enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED,
		enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
		enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED,
		enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
		enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW,
		enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT,
		enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED,
	} {
		t.Run(state.String(), func(t *testing.T) {
			service := &activeCancellationService{}
			if state != enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED {
				service.owner = &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: "owner", RunId: "exact-execution"}, Type: &commonpb.WorkflowType{Name: "accepted.workflow"}, TaskQueue: "accepted.queue", Status: state}
			}
			implementation := &Engine{client: newWorkflowServiceClient(t, service)}
			t.Cleanup(implementation.client.Close)
			request := engine.CancellationRequest{RunID: "selected-child", Reason: "user_requested"}
			err := implementation.StartActiveCancellationWorkflow(t.Context(), "owner", request)
			switch state {
			case enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, enumspb.WORKFLOW_EXECUTION_STATUS_PAUSED:
				require.NoError(t, err)
				accepted := service.startRequest()
				assert.Equal(t, "accepted.workflow.cancel", accepted.WorkflowType.Name)
				assert.Equal(t, "accepted.queue", accepted.TaskQueue.Name)
				assert.Equal(t, request, decodePayload[engine.CancellationRequest](t, accepted.Input.Payloads[0]))
				require.NoError(t, implementation.StartActiveCancellationWorkflow(t.Context(), "owner", request))
			case enumspb.WORKFLOW_EXECUTION_STATUS_COMPLETED, enumspb.WORKFLOW_EXECUTION_STATUS_FAILED,
				enumspb.WORKFLOW_EXECUTION_STATUS_CANCELED, enumspb.WORKFLOW_EXECUTION_STATUS_TERMINATED,
				enumspb.WORKFLOW_EXECUTION_STATUS_CONTINUED_AS_NEW, enumspb.WORKFLOW_EXECUTION_STATUS_TIMED_OUT:
				require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
				assert.Nil(t, service.startRequest())
			case enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED:
				require.ErrorIs(t, err, engine.ErrWorkflowNotFound)
				assert.Nil(t, service.startRequest())
			}
		})
	}
}
