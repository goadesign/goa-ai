// A cancellation job is a new outer workflow. Its launch retains the original
// root's exact worker build while its input remains only the run and reason.
package temporal

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestCancellationWorkflowPinsAcceptedRootTarget(t *testing.T) {
	for _, active := range []bool{false, true} {
		t.Run(map[bool]string{false: "suspended owner", true: "active parent"}[active], func(t *testing.T) {
			service := &activeCancellationService{owner: &workflowpb.WorkflowExecutionInfo{
				Execution: &commonpb.WorkflowExecution{WorkflowId: "owner"},
				Type:      &commonpb.WorkflowType{Name: "accepted.workflow"},
				TaskQueue: "accepted.queue",
				Status:    enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING,
			}}
			implementation := &Engine{client: newWorkflowServiceClient(t, service)}
			t.Cleanup(implementation.client.Close)
			request := engine.CancellationRequest{RunID: "selected-run", Reason: "user_requested"}
			target := &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
			start := func(target *engine.WorkerDeploymentVersion) error {
				if active {
					return implementation.StartActiveCancellationWorkflow(t.Context(), "owner", request, target)
				}
				return implementation.StartCancellationWorkflow(t.Context(), "accepted.workflow.cancel", "accepted.queue", request, target)
			}
			require.NoError(t, start(target))
			accepted := service.startRequest()
			pinned := accepted.GetVersioningOverride().GetPinned()
			require.NotNil(t, pinned)
			assert.Equal(t, target.DeploymentName, pinned.GetVersion().GetDeploymentName())
			assert.Equal(t, target.BuildID, pinned.GetVersion().GetBuildId())
			assert.Equal(t, request, decodePayload[engine.CancellationRequest](t, accepted.Input.Payloads[0]))
			require.NoError(t, start(target))
			err := start(&engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-b"})
			require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
		})
	}
}
