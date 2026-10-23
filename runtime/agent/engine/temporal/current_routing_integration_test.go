//go:build integration

// These synthetic workflows let Temporal choose workers when accepting a new
// execution. Replaying the same saved request must recover the original run.
package temporal

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

func TestTemporalServerImplicitCurrentStartKeepsAcceptedBuildOnRetry(t *testing.T) {
	caller, queue := localRequestEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	deploymentName := queue + "-workers"
	var effects atomic.Int32
	for _, build := range []string{"build-a", "build-b", "build-c"} {
		backend := implicitRoutingWorker(t, queue, deploymentName, build, &effects)
		t.Cleanup(func() { assert.NoError(t, backend.Close()) })
	}
	deployment := caller.client.WorkerDeploymentClient().GetHandle(deploymentName)
	waitImplicitQueues(t, ctx, deployment, []string{"build-a", "build-b", "build-c"}, queue)
	_, err := deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "build-b"})
	require.NoError(t, err)
	request := implicitRoutingRequest(queue, queue+"-answer")
	require.Nil(t, request.RootExecutionTarget)
	first, err := caller.StartWorkflow(ctx, request)
	require.NoError(t, err)
	require.EventuallyWithT(t, func(collect *assert.CollectT) {
		assert.EqualValues(collect, 1, effects.Load())
	}, 10*time.Second, 20*time.Millisecond)
	description, err := caller.client.DescribeWorkflowExecution(ctx, request.ID, "")
	require.NoError(t, err)
	acceptedID := description.GetWorkflowExecutionInfo().GetExecution().GetRunId()
	assert.Equal(t, "build-b", description.GetWorkflowExecutionInfo().GetVersioningInfo().GetDeploymentVersion().GetBuildId())
	assert.Nil(t, description.GetWorkflowExecutionInfo().GetVersioningInfo().GetVersioningOverride())
	_, err = deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "build-c"})
	require.NoError(t, err)
	retried, err := caller.StartWorkflow(ctx, request)
	require.NoError(t, err)
	description, err = caller.client.DescribeWorkflowExecution(ctx, request.ID, "")
	require.NoError(t, err)
	assert.Equal(t, acceptedID, description.GetWorkflowExecutionInfo().GetExecution().GetRunId())
	assert.Equal(t, "build-b", description.GetWorkflowExecutionInfo().GetVersioningInfo().GetDeploymentVersion().GetBuildId())
	changed := request
	changed.Input = &api.RunInput{Subject: request.Input.Subject, RunID: request.ID, TurnID: "different-answer"}
	_, err = caller.StartWorkflow(ctx, changed)
	var conflict *engine.WorkflowStartConflictError
	assert.ErrorAs(t, err, &conflict)
	require.NoError(t, caller.client.SignalWorkflow(ctx, request.ID, acceptedID, "finish", nil))
	_, err = first.Wait(ctx)
	require.NoError(t, err)
	_, err = retried.Wait(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, effects.Load(), "a duplicate start must not repeat the activity")
}

func TestTemporalServerImplicitStartBeforeDispatchUsesNewCurrent(t *testing.T) {
	caller, queue := localRequestEngine(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	deploymentName := queue + "-workers"
	var effects atomic.Int32
	b := implicitRoutingWorker(t, queue, deploymentName, "build-b", &effects)
	c := implicitRoutingWorker(t, queue, deploymentName, "build-c", &effects)
	deployment := caller.client.WorkerDeploymentClient().GetHandle(deploymentName)
	waitImplicitQueues(t, ctx, deployment, []string{"build-b", "build-c"}, queue)
	require.NoError(t, b.Close())
	require.NoError(t, c.Close())
	_, err := deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "build-b"})
	require.NoError(t, err)
	request := implicitRoutingRequest(queue, queue+"-staged-answer")
	first, err := caller.StartWorkflow(ctx, request)
	require.NoError(t, err)
	description, err := caller.client.DescribeWorkflowExecution(ctx, request.ID, "")
	require.NoError(t, err)
	assert.Empty(t, description.GetWorkflowExecutionInfo().GetVersioningInfo().GetDeploymentVersion().GetBuildId())
	assert.Zero(t, effects.Load())
	_, err = deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "build-c"})
	require.NoError(t, err)
	current := implicitRoutingWorker(t, queue, deploymentName, "build-c", &effects)
	t.Cleanup(func() { assert.NoError(t, current.Close()) })
	require.EventuallyWithT(t, func(collect *assert.CollectT) { assert.EqualValues(collect, 1, effects.Load()) }, 10*time.Second, 20*time.Millisecond)
	description, err = caller.client.DescribeWorkflowExecution(ctx, request.ID, "")
	require.NoError(t, err)
	assert.Equal(t, "build-c", description.GetWorkflowExecutionInfo().GetVersioningInfo().GetDeploymentVersion().GetBuildId())
	assert.Nil(t, description.GetWorkflowExecutionInfo().GetVersioningInfo().GetVersioningOverride())
	retried, err := caller.StartWorkflow(ctx, request)
	require.NoError(t, err)
	require.NoError(t, caller.client.SignalWorkflow(ctx, request.ID, "", "finish", nil))
	_, err = first.Wait(ctx)
	require.NoError(t, err)
	_, err = retried.Wait(ctx)
	require.NoError(t, err)
	assert.EqualValues(t, 1, effects.Load())
}

