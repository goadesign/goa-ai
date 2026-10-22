// Direct cancellation tests keep provider preparation separate from session
// admission. The engine must settle a blocked resolver without invented history.
package runtime

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	genregistry "goa.design/goa-ai/registry/gen/registry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

// expiredOwnerEngine preserves new executions while making the original saved
// owner's engine metadata unavailable. Session-owned cancellation must still finish.
type expiredOwnerEngine struct {
	engine.Engine
	expiredRun   string
	expiredReads atomic.Int32
}

func TestDirectOperationCancellationStopsBlockedPreparation(t *testing.T) {
	resolution := testNativeRegistryResolution("revision/1")
	operation := resolution.Toolset.Tools[0]
	resolved, err := registrycontract.Resolve(resolution)
	require.NoError(t, err)
	spec := resolved.Specs[tools.Ident(operation.Name)]
	rt := New(newTestStore(), WithEngine(engineinmem.New()))
	definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
	var plans atomic.Int32
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "direct.plan", ResumeActivityName: "direct.resume", ExecuteToolActivity: "direct.execute",
		Planner: &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
			plans.Add(1)
			return nil, context.Canceled
		}},
	}))
	started, stopped := make(chan struct{}), make(chan struct{})
	require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(ctx context.Context, _ genregistry.ExecutionInvocation, _ string, _ *ToolCall) (*AgentToolConfiguration, error) {
		close(started)
		<-ctx.Done()
		close(stopped)
		return nil, ctx.Err()
	}))
	_, err = createSessionForTest(t.Context(), rt.Store, "session")
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	handle, err := rt.StartOperation(ctx, "session", "blocked-run", operation, rawjson.Message(`{"value":1}`), nil, nil)
	require.NoError(t, err)
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("provider preparation did not start")
	}
	requester := rt.Engine.(engine.CancellationRequester)
	require.ErrorIs(t, requester.RequestCancellation(ctx, "blocked-run", engine.CancellationRequest{RunID: "unrelated", Reason: run.CancellationReasonUserRequested}), engine.ErrCancellationRunNotOwned)
	request := CancelRequest{RunID: "blocked-run", Reason: run.CancellationReasonUserRequested}
	require.NoError(t, rt.CancelRun(ctx, request))
	_, err = handle.Wait(ctx)
	require.ErrorIs(t, err, context.Canceled)
	select {
	case <-stopped:
	case <-ctx.Done():
		t.Fatal("provider preparation did not observe cancellation")
	}
	completion, err := rt.Engine.QueryRunCompletion(ctx, "blocked-run")
	require.NoError(t, err)
	assert.Equal(t, engine.RunStatusCanceled, completion.Status)
	_, err = rt.Store.LoadRun(ctx, "blocked-run")
	require.ErrorIs(t, err, session.ErrRunNotFound)
	assert.Zero(t, plans.Load())
	require.NoError(t, rt.CancelRun(ctx, request))
	err = rt.CancelRun(ctx, CancelRequest{RunID: request.RunID, Reason: run.CancellationReasonSessionEnded})
	var conflict *CancellationReasonConflictError
	assert.ErrorAs(t, err, &conflict)
}

