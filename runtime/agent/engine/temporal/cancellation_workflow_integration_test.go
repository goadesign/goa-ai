//go:build integration

// These checks use an explicitly supplied local Temporal server. A cancellation
// accepted without a worker must settle saved Tasks when a replacement starts.
package temporal

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/run"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/session"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
)

// cancellationOwnerCloseClient finishes the described owner before allowing
// the caller to submit its observer, reproducing worker retirement during admission.
type cancellationOwnerCloseClient struct {
	client.Client
	ownerID          string
	afterDescription func() error
}

func TestTemporalServerCancellationAfterWorkerReplacement(t *testing.T) {
	original, queue := localRequestEngine(t)
	store := storageinmem.New()
	spec := anyJSONToolSpec("remote.lookup")
	definition := testTemporalAgentDefinition("cancel.agent", "cancel.workflow", queue, []tools.ToolSpec{spec})
	plan := &mcpInputPlanner{}
	var creates, cancels atomic.Int32
	install := func(backend *Engine) *agentruntime.Runtime {
		runtime := agentruntime.New(store, agentruntime.WithEngine(backend))
		require.NoError(t, runtime.RegisterToolset(agentruntime.ToolsetRegistration{
			Name: "remote", Specs: []tools.ToolSpec{spec}, DecodeInExecutor: true,
			Execute: func(_ context.Context, call *agentruntime.ToolCall) (*agentruntime.ToolExecutionResult, error) {
				if operation := call.ExecutionContinuation; operation != nil {
					id, cancel := operation.AsTaskCancel()
					assert.True(t, cancel)
					assert.Equal(t, "accepted-task", id)
					cancels.Add(1)
					pending, err := tooloperation.NewPendingTaskWait(id, nil)
					if err != nil {
						return nil, err
					}
					return agentruntime.Unfinished(pending)
				}
				creates.Add(1)
				pending, err := tooloperation.NewPendingTaskInput("accepted-task", nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
					"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)},
				}})
				if err != nil {
					return nil, err
				}
				return agentruntime.Unfinished(pending)
			},
		}))
		require.NoError(t, runtime.RegisterAgent(t.Context(), agentruntime.AgentRegistration{
			Definition: definition, Planner: plan, WorkflowHandler: runtime.ExecuteWorkflow,
			PlanActivityName: "cancel.plan", ResumeActivityName: "cancel.resume", ExecuteToolActivity: "cancel.execute",
		}))
		return runtime
	}
	ctx, stop := context.WithTimeout(t.Context(), time.Minute)
	defer stop()
	runtime := install(original)
	sessionID, runID := queue+"-session", queue+"-source"
	_, err := store.CreateSession(ctx, sessionID, time.Now().UTC())
	require.NoError(t, err)
	first, err := runtime.MustClient("cancel.agent").Run(ctx, sessionID, nil, agentruntime.WithRunID(runID))
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	original.stopWorkers()

	address := os.Getenv("GOA_AI_START_TEST_TEMPORAL_ADDRESS")
	remote, err := NewClient(Options{ClientOptions: &client.Options{HostPort: address, Namespace: "default"}})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, remote.Close()) })
	caller := agentruntime.New(store, agentruntime.WithEngine(remote))
	request := engine.CancellationRequest{RunID: runID, Reason: run.CancellationReasonUserRequested}
	callerContext, cancelCaller := context.WithCancel(ctx)
	require.NoError(t, caller.CancelRun(callerContext, request))
	cancelCaller()
	require.NoError(t, caller.CancelRun(ctx, request))
	before, err := store.LoadRun(ctx, runID)
	require.NoError(t, err)
	assert.Nil(t, before.SuspensionResolution)
	assert.Zero(t, cancels.Load())

	replacement, err := NewWorker(Options{
		ClientOptions:   &client.Options{HostPort: address, Namespace: "default"},
		WorkerOptions:   WorkerOptions{TaskQueue: queue},
		Instrumentation: InstrumentationOptions{DisableTracing: true, DisableMetrics: true},
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, replacement.Close()) })
	resumed := install(replacement)
	require.NoError(t, resumed.Seal(ctx))
	jobID, _, err := startrecipe.CancellationRecipe("cancel.workflow.cancel", queue, request, nil)
	require.NoError(t, err)
	require.NoError(t, replacement.client.GetWorkflow(ctx, jobID, "").Get(ctx, nil))
	previous, err := store.LoadRun(ctx, runID)
	require.NoError(t, err)
	require.NotNil(t, previous.SuspensionResolution)
	cleanup, err := store.LoadRun(ctx, previous.SuspensionResolution.(session.ContinuedSuspension).RunID)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCanceled, cleanup.Status)
	assert.Equal(t, request.Reason, cleanup.CancellationReason)
	assert.EqualValues(t, 1, creates.Load())
	assert.EqualValues(t, 1, cancels.Load())
	assert.EqualValues(t, 1, plan.starts.Load())
	assert.Zero(t, plan.resumes.Load())
	require.NoError(t, caller.CancelRun(ctx, request))
	assert.EqualValues(t, 1, cancels.Load())
}

