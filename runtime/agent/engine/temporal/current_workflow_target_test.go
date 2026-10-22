// Current target reads use the SDK's ordinary workflow queue request. The saved
// routing result remains usable when the response contains no active pollers.
package temporal

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	deploymentpb "go.temporal.io/api/deployment/v1"
	enumspb "go.temporal.io/api/enums/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"

	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	workflowTargetClient struct {
		client.Client
		response *workflowservice.DescribeTaskQueueResponse
		err      error
		queue    string
		kind     enumspb.TaskQueueType
	}
)

func TestCurrentWorkflowTargetReadsSavedRoutingWithoutPollers(t *testing.T) {
	fake := &workflowTargetClient{}
	adapter := &Engine{client: fake}
	for _, build := range []string{"build-a", "build-b"} {
		fake.response = &workflowservice.DescribeTaskQueueResponse{VersioningInfo: &taskqueuepb.TaskQueueVersioningInfo{CurrentDeploymentVersion: &deploymentpb.WorkerDeploymentVersion{DeploymentName: "execution", BuildId: build}}}
		target, err := adapter.CurrentWorkflowTarget(t.Context(), "agent_workflow")
		require.NoError(t, err)
		assert.Equal(t, "agent_workflow", fake.queue)
		assert.Equal(t, enumspb.TASK_QUEUE_TYPE_WORKFLOW, fake.kind)
		assert.Empty(t, fake.response.Pollers)
		assert.Equal(t, &engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: build}, target)
	}
}

func TestCurrentWorkflowTargetReportsAbsenceAndOwnerFailures(t *testing.T) {
	failure := errors.New("owner unavailable")
	for _, test := range []struct {
		name      string
		response  *workflowservice.DescribeTaskQueueResponse
		err       error
		wantError string
	}{
		{name: "no queue routing", response: &workflowservice.DescribeTaskQueueResponse{}},
		{name: "no current version", response: &workflowservice.DescribeTaskQueueResponse{VersioningInfo: &taskqueuepb.TaskQueueVersioningInfo{}}},
		{name: "owner unavailable", err: failure, wantError: "owner unavailable"},
		{name: "missing response", wantError: "no workflow task queue description"},
		{name: "incomplete target", response: &workflowservice.DescribeTaskQueueResponse{VersioningInfo: &taskqueuepb.TaskQueueVersioningInfo{CurrentDeploymentVersion: &deploymentpb.WorkerDeploymentVersion{DeploymentName: "execution"}}}, wantError: "incomplete current workflow target"},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := &Engine{client: &workflowTargetClient{response: test.response, err: test.err}}
			target, err := adapter.CurrentWorkflowTarget(t.Context(), "agent_workflow")
			assert.Nil(t, target)
			if test.wantError == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, test.wantError)
			}
			if test.err != nil {
				assert.ErrorIs(t, err, test.err)
			}
		})
	}
}

func (c *workflowTargetClient) DescribeTaskQueue(_ context.Context, queue string, kind enumspb.TaskQueueType) (*workflowservice.DescribeTaskQueueResponse, error) {
	c.queue, c.kind = queue, kind
	return c.response, c.err
}