func TestDirectSuspendedCancellationPreservesInitialAndContinuedRequests(t *testing.T) {
	for _, variation := range []struct {
		continued bool
		nextTurn  bool
	}{{}, {continued: true}, {nextTurn: true}, {continued: true, nextTurn: true}} {
		continued := variation.continued
		name := "initial direct operation"
		if continued {
			name = "continued direct operation with launch metadata"
		}
		if variation.nextTurn {
			name += "/completed source history"
		}
		t.Run(name, func(t *testing.T) {
			resolution := testNativeRegistryResolution("revision/1")
			operation := resolution.Toolset.Tools[0]
			resolved, err := registrycontract.Resolve(resolution)
			require.NoError(t, err)
			spec := resolved.Specs[tools.Ident(operation.Name)]
			rt := New(newTestStore(), WithEngine(engineinmem.New()))
			definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
			var plans, resumes, preparations atomic.Int32
			require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
				Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "direct.plan", ResumeActivityName: "direct.resume", ExecuteToolActivity: "direct.execute",
				Planner: &stubPlanner{
					start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
						plans.Add(1)
						return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "first-question", Question: "Which record?"}))}, nil
					},
					resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
						resumes.Add(1)
						return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "second-question", Question: "Which date?"}))}, nil
					},
				},
			}))
			require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
				preparations.Add(1)
				messages := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read a record."}}}}
				history := LiteralAgentHistory(messages)
				if variation.nextTurn {
					history = NextTurnAgentHistory("completed", nil, messages).WithoutPriorReasoning()
				}
				return &AgentToolConfiguration{History: history}, nil
			}))
			_, err = createSessionForTest(t.Context(), rt.Store, "session")
			require.NoError(t, err)
			if variation.nextTurn {
				publishProviderHistorySource(t, rt, definition.route.ID, "session", "completed", []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Earlier question"}}}}, true)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			handle, err := rt.StartOperation(ctx, "session", "source", operation, rawjson.Message(`{"value":1}`), nil, nil)
			require.NoError(t, err)
			out, err := handle.Wait(ctx)
			require.NoError(t, err)
			require.NotNil(t, out.Suspension)
			target := "source"
			options := WorkflowOptions{}
			if continued {
				options = WorkflowOptions{Memo: map[string]any{"request_note": "retained"}, SearchAttributes: map[string]any{"SessionID": "session"}}
				routeOnly := NewAgentDefinition(definition.route, nil, nil, nil, nil, nil, nil)
				out, err = rt.MustClientFor(routeOnly).Continue(ctx, "session", "source", "answer", "turn-2", &api.PendingInputResponse{
					Clarification: &api.ClarificationAnswer{ID: "first-question", Answer: "Record seven"},
				}, options)
				require.NoError(t, err)
				require.NotNil(t, out.Suspension)
				target = "answer"
			}
			acceptedCompletion, err := rt.Engine.QueryRunCompletion(ctx, target)
			require.NoError(t, err)
			retainedEngine := &expiredOwnerEngine{Engine: rt.Engine, expiredRun: target}
			rt.Engine = retainedEngine
			before, err := rt.Store.LoadRunSuspension(ctx, target)
			require.NoError(t, err)
			records, err := rt.Store.ListRunRecords(ctx, target, "", 100)
			require.NoError(t, err)
			request := CancelRequest{RunID: target, Reason: run.CancellationReasonUserRequested}
			require.NoError(t, rt.CancelRun(ctx, request))
			require.NoError(t, rt.CancelRun(ctx, request))
			retained, err := rt.Store.LoadRun(ctx, target)
			require.NoError(t, err)
			closed, ok := retained.SuspensionResolution.(session.RelinquishedSuspension)
			require.True(t, ok)
			assert.Equal(t, request.Reason, closed.Reason)
			assert.Equal(t, session.RunStatusSuspended, retained.Status)
			after, err := rt.Store.LoadRunSuspension(ctx, target)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			afterRecords, err := rt.Store.ListRunRecords(ctx, target, "", 100)
			require.NoError(t, err)
			assert.Equal(t, records, afterRecords)
			assert.EqualValues(t, 1, plans.Load())
			assert.EqualValues(t, 1, preparations.Load())
			if continued {
				assert.EqualValues(t, 1, resumes.Load())
			} else {
				assert.Zero(t, resumes.Load())
			}
			require.NoError(t, rt.CancelRun(ctx, request))
			require.NoError(t, rt.CancelRun(ctx, CancelRequest{RunID: target, Reason: run.CancellationReasonSessionEnded}))
			closedCompletion, err := retainedEngine.Engine.QueryRunCompletion(ctx, target)
			require.NoError(t, err)
			assert.Equal(t, acceptedCompletion.StartRequestDigest, closedCompletion.StartRequestDigest)
			assert.Zero(t, retainedEngine.expiredReads.Load(), "closure must not read expired engine routing")
			if !continued {
				return
			}
			seed, err := rt.Store.LoadRunSeed(ctx, retained.RunID, retained.SeedEndID)
			require.NoError(t, err)
			accepted, found, err := rt.Store.FindRunPreparation(ctx, storage.PreparationOperation{
				Subject: retained.Subject, RunID: retained.RunID, SessionID: retained.SessionID, CommandID: seed.Declaration.CommandID,
			})
			require.NoError(t, err)
			require.True(t, found)
			data, err := readPreparation(ctx, rt.Store, accepted)
			require.NoError(t, err)
			saved, err := startrecipe.ParsePreparedRequest(data)
			require.NoError(t, err)
			launch, err := encodeWorkflowLaunch(options)
			require.NoError(t, err)
			expected := saved.Request
			expected.Memo, expected.SearchAttributes = launch.memo, launch.searchAttributes
			snapshot, err := startrecipe.SnapshotRequest(expected)
			require.NoError(t, err)
			assert.Equal(t, snapshot.Digest, saved.Digest, "closure preserves the original accepted launch metadata")
			assert.Equal(t, definition.route.WorkflowName, saved.Request.Workflow)
			assert.Equal(t, definition.route.DefaultTaskQueue, saved.Request.TaskQueue)
			assert.Zero(t, retainedEngine.expiredReads.Load(), "saved cancellation must not read expired engine routing")
		})
	}
}

func (e *expiredOwnerEngine) QueryRunCompletion(ctx context.Context, id string) (engine.RunCompletion, error) {
	if id == e.expiredRun {
		e.expiredReads.Add(1)
		return engine.RunCompletion{}, engine.ErrWorkflowNotFound
	}
	return e.Engine.QueryRunCompletion(ctx, id)
}

func (e *expiredOwnerEngine) StartActiveCancellationWorkflow(ctx context.Context, id string, request engine.CancellationRequest, target *engine.WorkerDeploymentVersion) error {
	if id == e.expiredRun {
		e.expiredReads.Add(1)
		return engine.ErrWorkflowNotFound
	}
	return e.Engine.StartActiveCancellationWorkflow(ctx, id, request, target)
}

func (e *expiredOwnerEngine) RequestCancellation(ctx context.Context, id string, request engine.CancellationRequest) error {
	return e.Engine.(engine.CancellationRequester).RequestCancellation(ctx, id, request)
}
