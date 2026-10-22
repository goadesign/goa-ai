package temporal

// The SDK request must pin accepted roots to exactly the selected worker build.
// Duplicate starts compare that selection through the shared request digest.

import (
	"testing"

	"github.com/stretchr/testify/require"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/sdk/client"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestTemporalRootExecutionTargetPinsAcceptedBuild(t *testing.T) {
	service := &testWorkflowService{}
	eng, err := NewClient(Options{ClientOptions: &client.Options{}})
	require.NoError(t, err)
	eng.client.Close()
	eng.client = newWorkflowServiceClient(t, service)
	t.Cleanup(func() { require.NoError(t, eng.Close()) })
	request := engine.WorkflowStartRequest{
		ID: "root", Workflow: "agent.workflow", TaskQueue: "agent.queue",
		RootExecutionTarget: &engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: "build-a"},
		Input:               &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, RunID: "root"},
	}
	_, err = eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	pinned := service.startRequest().GetVersioningOverride().GetPinned()
	require.NotNil(t, pinned)
	require.Equal(t, workflowpb.VersioningOverride_PINNED_OVERRIDE_BEHAVIOR_PINNED, pinned.GetBehavior())
	require.Equal(t, request.RootExecutionTarget.DeploymentName, pinned.GetVersion().GetDeploymentName())
	require.Equal(t, request.RootExecutionTarget.BuildID, pinned.GetVersion().GetBuildId())
	_, err = eng.StartWorkflow(t.Context(), request)
	require.NoError(t, err)
	request.RootExecutionTarget = &engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: "build-b"}
	_, err = eng.StartWorkflow(t.Context(), request)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}
