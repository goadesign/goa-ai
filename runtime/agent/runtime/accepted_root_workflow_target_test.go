// Saved continuations and cancellation keep the worker target selected for the
// original root. Published retries do not read a replacement owner or queue.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	// targetCaptureEngine records new outer starts without changing child starts.
	// A selected failure lets a test inspect a published cleanup without running it.
	targetCaptureEngine struct {
		engine.Engine
		starts      []engine.WorkflowStartRequest
		jobTarget   *engine.WorkerDeploymentVersion
		jobRequests int
		startError  error
	}
)

func TestAcceptedRootTargetRetainsNativeContinuationAndPublicPreparation(t *testing.T) {
	for _, publicClient := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact operation", true: "public client"}[publicClient], func(t *testing.T) {
			store, definition := acceptedTargetNativeSuspension(t)
			backend := &stubEngine{}
			reads := 0
			selected := &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
			read := func(_ context.Context, originalRootID string) (*engine.WorkerDeploymentVersion, error) {
				reads++
				assert.Equal(t, "run-1", originalRootID)
				return selected, nil
			}
			rt := New(store, WithEngine(backend), WithAcceptedRootWorkflowTarget(read))
			response := directConsumerResponse(rawjson.Message(`1`))
			if publicClient {
				client := rt.MustClientFor(definition)
				prepared, err := client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", response, WorkflowOptions{})
				require.NoError(t, err)
				_, err = client.StartPrepared(t.Context(), prepared)
				require.NoError(t, err)
			} else {
				_, err := rt.StartToolContinuation(t.Context(), "run-1", "run-2", response)
				require.NoError(t, err)
			}
			first, err := startrecipe.SnapshotRequest(backend.last)
			require.NoError(t, err)
			assert.Equal(t, selected, first.Request.RootExecutionTarget)
			assert.Equal(t, 1, reads)
			selected.BuildID = "build-b"
			unavailable := errors.New("accepted root owner unavailable")
			restarted := New(store, WithEngine(backend), WithAcceptedRootWorkflowTarget(func(context.Context, string) (*engine.WorkerDeploymentVersion, error) {
				t.Error("published successor must not re-read its target")
				return nil, unavailable
			}))
			if publicClient {
				client := restarted.MustClientFor(definition)
				prepared, err := client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", response, WorkflowOptions{})
				require.NoError(t, err)
				_, err = client.StartPrepared(t.Context(), prepared)
				require.NoError(t, err)
				_, err = client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", directConsumerResponse(rawjson.Message(`2`)), WorkflowOptions{})
				require.ErrorIs(t, err, storage.ErrSeedConflict)
			} else {
				_, err := restarted.StartToolContinuation(t.Context(), "run-1", "run-2", response)
				require.NoError(t, err)
				completion := &completionOnlyEngine{completion: engine.RunCompletion{Status: engine.RunStatusRunning, StartRequestDigest: first.Digest}}
				restarted.Engine = completion
				require.ErrorIs(t, restarted.EnsureToolContinuationCompletion(t.Context(), "run-1", "run-2", response), ErrRunCompletionNotReady)
			}
			again, err := startrecipe.SnapshotRequest(backend.last)
			require.NoError(t, err)
			assert.Equal(t, first.Digest, again.Digest)
			assert.Equal(t, "build-a", again.Request.RootExecutionTarget.BuildID)
		})
	}
}

func TestAcceptedRootTargetNativeCancellationUsesOriginalDeclaration(t *testing.T) {
	store, _ := acceptedTargetNativeSuspension(t)
	target := &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
	rt := New(store, WithAcceptedRootWorkflowTarget(func(_ context.Context, rootID string) (*engine.WorkerDeploymentVersion, error) {
		assert.Equal(t, "run-1", rootID)
		return target, nil
	}))
	owner, err := store.LoadRun(t.Context(), "run-1")
	require.NoError(t, err)
	route, launch, err := rt.cancellationLaunch(t.Context(), owner)
	require.NoError(t, err)
	assert.Equal(t, "generic.workflow", route.WorkflowName)
	assert.Equal(t, target, launch.rootExecutionTarget)
	request, err := rt.prepareOperationContinuationRequest(t.Context(), "run-1", "cancel-run", &api.RunContinuationInput{
		Cancellation: &api.CancellationRequest{RunID: "run-1", Reason: "user_requested"},
	})
	require.NoError(t, err)
	assert.Equal(t, target, request.RootExecutionTarget)
}

func TestAcceptedRootTargetOwnerFailureAndExplicitUnversioned(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "explicit unversioned", true: "owner unavailable"}[fail], func(t *testing.T) {
			store, _ := acceptedTargetNativeSuspension(t)
			backend := &stubEngine{}
			unavailable := errors.New("accepted root owner unavailable")
			rt := New(store, WithEngine(backend), WithAcceptedRootWorkflowTarget(func(context.Context, string) (*engine.WorkerDeploymentVersion, error) {
				if fail {
					return nil, unavailable
				}
				return nil, nil
			}))
			_, err := rt.StartToolContinuation(t.Context(), "run-1", "run-2", directConsumerResponse(rawjson.Message(`1`)))
			if fail {
				require.ErrorIs(t, err, unavailable)
				assert.Zero(t, backend.startCalls)
			} else {
				require.NoError(t, err)
				assert.Nil(t, backend.last.RootExecutionTarget)
			}
		})
	}
}

