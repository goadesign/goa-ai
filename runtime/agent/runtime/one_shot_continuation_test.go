// Continuation tests follow direct and registered Agent roots through saved
// input requests and resumed completion, with and without a Session.
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestOneShotContinuationCompletesExactNativeInvocation(t *testing.T) {
	for _, test := range []struct {
		registered         bool
		sessionID          string
		clientContinuation bool
	}{
		{}, {clientContinuation: true}, {registered: true}, {sessionID: "session-1"}, {registered: true, sessionID: "session-1"},
	} {
		registered, sessionID := test.registered, test.sessionID
		name := "direct"
		if registered {
			name = "registered"
		}
		if test.clientContinuation {
			name += "/client"
		}
		t.Run(name+"/session="+sessionID, func(t *testing.T) {
			resolution := testNativeRegistryResolution("revision/1")
			operation := resolution.Toolset.Tools[0]
			resolved, err := registrycontract.Resolve(resolution)
			require.NoError(t, err)
			spec := resolved.Specs[tools.Ident(operation.Name)]
			_, invocation := registeredRootFixture()
			invocation.Target.Target = genregistry.NewCatalogTargetNative(&genregistry.NativeCatalogTarget{Toolset: resolution.Toolset.Name, Tool: operation.Name})
			root, ok := invocation.Invocation.Invocation.AsRoot()
			require.True(t, ok)
			originalID := root.RunID
			store := newTestStore()
			rt := New(store, WithEngine(engineinmem.New()))
			rt.streamSubscriber = newCompletionSubscriber(t)
			if sessionID != "" {
				_, err := store.CreateSession(t.Context(), sessionID, time.Now().UTC())
				require.NoError(t, err)
			}
			definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
			starts, resumes, preparations := 0, 0, 0
			require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
				Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
				PlanActivityName: "standalone.plan", ResumeActivityName: "standalone.resume", ExecuteToolActivity: "standalone.execute",
				Planner: &stubPlanner{
					start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
						starts++
						assert.Equal(t, sessionID, input.RunContext.SessionID)
						assert.Equal(t, originalID, input.RunContext.RunID)
						return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "question", Question: "Which record?"}))}, nil
					},
					resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
						resumes++
						assert.Equal(t, sessionID, input.RunContext.SessionID)
						assert.Equal(t, "successor", input.RunContext.RunID)
						assert.Equal(t, spec.Name, input.RunContext.Tool)
						if registered {
							assert.Equal(t, invocation, input.RunContext.RegisteredInvocation)
						} else {
							assert.Equal(t, operation, input.RunContext.Operation)
						}
						return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":9007199254740993}`)}}, nil
					},
				},
			}))
			require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(_ context.Context, origin genregistry.ExecutionInvocation, _ string, call *ToolCall) (*AgentToolConfiguration, error) {
				preparations++
				selected, ok := origin.Invocation.AsRoot()
				require.True(t, ok)
				assert.Equal(t, originalID, selected.RunID)
				assert.Equal(t, sessionID, call.SessionID)
				return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read the selected record."}}}})}, nil
			}))
			var handle engine.WorkflowHandle
			switch {
			case sessionID != "" && registered:
				handle, err = rt.StartToolOperation(t.Context(), sessionID, "registry", resolution, invocation, nil, nil)
			case sessionID != "":
				handle, err = rt.StartOperation(t.Context(), sessionID, originalID, operation, rawjson.Message(`{"value":1}`), nil, nil)
			case registered:
				handle, err = rt.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil)
			default:
				handle, err = rt.StartOneShotOperation(t.Context(), originalID, operation, rawjson.Message(`{"value":1}`), nil, nil)
			}
			require.NoError(t, err)
			first, err := handle.Wait(t.Context())
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			saved, err := rt.LoadRunSuspension(t.Context(), originalID)
			require.NoError(t, err)
			assert.Equal(t, first.Suspension, saved)
			previous, err := rt.Store.LoadRun(t.Context(), originalID)
			require.NoError(t, err)
			assert.Equal(t, sessionID, previous.SessionID)
			assert.Equal(t, session.RunStatusSuspended, previous.Status)
			response := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "record A", Labels: map[string]string{"responder": "operator-1"}}}
			client := rt.MustClientFor(definition)
			if sessionID == "" {
				_, err = client.PrepareContinuation(t.Context(), "", originalID, "invalid", "turn-invalid", response, WorkflowOptions{})
				assert.ErrorIs(t, err, ErrMissingSessionID) //nolint:testifylint // The following continuation request is independent of this rejected request.
				_, err = client.PrepareOneShotContinuation(t.Context(), originalID, "wrong-answer", "turn-wrong", &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "other", Answer: "record A"}}, WorkflowOptions{})
				assert.ErrorIs(t, err, ErrContinuationRejected) //nolint:testifylint // The following stored-position check uses the saved suspension, not this request result.
				// An altered stored position must fail before any successor starts.
				changed := *saved
				rewriteSuspensionCheckpoint(t, &changed, func(c *workflowCheckpoint) {
					c.HistoryEndID = "999999"
				})
				changedData, err := json.Marshal(&changed)
				require.NoError(t, err)
				changedStore := suspensionReadStore{Store: rt.Store, run: previous, suspension: session.RunSuspension{ID: changed.ID, Data: changedData}}
				_, err = New(changedStore).MustClientFor(definition).PrepareOneShotContinuation(t.Context(), originalID, "wrong-position", "wrong-position-turn", response, WorkflowOptions{})
				assert.ErrorIs(t, err, storage.ErrInvalidTranscriptPosition) //nolint:testifylint // The following continuation uses the original store, not this altered-position request result.
			}
			var successor engine.WorkflowHandle
			if test.clientContinuation {
				prepared, prepareErr := client.PrepareOneShotContinuation(t.Context(), originalID, "successor", "turn-successor", response, WorkflowOptions{})
				require.NoError(t, prepareErr)
				successor, err = client.StartPrepared(t.Context(), prepared)
			} else {
				successor, err = rt.StartToolContinuation(t.Context(), originalID, "successor", response, nil)
			}
			require.NoError(t, err)
			output, err := successor.Wait(t.Context())
			require.NoError(t, err)
			require.NotNil(t, output.FinalToolResult)
			assert.Equal(t, `{"value":9007199254740993}`, string(output.FinalToolResult.Result))
			after, err := rt.Store.LoadRun(t.Context(), "successor")
			require.NoError(t, err)
			assert.Equal(t, previous.Origin, after.Origin)
			assert.Equal(t, previous.Subject, after.Subject)
			assert.Equal(t, sessionID, after.SessionID)
			assert.Equal(t, session.RunStatusCompleted, after.Status)
			prior, err := rt.Store.LoadRun(t.Context(), originalID)
			require.NoError(t, err)
			assert.Equal(t, session.ContinuedSuspension{RunID: "successor"}, prior.SuspensionResolution)
			duplicate, err := rt.StartToolContinuation(t.Context(), originalID, "duplicate", response, nil)
			if err == nil {
				_, err = duplicate.Wait(t.Context())
			}
			require.Error(t, err)
			accepted, found, err := rt.Store.FindRunPreparation(t.Context(), storage.PreparationOperation{Subject: previous.Subject, SessionID: sessionID, RunID: "successor", CommandID: "successor"})
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, originalID, accepted.Seed.Declaration.PredecessorRunID)
			assert.Equal(t, originalID, accepted.Seed.Declaration.SourceRunID)
			checkpoint, err := decodeWorkflowCheckpointState(saved)
			require.NoError(t, err)
			assert.Equal(t, checkpoint.HistoryEndID, accepted.Seed.Declaration.SourceEndID)
			preparedData, err := readPreparation(t.Context(), rt.Store, accepted)
			require.NoError(t, err)
			prepared, err := startrecipe.ParsePreparedRequest(preparedData)
			require.NoError(t, err)
			assert.Equal(t, response, prepared.Request.Input.Continuation.Response)
			beforeRecords, err := rt.Store.ListRunRecords(t.Context(), "successor", "", 100)
			require.NoError(t, err)
			for range 2 {
				_, err = rt.StartToolContinuation(t.Context(), originalID, "successor", response, nil)
				require.NoError(t, err)
				require.NoError(t, rt.EnsureToolContinuationCompletion(t.Context(), originalID, "successor", response, nil))
			}
			afterRecords, err := rt.Store.ListRunRecords(t.Context(), "successor", "", 100)
			require.NoError(t, err)
			assert.Equal(t, beforeRecords, afterRecords)
			_, err = rt.StartToolContinuation(t.Context(), originalID, "successor", &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "changed answer"}}, nil)
			require.ErrorIs(t, err, storage.ErrSeedConflict)
			_, err = rt.StartToolContinuation(t.Context(), originalID, "successor", &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "record A", Labels: map[string]string{"responder": "operator-2"}}}, nil)
			require.ErrorIs(t, err, storage.ErrSeedConflict)
			assert.Equal(t, 1, starts)
			assert.Equal(t, 1, resumes)
			assert.Equal(t, 1, preparations)
		})
	}
}