func TestTemporalServerImplicitCurrentUsesActualQueueAndUnversionedWorkers(t *testing.T) {
	for _, versioned := range []bool{false, true} {
		t.Run(map[bool]string{false: "unversioned", true: "independent versioned queue"}[versioned], func(t *testing.T) {
			caller, defaultQueue := localRequestEngine(t)
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			queue := defaultQueue + "-override"
			deploymentName, build := "", ""
			if versioned {
				deploymentName, build = queue+"-external", "independent-build"
			}
			var effects atomic.Int32
			backend := implicitRoutingWorker(t, queue, deploymentName, build, &effects)
			t.Cleanup(func() { assert.NoError(t, backend.Close()) })
			if versioned {
				deployment := caller.client.WorkerDeploymentClient().GetHandle(deploymentName)
				waitImplicitQueues(t, ctx, deployment, []string{build}, queue)
				_, err := deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: build})
				require.NoError(t, err)
			}
			request := implicitRoutingRequest(queue, queue+"-answer")
			first, err := caller.StartWorkflow(ctx, request)
			require.NoError(t, err)
			require.EventuallyWithT(t, func(collect *assert.CollectT) { assert.EqualValues(collect, 1, effects.Load()) }, 10*time.Second, 20*time.Millisecond)
			description, err := caller.client.DescribeWorkflowExecution(ctx, request.ID, "")
			require.NoError(t, err)
			info := description.GetWorkflowExecutionInfo()
			assert.Equal(t, queue, info.GetTaskQueue())
			assert.Equal(t, build, info.GetVersioningInfo().GetDeploymentVersion().GetBuildId())
			assert.Nil(t, info.GetVersioningInfo().GetVersioningOverride())
			retried, err := caller.StartWorkflow(ctx, request)
			require.NoError(t, err)
			require.NoError(t, caller.client.SignalWorkflow(ctx, request.ID, "", "finish", nil))
			_, err = first.Wait(ctx)
			require.NoError(t, err)
			_, err = retried.Wait(ctx)
			require.NoError(t, err)
			assert.EqualValues(t, 1, effects.Load())
		})
	}
}

func implicitRoutingWorker(t *testing.T, queue, deploymentName, build string, effects *atomic.Int32) *Engine {
	t.Helper()
	workerOptions := worker.Options{}
	if deploymentName != "" {
		workerOptions.DeploymentOptions = worker.DeploymentOptions{UseVersioning: true, Version: worker.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: build}, DefaultVersioningBehavior: workflow.VersioningBehaviorPinned}
	}
	backend, err := NewWorker(Options{
		ClientOptions:   &client.Options{HostPort: os.Getenv("GOA_AI_START_TEST_TEMPORAL_ADDRESS"), Namespace: "default"},
		WorkerOptions:   WorkerOptions{TaskQueue: queue, Options: workerOptions},
		Instrumentation: InstrumentationOptions{DisableTracing: true, DisableMetrics: true},
	})
	require.NoError(t, err)
	backend.workerForQueue(queue).worker.RegisterActivityWithOptions(func() error { effects.Add(1); return nil }, activity.RegisterOptions{Name: "implicit.effect"})
	require.NoError(t, backend.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{Name: "implicit.answer", TaskQueue: queue, Handler: func(w engine.WorkflowContext, input *api.RunInput) (*api.RunOutput, error) {
		ctx := workflow.WithActivityOptions(w.(*temporalWorkflowContext).ctx, workflow.ActivityOptions{StartToCloseTimeout: 5 * time.Second})
		if err := workflow.ExecuteActivity(ctx, "implicit.effect").Get(ctx, nil); err != nil {
			return nil, err
		}
		workflow.GetSignalChannel(ctx, "finish").Receive(ctx, nil)
		return &api.RunOutput{Subject: input.Subject, RunID: input.RunID}, nil
	}}))
	require.NoError(t, backend.SealRegistration(t.Context()))
	return backend
}

func implicitRoutingRequest(queue, id string) engine.WorkflowStartRequest {
	return engine.WorkflowStartRequest{ID: id, Workflow: "implicit.answer", TaskQueue: queue, Input: &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("synthetic.agent")}, RunID: id}}
}

func waitImplicitQueues(t *testing.T, ctx context.Context, deployment client.WorkerDeploymentHandle, builds []string, queue string) {
	t.Helper()
	for _, build := range builds {
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			description, err := deployment.DescribeVersion(ctx, client.WorkerDeploymentDescribeVersionOptions{BuildID: build})
			require.NoError(collect, err)
			assert.Contains(collect, description.Info.TaskQueuesInfos, client.WorkerDeploymentTaskQueueInfo{Name: queue, Type: client.TaskQueueTypeWorkflow})
		}, 10*time.Second, 100*time.Millisecond)
	}
}
