// Explicitly targeted cancellation jobs retain their accepted worker build.
// Retries that omit a target must prove the same accepted route, reason and build.
package temporal

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	deploymentpb "go.temporal.io/api/deployment/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"goa.design/goa-ai/runtime/agent/engine"
)

// cancellationRetryService returns the accepted start route and pin along with
// the memo, matching the facts Temporal supplies when a duplicate is described.
type cancellationRetryService struct {
	testWorkflowService
	changeDescription func(*workflowpb.WorkflowExecutionInfo)
}

func TestCancellationWorkflowKeepsExplicitWorkerTarget(t *testing.T) {
	service := &cancellationRetryService{}
	implementation := &Engine{client: newWorkflowServiceClient(t, service)}
	t.Cleanup(implementation.client.Close)
	request := engine.CancellationRequest{RunID: "selected-run", Reason: "user_requested"}
	target := &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "accepted.workflow.cancel", "accepted.queue", request, target))
	accepted := service.startRequest()
	pinned := accepted.GetVersioningOverride().GetPinned()
	require.NotNil(t, pinned)
	assert.Equal(t, target.DeploymentName, pinned.GetVersion().GetDeploymentName())
	assert.Equal(t, target.BuildID, pinned.GetVersion().GetBuildId())
	assert.Equal(t, request, decodePayload[engine.CancellationRequest](t, accepted.Input.Payloads[0]))
	require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "accepted.workflow.cancel", "accepted.queue", request, target))
	err := implementation.StartCancellationWorkflow(t.Context(), "accepted.workflow.cancel", "accepted.queue", request, &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-b"})
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}

func (s *cancellationRetryService) DescribeWorkflowExecution(ctx context.Context, request *workflowservice.DescribeWorkflowExecutionRequest) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	description, err := s.testWorkflowService.DescribeWorkflowExecution(ctx, request)
	if err != nil {
		return nil, err
	}
	accepted := s.startRequest()
	info := description.WorkflowExecutionInfo
	info.Type = accepted.WorkflowType
	info.TaskQueue = accepted.TaskQueue.Name
	info.VersioningInfo = &workflowpb.WorkflowExecutionVersioningInfo{VersioningOverride: accepted.VersioningOverride}
	if s.changeDescription != nil {
		s.changeDescription(info)
	}
	return description, nil
}

func TestCancellationWorkflowWithoutTargetReusesExactAcceptedPin(t *testing.T) {
	for _, test := range []struct {
		name              string
		workflow          string
		queue             string
		reason            string
		changeDescription func(*workflowpb.WorkflowExecutionInfo)
		conflict          bool
	}{
		{name: "accepted pin", workflow: "cleanup", queue: "queue", reason: "user_requested"},
		{name: "changed workflow", workflow: "other", queue: "queue", reason: "user_requested", conflict: true},
		{name: "changed queue", workflow: "cleanup", queue: "other", reason: "user_requested", conflict: true},
		{name: "changed reason", workflow: "cleanup", queue: "queue", reason: "session_ended", conflict: true},
		{name: "changed saved pin", workflow: "cleanup", queue: "queue", reason: "user_requested", conflict: true, changeDescription: func(info *workflowpb.WorkflowExecutionInfo) {
			info.VersioningInfo = &workflowpb.WorkflowExecutionVersioningInfo{VersioningOverride: &workflowpb.VersioningOverride{Override: &workflowpb.VersioningOverride_Pinned{Pinned: &workflowpb.VersioningOverride_PinnedOverride{Version: &deploymentpb.WorkerDeploymentVersion{DeploymentName: "workers", BuildId: "build-b"}}}}}
		}},
		{name: "missing saved pin", workflow: "cleanup", queue: "queue", reason: "user_requested", conflict: true, changeDescription: func(info *workflowpb.WorkflowExecutionInfo) {
			info.VersioningInfo = nil
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := &cancellationRetryService{changeDescription: test.changeDescription}
			implementation := &Engine{client: newWorkflowServiceClient(t, service)}
			t.Cleanup(implementation.client.Close)
			request := engine.CancellationRequest{RunID: "saved-run", Reason: "user_requested"}
			target := &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
			require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), "cleanup", "queue", request, target))
			request.Reason = test.reason
			err := implementation.StartCancellationWorkflow(t.Context(), test.workflow, test.queue, request, nil)
			if test.conflict {
				if test.reason != "user_requested" {
					var conflict *engine.CancellationConflictError
					require.ErrorAs(t, err, &conflict)
				} else {
					require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
				}
				return
			}
			require.NoError(t, err)
			require.NoError(t, implementation.StartCancellationWorkflow(t.Context(), test.workflow, test.queue, request, nil))
			assert.Equal(t, "build-a", service.startRequest().GetVersioningOverride().GetPinned().GetVersion().GetBuildId())
		})
	}
}