func TestOneShotContinuationRejectsChangedSavedIdentity(t *testing.T) {
	for _, variation := range []string{"valid", "missing agent", "different agent", "different session", "different predecessor", "missing history", "changed pending", "unpublished suspension", "completed predecessor"} {
		t.Run(variation, func(t *testing.T) {
			suspension := suspensionContractFixtureWithContext(t, "svc.lookup", "svc.agent", "run-1", nil, nil)
			rewriteSuspensionCheckpoint(t, suspension, func(c *workflowCheckpoint) { c.SessionID = "" })
			owner := session.RunMeta{RunID: "run-1", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("svc.agent")}, Status: session.RunStatusSuspended}
			switch variation {
			case "missing agent":
				rewriteSuspensionCheckpoint(t, suspension, func(c *workflowCheckpoint) { c.AgentID = "" })
			case "different agent":
				owner.Subject.Executor = genrunidentity.NewRunExecutorAgent("other.agent")
			case "different session":
				owner.SessionID = "conversation"
			case "different predecessor":
				owner.RunID = "other-run"
			case "missing history":
				rewriteSuspensionCheckpoint(t, suspension, func(c *workflowCheckpoint) { c.HistoryEndID = "" })
			case "changed pending":
				suspension.Pending = nil
			case "unpublished suspension":
				owner.Status = session.RunStatusRunning
			case "completed predecessor":
				owner.Status = session.RunStatusCompleted
			}
			data, err := json.Marshal(suspension)
			require.NoError(t, err)
			store := suspensionReadStore{run: owner, suspension: session.RunSuspension{ID: suspension.ID, Data: data}}
			_, err = New(store).LoadRunSuspension(t.Context(), owner.RunID)
			if variation == "valid" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

func TestOneShotHistoryRequiresExactContinuation(t *testing.T) {
	declaration := storage.SeedDeclaration{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("svc.agent")},
		RunID:   "successor", CommandID: "successor", AttemptID: "successor", Kind: storage.SeedContinuation,
		PredecessorRunID: "original", SourceRunID: "original", SourceEndID: "7",
	}
	for _, variation := range []string{"valid", "next turn", "other history", "missing position", "literal", "self", "removed reasoning"} {
		t.Run(variation, func(t *testing.T) {
			d := declaration
			switch variation {
			case "next turn":
				d.Kind = storage.SeedNextTurn
				d.PredecessorRunID = ""
				d.SourceEndID = ""
			case "other history":
				d.SourceRunID = "unrelated"
			case "missing position":
				d.SourceEndID = ""
			case "literal":
				d.Kind = storage.SeedLiteral
				d.SourceRunID = ""
				d.SourceEndID = ""
			case "self":
				d.PredecessorRunID = d.RunID
				d.SourceRunID = d.RunID
			case "removed reasoning":
				d.ExcludeReasoning = true
			}
			err := storage.ValidateSeedDeclaration(d)
			if variation == "valid" {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}