func TestAcceptedRootTargetCancellationJobAndServiceCleanupStayOnAcceptedBuild(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	questions := &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"choice": {
		Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`),
	}}}
	zero := int64(0)
	pending, err := api.NewPendingTaskInput("provider-task", &zero, questions)
	require.NoError(t, err)
	backend := &targetCaptureEngine{Engine: registeredRootTestEngine{Engine: engineinmem.New(), execute: func(context.Context, *api.ToolInput) (*api.ToolOutput, error) {
		return &api.ToolOutput{PendingExecution: pending}, nil
	}}}
	target := &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
	reads := 0
	rt := New(newTestStore(), WithEngine(backend), WithToolWorkflowQueue("service-worker"), WithAcceptedRootWorkflowTarget(func(_ context.Context, rootID string) (*engine.WorkerDeploymentVersion, error) {
		reads++
		assert.Equal(t, "service-root", rootID)
		return target, nil
	}))
	require.NoError(t, rt.RegisterToolWorkflow(t.Context()))
	handle, err := rt.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil, WithRootExecutionTarget(*target))
	require.NoError(t, err)
	output, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, output.Suspension)
	request := engine.CancellationRequest{RunID: "service-root", Reason: "user_requested"}
	require.NoError(t, rt.CancelRun(t.Context(), request))
	assert.Equal(t, 1, backend.jobRequests)
	assert.Equal(t, target, backend.jobTarget)
	backend.startError = errors.New("cleanup admission reply unavailable")
	_, err = rt.cancellationActivity(t.Context(), request)
	require.ErrorIs(t, err, backend.startError)
	require.Len(t, backend.starts, 2)
	cleanup := backend.starts[1]
	assert.Equal(t, target, cleanup.RootExecutionTarget)
	assert.Equal(t, request, *cleanup.Input.Continuation.Cancellation)
	before, err := startrecipe.SnapshotRequest(cleanup)
	require.NoError(t, err)
	assert.Equal(t, 2, reads)
	rt.acceptedRootWorkflowTarget = func(context.Context, string) (*engine.WorkerDeploymentVersion, error) {
		t.Error("published cleanup must not re-read its target")
		return nil, errors.New("owner unavailable after publication")
	}
	_, err = rt.cancellationActivity(t.Context(), request)
	require.ErrorIs(t, err, backend.startError)
	require.Len(t, backend.starts, 3)
	after, err := startrecipe.SnapshotRequest(backend.starts[2])
	require.NoError(t, err)
	assert.Equal(t, before.Digest, after.Digest)
}

func TestAcceptedRootTargetFollowsOriginalRootThroughChildAndSuccessor(t *testing.T) {
	store := newTestStore()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("parent.agent")}
	admitRunForTest(t, store, session.RunMeta{Subject: subject, RunID: "original-root", SessionID: "session", Status: session.RunStatusRunning})
	require.NoError(t, storeSuspensionForTest(t.Context(), store, "original-root", session.RunSuspension{ID: "held", Data: []byte(`{}`)}))
	admitContinuedRunForTest(t, store, session.RunMeta{Subject: subject, RunID: "parent-successor", SessionID: "session", Status: session.RunStatusRunning}, "original-root")
	admitRunForTest(t, store, session.RunMeta{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("child.agent")},
		RunID:   "child", SessionID: "session", ParentRunID: "parent-successor", Status: session.RunStatusRunning,
	})
	rt := New(store, WithAcceptedRootWorkflowTarget(func(_ context.Context, rootID string) (*engine.WorkerDeploymentVersion, error) {
		assert.Equal(t, "original-root", rootID)
		return &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}, nil
	}))
	child, err := store.LoadRun(t.Context(), "child")
	require.NoError(t, err)
	target, err := rt.acceptedWorkflowTarget(t.Context(), child)
	require.NoError(t, err)
	assert.Equal(t, "build-a", target.BuildID)
}

// acceptedTargetNativeSuspension creates real lifecycle rows and a generated
// native declaration; only the deployment owner and engine start are test fakes.
func acceptedTargetNativeSuspension(t *testing.T) (*testStore, AgentDefinition) {
	t.Helper()
	suspension, definition := directConsumerSuspension(t)
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(definition.route.ID))},
		RunID:   "run-1", SessionID: "session-1", Status: session.RunStatusRunning,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), store, "run-1", session.RunSuspension{ID: suspension.ID, Data: data}))
	return store, definition
}

func (e *targetCaptureEngine) StartWorkflow(ctx context.Context, request engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	e.starts = append(e.starts, request)
	if e.startError != nil {
		return nil, e.startError
	}
	return e.Engine.StartWorkflow(ctx, request)
}

func (e *targetCaptureEngine) RequestCancellation(ctx context.Context, runID string, request engine.CancellationRequest) error {
	return e.Engine.(engine.CancellationRequester).RequestCancellation(ctx, runID, request)
}

func (e *targetCaptureEngine) StartCancellationWorkflow(_ context.Context, _, _ string, _ engine.CancellationRequest, target *engine.WorkerDeploymentVersion) error {
	e.jobRequests++
	e.jobTarget = target
	return nil
}