// Temporal keeps the accepted job memo when history rolls over. Duplicate
// submission must still compare the original request after that rollover.
func TestTemporalServerCancellationKeepsRequestAfterHistoryRollover(t *testing.T) {
	backend, queue := localRequestEngine(t)
	name := "cancellation-rollover"
	request := engine.CancellationRequest{RunID: queue + "-source", Reason: run.CancellationReasonUserRequested}
	jobID, _, err := startrecipe.CancellationRecipe(name, queue, request, nil)
	require.NoError(t, err)
	backend.workerForQueue(queue).worker.RegisterWorkflowWithOptions(func(ctx workflow.Context, got engine.CancellationRequest) error {
		if got != request {
			return errors.New("rollover changed the accepted cancellation request")
		}
		if workflow.GetInfo(ctx).ContinuedExecutionRunID == "" {
			return workflow.NewContinueAsNewErrorWithOptions(ctx, workflow.ContinueAsNewErrorOptions{BackoffStartInterval: time.Millisecond}, name, got)
		}
		return nil
	}, workflow.RegisterOptions{Name: name})
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	require.NoError(t, backend.SealRegistration(ctx))
	require.NoError(t, backend.StartCancellationWorkflow(ctx, name, queue, request, nil))
	require.NoError(t, backend.client.GetWorkflow(ctx, jobID, "").Get(ctx, nil))
	require.NoError(t, backend.StartCancellationWorkflow(ctx, name, queue, request, nil))
	changed := request
	changed.Reason = run.CancellationReasonSessionEnded
	err = backend.StartCancellationWorkflow(ctx, name, queue, changed, nil)
	var conflict *engine.CancellationConflictError
	require.ErrorAs(t, err, &conflict)
}

