// New answers and cleanup leave worker selection to the engine. Retrying a
// saved request preserves its exact bytes, including any older explicit target.
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
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	// targetCaptureEngine records new outer starts without changing child starts.
	// A selected failure lets a test inspect a published cleanup without running it.
	targetCaptureEngine struct {
		engine.Engine
		starts      []engine.WorkflowStartRequest
		jobTarget   *engine.WorkerDeploymentVersion
		jobOwnerID  string
		jobRequests int
		startError  error
	}
)

func TestContinuationUsesCurrentUnlessRequestWasAlreadyPublished(t *testing.T) {
	for _, publicClient := range []bool{false, true} {
		for _, published := range []bool{false, true} {
			name := map[bool]string{false: "exact operation", true: "public client"}[publicClient] + "/" + map[bool]string{false: "new request", true: "existing explicit target"}[published]
			t.Run(name, func(t *testing.T) {
				store, definition := acceptedTargetNativeSuspension(t)
				backend := &stubEngine{}
				rt := New(store, WithEngine(backend))
				response := directConsumerResponse(rawjson.Message(`1`))
				memo := map[string]any{"step": "answer"}
				var expected *engine.WorkerDeploymentVersion
				if published {
					expected = &engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}
					turnID := "turn-2"
					if !publicClient {
						owner, err := store.LoadRun(t.Context(), "run-1")
						require.NoError(t, err)
						turnID, err = lifecycle.RegisteredRunTurnID(owner.Subject, "run-2", "run-1")
						require.NoError(t, err)
					}
					input, writer, err := rt.buildStoredContinuationRunInput(t.Context(), definition, "session-1", "run-1", "run-2", turnID, &api.RunContinuationInput{Response: response}, "run-2", "run-2")
					require.NoError(t, err)
					launch, err := encodeWorkflowLaunch(WorkflowOptions{Memo: memo})
					require.NoError(t, err)
					launch.rootExecutionTarget = expected
					request, err := prepareRunWithDefinition(input, launch, definition, true)
					require.NoError(t, err)
					_, err = publishPreparedRun(t.Context(), writer, definition.route.ID, request, "", "run-2")
					require.NoError(t, err)
				}
				var digest [32]byte
				for attempt := range 2 {
					restarted := New(store, WithEngine(backend))
					if publicClient {
						client := restarted.MustClientFor(definition)
						prepared, err := client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", response, WorkflowOptions{Memo: memo})
						require.NoError(t, err)
						_, err = client.StartPrepared(t.Context(), prepared)
						require.NoError(t, err)
						_, err = client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", directConsumerResponse(rawjson.Message(`2`)), WorkflowOptions{Memo: memo})
						require.ErrorIs(t, err, storage.ErrSeedConflict)
						_, err = client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", response, WorkflowOptions{Memo: map[string]any{"step": "changed"}})
						require.ErrorIs(t, err, storage.ErrSeedConflict)
					} else {
						_, err := restarted.StartToolContinuation(t.Context(), "run-1", "run-2", response, memo)
						require.NoError(t, err)
						_, err = restarted.StartToolContinuation(t.Context(), "run-1", "run-2", directConsumerResponse(rawjson.Message(`2`)), memo)
						require.ErrorIs(t, err, storage.ErrSeedConflict)
						_, err = restarted.StartToolContinuation(t.Context(), "run-1", "run-2", response, map[string]any{"step": "changed"})
						require.ErrorIs(t, err, storage.ErrSeedConflict)
					}
					snapshot, err := startrecipe.SnapshotRequest(backend.last)
					require.NoError(t, err)
					assert.Equal(t, expected, snapshot.Request.RootExecutionTarget)
					if attempt == 0 {
						digest = snapshot.Digest
					} else {
						assert.Equal(t, digest, snapshot.Digest)
					}
				}
			})
		}
	}
}

func TestContinuationTargetUsesEffectiveQueue(t *testing.T) {
	backend := &targetCaptureEngine{Engine: engineinmem.New()}
	definition := testAgentDefinition("ordinary.agent", "ordinary.workflow", "default-queue", nil, nil)
	rt := New(newTestStore(), WithEngine(backend))
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "ordinary.plan", ResumeActivityName: "ordinary.resume", ExecuteToolActivity: "ordinary.execute",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "question", Question: "Which room?"}))}, nil
		}},
	}))
	_, err := createSessionForTest(t.Context(), rt.Store, "session-1")
	require.NoError(t, err)
	client := rt.MustClientFor(definition)
	_, err = client.Run(t.Context(), "session-1", nil, WithRunID("run-1"))
	require.NoError(t, err)
	options := WorkflowOptions{TaskQueue: "alternate-queue"}
	response := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "room 2"}}
	prepared, err := client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", response, options)
	require.NoError(t, err)

	recovered, err := client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", response, options)
	require.NoError(t, err)
	first, err := prepared.MarshalBinary()
	require.NoError(t, err)
	again, err := recovered.MarshalBinary()
	require.NoError(t, err)
	assert.Equal(t, first, again)
	backend.startError = errors.New("stop after engine submission")
	_, err = client.StartPrepared(t.Context(), recovered)
	require.ErrorIs(t, err, backend.startError)
	assert.Equal(t, "alternate-queue", backend.starts[1].TaskQueue)
	assert.Nil(t, backend.starts[1].RootExecutionTarget)
}

