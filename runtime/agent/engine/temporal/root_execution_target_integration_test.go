//go:build integration

// This synthetic check uses an explicitly supplied local Temporal server. Both
// builds poll both queues; a root pinned to the earlier build must keep its child
// on that build even when the task queue's current build has changed.
package temporal

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestTemporalServerPinnedRootChildKeepsEarlierBuildAcrossQueues(t *testing.T) {
	caller, rootQueue := localRequestEngine(t)
	childQueue := rootQueue + "-child"
	deploymentName := rootQueue + "-workers"
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	for _, buildID := range []string{"build-a", "build-b"} {
		backend, err := NewWorker(Options{
			ClientOptions: &client.Options{HostPort: os.Getenv("GOA_AI_START_TEST_TEMPORAL_ADDRESS"), Namespace: "default"},
			WorkerOptions: WorkerOptions{
				TaskQueue: rootQueue,
				Options: worker.Options{DeploymentOptions: worker.DeploymentOptions{
					UseVersioning: true,
					Version: worker.WorkerDeploymentVersion{
						DeploymentName: deploymentName, BuildID: buildID,
					},
					DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
				}},
			},
			Instrumentation: InstrumentationOptions{DisableTracing: true, DisableMetrics: true},
		})
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, backend.Close()) })
		require.NoError(t, backend.RegisterWorkflow(ctx, engine.WorkflowDefinition{
			Name: "retained.child", TaskQueue: childQueue,
			Handler: func(_ engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
				return &api.RunOutput{Subject: input.Subject, RunID: input.RunID}, nil
			},
		}))
		require.NoError(t, backend.RegisterWorkflow(ctx, engine.WorkflowDefinition{
			Name: "retained.parent", TaskQueue: rootQueue,
			Handler: func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
				childID := input.RunID + "-child"
				child, err := w.StartChildWorkflow(w.Context(), engine.ChildWorkflowRequest{
					ID: childID, Workflow: "retained.child", TaskQueue: childQueue,
					Input: &api.RunInput{Subject: input.Subject, RunID: childID},
				})
				if err != nil {
					return nil, err
				}
				return child.Get(w.Context())
			},
		}))
		require.NoError(t, backend.SealRegistration(ctx))
	}
	deployment := caller.client.WorkerDeploymentClient().GetHandle(deploymentName)
	for _, buildID := range []string{"build-a", "build-b"} {
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			description, err := deployment.DescribeVersion(ctx, client.WorkerDeploymentDescribeVersionOptions{BuildID: buildID})
			require.NoError(collect, err)
			for _, queue := range []string{rootQueue, childQueue} {
				assert.Contains(collect, description.Info.TaskQueuesInfos, client.WorkerDeploymentTaskQueueInfo{
					Name: queue, Type: client.TaskQueueTypeWorkflow,
				})
			}
		}, 10*time.Second, 100*time.Millisecond)
	}
	_, err := deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "build-b"})
	require.NoError(t, err)
	for _, queue := range []string{rootQueue, childQueue} {
		current, err := caller.CurrentWorkflowTarget(ctx, queue)
		require.NoError(t, err)
		require.Equal(t, &engine.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: "build-b"}, current)
	}
	rootID := rootQueue + "-root"
	handle, err := caller.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID: rootID, Workflow: "retained.parent", TaskQueue: rootQueue,
		RootExecutionTarget: &engine.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: "build-a"},
		Input:               &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("synthetic.agent")}, RunID: rootID},
	})
	require.NoError(t, err)
	_, err = handle.Wait(ctx)
	require.NoError(t, err)
	for _, id := range []string{rootID, rootID + "-child"} {
		description, err := caller.client.DescribeWorkflowExecution(ctx, id, "")
		require.NoError(t, err)
		version := description.GetWorkflowExecutionInfo().GetVersioningInfo()
		require.NotNil(t, version)
		assert.Equal(t, deploymentName, version.GetDeploymentVersion().GetDeploymentName())
		assert.Equal(t, "build-a", version.GetDeploymentVersion().GetBuildId())
		pinned := version.GetVersioningOverride().GetPinned()
		require.NotNil(t, pinned)
		assert.Equal(t, "build-a", pinned.GetVersion().GetBuildId())
	}
}