// Cancellation observations can move to the current worker without redoing
// accepted agent work. Explicitly pinned jobs still retain their original build.
func TestTemporalServerCancellationObserverFollowsCurrentVersion(t *testing.T) {
	caller, queue := localRequestEngine(t)
	deploymentName := queue + "-workers"
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	var observedA atomic.Int32
	var observedB atomic.Int32
	var first *Engine
	for _, buildID := range []string{"build-a", "build-b"} {
		backend, err := NewWorker(Options{
			ClientOptions: &client.Options{HostPort: os.Getenv("GOA_AI_START_TEST_TEMPORAL_ADDRESS"), Namespace: "default"},
			WorkerOptions: WorkerOptions{
				TaskQueue: queue,
				Options: worker.Options{DeploymentOptions: worker.DeploymentOptions{
					UseVersioning:             true,
					Version:                   worker.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: buildID},
					DefaultVersioningBehavior: workflow.VersioningBehaviorPinned,
				}},
			},
			Instrumentation: InstrumentationOptions{DisableTracing: true, DisableMetrics: true},
		})
		require.NoError(t, err)
		t.Cleanup(func() { assert.NoError(t, backend.Close()) })
		if buildID == "build-a" {
			first = backend
		}
		require.NoError(t, backend.RegisterCancellationWorkflow(ctx, "cleanup.owner.cancel", engine.ActivityOptions{
			Queue: queue, StartToCloseTimeout: time.Minute,
			RetryPolicy: engine.RetryPolicy{InitialInterval: 100 * time.Millisecond},
		}, func(_ context.Context, request engine.CancellationRequest) (bool, error) {
			if request.RunID == queue+"-pinned" {
				return true, nil
			}
			if buildID == "build-a" {
				observedA.Add(1)
				return false, nil
			}
			observedB.Add(1)
			return true, nil
		}))
		require.NoError(t, backend.RegisterNativeWorkflow(ctx, queue, func(workflowContext workflow.Context) error {
			workflow.GetSignalChannel(workflowContext, "finish").Receive(workflowContext, nil)
			return nil
		}, workflow.RegisterOptions{Name: "cleanup.owner"}))
		require.NoError(t, backend.SealRegistration(ctx))
	}
	deployment := caller.client.WorkerDeploymentClient().GetHandle(deploymentName)
	for _, buildID := range []string{"build-a", "build-b"} {
		require.EventuallyWithT(t, func(collect *assert.CollectT) {
			description, err := deployment.DescribeVersion(ctx, client.WorkerDeploymentDescribeVersionOptions{BuildID: buildID})
			require.NoError(collect, err)
			assert.Contains(collect, description.Info.TaskQueuesInfos, client.WorkerDeploymentTaskQueueInfo{Name: queue, Type: client.TaskQueueTypeWorkflow})
		}, 10*time.Second, 100*time.Millisecond)
	}
	_, err := deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "build-a"})
	require.NoError(t, err)
	pinnedRequest := engine.CancellationRequest{RunID: queue + "-pinned", Reason: run.CancellationReasonUserRequested}
	pinnedTarget := &engine.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: "build-a"}
	require.NoError(t, caller.StartCancellationWorkflow(ctx, "cleanup.owner.cancel", queue, pinnedRequest, pinnedTarget))
	pinnedID, _, err := startrecipe.CancellationRecipe("cleanup.owner.cancel", queue, pinnedRequest, pinnedTarget)
	require.NoError(t, err)
	require.NoError(t, caller.client.GetWorkflow(ctx, pinnedID, "").Get(ctx, nil))

	request := engine.CancellationRequest{RunID: queue + "-follows", Reason: run.CancellationReasonUserRequested}
	require.NoError(t, caller.StartCancellationWorkflow(ctx, "cleanup.owner.cancel", queue, request, nil))
	require.Eventually(t, func() bool { return observedA.Load() > 0 }, 10*time.Second, 10*time.Millisecond)
	jobID, _, err := startrecipe.CancellationRecipe("cleanup.owner.cancel", queue, request, nil)
	require.NoError(t, err)
	description, err := caller.client.DescribeWorkflowExecution(ctx, jobID, "")
	require.NoError(t, err)
	assert.Equal(t, enumspb.VERSIONING_BEHAVIOR_AUTO_UPGRADE, description.GetWorkflowExecutionInfo().GetVersioningInfo().GetBehavior())
	assert.Nil(t, description.GetWorkflowExecutionInfo().GetVersioningInfo().GetVersioningOverride())

	ownerID := queue + "-owner"
	owner, err := caller.client.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: ownerID, TaskQueue: queue,
		VersioningOverride: &client.PinnedVersioningOverride{Version: worker.WorkerDeploymentVersion{DeploymentName: deploymentName, BuildID: "build-a"}},
	}, "cleanup.owner")
	require.NoError(t, err)
	observerCaller := &Engine{client: &cancellationOwnerCloseClient{
		Client: caller.client, ownerID: ownerID,
		afterDescription: func() error {
			if err := caller.client.SignalWorkflow(ctx, ownerID, "", "finish", nil); err != nil {
				return err
			}
			if err := owner.Get(ctx, nil); err != nil {
				return err
			}
			if _, err := deployment.SetCurrentVersion(ctx, client.WorkerDeploymentSetCurrentVersionOptions{BuildID: "build-b"}); err != nil {
				return err
			}
			first.stopWorkers()
			return nil
		},
	}}
	activeRequest := engine.CancellationRequest{RunID: queue + "-active", Reason: run.CancellationReasonUserRequested}
	require.NoError(t, observerCaller.StartActiveCancellationWorkflow(ctx, ownerID, activeRequest))
	activeJobID, _, err := startrecipe.CancellationRecipe("cleanup.owner.cancel", queue, activeRequest, nil)
	require.NoError(t, err)
	require.NoError(t, caller.client.GetWorkflow(ctx, activeJobID, "").Get(ctx, nil))
	description, err = caller.client.DescribeWorkflowExecution(ctx, activeJobID, "")
	require.NoError(t, err)
	activeVersion := description.GetWorkflowExecutionInfo().GetVersioningInfo()
	assert.Nil(t, activeVersion.GetVersioningOverride())
	assert.Equal(t, "build-b", activeVersion.GetDeploymentVersion().GetBuildId())
	require.NoError(t, caller.client.GetWorkflow(ctx, jobID, "").Get(ctx, nil))
	assert.Positive(t, observedB.Load())
	require.NoError(t, caller.StartCancellationWorkflow(ctx, "cleanup.owner.cancel", queue, request, nil))
	require.NoError(t, caller.StartCancellationWorkflow(ctx, "cleanup.owner.cancel", queue, pinnedRequest, nil))
	description, err = caller.client.DescribeWorkflowExecution(ctx, pinnedID, "")
	require.NoError(t, err)
	assert.Equal(t, "build-a", description.GetWorkflowExecutionInfo().GetVersioningInfo().GetVersioningOverride().GetPinned().GetVersion().GetBuildId())
}

func (c *cancellationOwnerCloseClient) DescribeWorkflowExecution(ctx context.Context, workflowID, runID string) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	description, err := c.Client.DescribeWorkflowExecution(ctx, workflowID, runID)
	if err != nil {
		return nil, err
	}
	if workflowID == c.ownerID {
		if err := c.afterDescription(); err != nil {
			return nil, err
		}
	}
	return description, nil
}