func TestCancellationKeepsSavedRouteAndChoosesCurrentWorkers(t *testing.T) {
	store, _ := acceptedTargetNativeSuspension(t)
	rt := New(store, WithEngine(&stubEngine{}))
	owner, err := store.LoadRun(t.Context(), "run-1")
	require.NoError(t, err)
	route, launch, err := rt.cancellationLaunch(t.Context(), owner)
	require.NoError(t, err)
	assert.Equal(t, "generic.workflow", route.WorkflowName)
	assert.Nil(t, launch.rootExecutionTarget)
	request, err := rt.prepareOperationContinuationRequest(t.Context(), "run-1", "cancel-run", &api.RunContinuationInput{
		Cancellation: &api.CancellationRequest{RunID: "run-1", Reason: "user_requested"},
	}, nil)
	require.NoError(t, err)
	assert.Nil(t, request.RootExecutionTarget)
}

func TestCancellationSupervisorAndNewCleanupFollowCurrentWorkers(t *testing.T) {
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

	rt := New(newTestStore(), WithEngine(backend), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, rt.RegisterToolWorkflow(t.Context()))
	handle, err := rt.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil, WithRootExecutionTarget(*target))
	require.NoError(t, err)
	output, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, output.Suspension)
	request := engine.CancellationRequest{RunID: "service-root", Reason: "user_requested"}
	require.NoError(t, rt.CancelRun(t.Context(), request))
	assert.Equal(t, 1, backend.jobRequests)
	assert.Nil(t, backend.jobTarget)
	require.NoError(t, rt.CancelRun(t.Context(), request))
	backend.startError = errors.New("cleanup admission reply unavailable")
	_, err = rt.cancellationActivity(t.Context(), request)
	require.ErrorIs(t, err, backend.startError)
	require.Len(t, backend.starts, 2)
	cleanup := backend.starts[1]
	assert.Nil(t, cleanup.RootExecutionTarget)
	assert.Equal(t, request, *cleanup.Input.Continuation.Cancellation)
	before, err := startrecipe.SnapshotRequest(cleanup)
	require.NoError(t, err)
	_, err = rt.cancellationActivity(t.Context(), request)
	require.ErrorIs(t, err, backend.startError)
	require.Len(t, backend.starts, 3)
	after, err := startrecipe.SnapshotRequest(backend.starts[2])
	require.NoError(t, err)
	assert.Equal(t, before.Digest, after.Digest)
}

func TestCancellationUsesHighestActiveSuccessor(t *testing.T) {
	for _, test := range []struct {
		name  string
		child bool
	}{
		{name: "active successor"},
		{name: "active successor parent of child", child: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, definition := acceptedTargetNativeSuspension(t)
			backend := &targetCaptureEngine{Engine: engineinmem.New()}
			rt := New(store, WithEngine(backend))
			prepared, err := rt.MustClientFor(definition).PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", directConsumerResponse(rawjson.Message(`1`)), WorkflowOptions{})
			require.NoError(t, err)
			admitContinuedRunForTest(t, store, session.RunMeta{
				Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(definition.route.ID))},
				RunID:   "run-2", SessionID: "session-1", SeedEndID: prepared.reference.HistoryEndID, Status: session.RunStatusRunning,
			}, "run-1")
			runID := "run-2"
			if test.child {
				admitRunForTest(t, store, session.RunMeta{
					Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("child.agent")},
					RunID:   "child", SessionID: "session-1", ParentRunID: "run-2", Status: session.RunStatusRunning,
				})
				runID = "child"
			}
			owner, err := store.LoadRun(t.Context(), runID)
			require.NoError(t, err)
			require.NoError(t, rt.startSuspendedCancellation(t.Context(), CancelRequest{RunID: runID, Reason: "user_requested"}, owner))
			assert.Equal(t, 1, backend.jobRequests)
			assert.Equal(t, "run-2", backend.jobOwnerID)
		})
	}
}

// acceptedTargetNativeSuspension creates real lifecycle rows and a generated
// native declaration; only the deployment owner and engine start are test fakes.
func acceptedTargetNativeSuspension(t *testing.T) (*testStore, AgentDefinition) {
	t.Helper()
	suspension, definition := directConsumerSuspension(t)
	store := newTestStore()
	rt := New(store, WithEngine(&stubEngine{}))
	_, err := createSessionForTest(t.Context(), store, "session-1")
	require.NoError(t, err)
	prepared, err := rt.MustClientFor(definition).Prepare(t.Context(), "session-1", nil, WithRunID("run-1"), WithRootExecutionTarget(engine.WorkerDeploymentVersion{DeploymentName: "workers", BuildID: "build-a"}))
	require.NoError(t, err)
	admitRunForTest(t, store, session.RunMeta{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(definition.route.ID))},
		RunID:   "run-1", SessionID: "session-1", SeedEndID: prepared.reference.HistoryEndID, Status: session.RunStatusRunning,
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

func (e *targetCaptureEngine) StartActiveCancellationWorkflow(_ context.Context, ownerID string, _ engine.CancellationRequest) error {
	e.jobRequests++
	e.jobOwnerID = ownerID
	return nil
}
