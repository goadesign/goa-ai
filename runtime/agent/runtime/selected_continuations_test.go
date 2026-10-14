package runtime

// These tests select one saved result whose call was scheduled in another run.
// The reader must recover that exact call without importing its other results.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"
	"time"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	literalContinuationHistoryStore struct {
		storage.Store
	}
)

func TestSelectedContinuationResultRetainsExactExternalSchedule(t *testing.T) {
	rt, search, _ := continuationTestRuntime()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}
	call := hooks.NewToolCallScheduledEvent("call-run", subject, "session-1", search.Name,
		"selected-call", rawjson.Message(`{"query":"selected"}`), "", "", 0)
	appendHistoricalHookEvent(t, rt.Store, call, "call", 1)
	unrelated := hooks.NewToolCallScheduledEvent("call-run", subject, "session-1", search.Name,
		"unrelated-call", rawjson.Message(`{"query":"unrelated"}`), "", "", 0)
	appendHistoricalHookEvent(t, rt.Store, unrelated, "unrelated-call", 2)
	unrelatedResult := hooks.NewToolResultReceivedEvent("call-run", subject, "session-1", "call-run",
		search.Name, "unrelated-call", "", rawjson.Message(`{"items":["unrelated"]}`), nil, nil, "Unrelated",
		&agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("unrelated-page")}, time.Second, nil, nil)
	appendHistoricalHookEvent(t, rt.Store, unrelatedResult, "unrelated-result", 3)
	result := hooks.NewToolResultReceivedEvent("result-run", subject, "session-1", "call-run",
		search.Name, "selected-call", "", rawjson.Message(`{"items":["selected"]}`), nil, nil, "Selected",
		&agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("selected-page")}, time.Second, nil, nil)
	appendHistoricalHookEvent(t, rt.Store, result, "selected-result", 4)
	page, err := rt.Store.ListRunRecords(t.Context(), "result-run", "", 256)
	require.NoError(t, err)
	meta, err := rt.Store.LoadRun(t.Context(), "result-run")
	require.NoError(t, err)
	reader := selectedContinuationReader{
		runtime: rt,
		input: &resolvedPlanActivityInput{PlanActivityInput: &PlanActivityInput{
			AgentID: "svc.agent", RunID: "next-run", RunContext: run.Context{SessionID: "session-1"},
		}},
		specs: rt.toolSpecs, runs: map[string]struct{}{"result-run": {}},
		events: make(map[string]map[string]*canonicalToolEvents), seen: make(map[continuationOutputKey]struct{}),
	}
	err = reader.readRun(t.Context(), continuationHistoryFrame{
		meta: meta, ref: storage.HistoryPrefix{RunID: "result-run", EndID: page.Events[len(page.Events)-1].ID},
	})
	require.NoError(t, err)
	require.Len(t, reader.outputs, 1)
	assert.Equal(t, "selected-call", reader.outputs[0].ToolCallID)
	assert.Equal(t, "selected-page", *reader.outputs[0].Bounds.NextCursor)
	assert.NotContains(t, reader.runs, "call-run")
}

func TestSelectedContinuationRejectsUnownedOrMismatchedReference(t *testing.T) {
	for _, tc := range []struct {
		name           string
		callAgent      agent.Ident
		callSession    string
		resultSelected bool
		resultTool     string
		want           string
	}{
		{name: "unselected result", callAgent: "svc.agent", callSession: "session-1", want: "unselected run"},
		{name: "foreign agent", callAgent: "other.agent", callSession: "session-1", resultSelected: true, want: "owner"},
		{name: "foreign session", callAgent: "svc.agent", callSession: "other-session", resultSelected: true, want: "owner"},
		{name: "mismatched nonpaging tool", callAgent: "svc.agent", callSession: "session-1", resultSelected: true, resultTool: "tools.other", want: "tool result name"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, search, _ := continuationTestRuntime()
			callName := search.Name
			resultName := search.Name
			if tc.resultTool != "" {
				callName = "tools.nonpaging"
				resultName = tools.Ident(tc.resultTool)
			}
			callSubject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(tc.callAgent))}
			call := hooks.NewToolCallScheduledEvent("call-run", callSubject, tc.callSession, callName,
				"selected-call", rawjson.Message(`{"query":"selected"}`), "", "", 0)
			appendHistoricalHookEvent(t, rt.Store, call, "call", 1)
			resultSubject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}
			result := hooks.NewToolResultReceivedEvent("result-run", resultSubject, "session-1", "call-run",
				resultName, "selected-call", "", rawjson.Message(`{"items":["selected"]}`), nil, nil, "Selected", nil, time.Second, nil, nil)
			appendHistoricalHookEvent(t, rt.Store, result, "result", 2)
			reader := selectedContinuationReader{
				runtime: rt, input: &resolvedPlanActivityInput{PlanActivityInput: &PlanActivityInput{
					AgentID: "svc.agent", RunID: "next-run", RunContext: run.Context{SessionID: "session-1"},
				}}, specs: rt.toolSpecs, runs: make(map[string]struct{}),
				events: make(map[string]map[string]*canonicalToolEvents), seen: make(map[continuationOutputKey]struct{}),
			}
			if tc.resultSelected {
				reader.runs["result-run"] = struct{}{}
			}
			err := reader.addReference(t.Context(), &api.ToolOutputRef{CallRunID: "call-run", ResultRunID: "result-run", ToolCallID: "selected-call"})
			require.ErrorContains(t, err, tc.want)
			assert.Empty(t, reader.outputs)
		})
	}
}

func TestSelectedContinuationNextTurnFromLiteralRunPreservesHistory(t *testing.T) {
	store := newTestStore()
	rt := New(literalContinuationHistoryStore{Store: store})
	spec := newAnyJSONSpec("tools.confirm")
	oldMessages := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Inspect the selected group."}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "saved-call", Name: spec.Name.String(), Input: rawjson.Message(`{"group":"A"}`)}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{ToolUseID: "saved-call", Content: rawjson.Message(`{"approved":true}`)}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "The saved answer."}}},
	}
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}
	call := hooks.NewToolCallScheduledEvent("literal-predecessor", subject, "session-1", spec.Name,
		"saved-call", rawjson.Message(`{"group":"A"}`), "", "", 0)
	appendHistoricalHookEvent(t, store, call, "call", 1)
	result := hooks.NewToolResultReceivedEvent("literal-completed", subject, "session-1", "literal-predecessor",
		spec.Name, "saved-call", "", rawjson.Message(`{"approved":true}`), nil, nil, "Approved", nil, time.Second, nil, nil)
	appendHistoricalHookEvent(t, store, result, "result", 2)
	appendTestActivityHistory(t, rt, &PlanActivityInput{AgentID: "svc.agent", RunID: "literal-completed", RunContext: run.Context{SessionID: "session-1"}}, oldMessages)
	terminal := testHookRecord(t, hooks.NewRunCompletedEvent("literal-completed", subject, "session-1", "success", run.PhaseCompleted, nil, nil, nil), "terminal", time.Now())
	_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{RunID: "literal-completed", Status: session.RunStatusCompleted, Record: terminal})
	require.NoError(t, err)
	before, err := store.ListRunRecords(t.Context(), "literal-completed", "", 256)
	require.NoError(t, err)
	beforeBytes, err := json.Marshal(before)
	require.NoError(t, err)
	starts := 0
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition:      testAgentDefinition("svc.agent", "svc.workflow", "svc.queue", []tools.ToolSpec{spec}, nil),
		WorkflowHandler: rt.ExecuteWorkflow, PlanActivityName: "svc.plan", ResumeActivityName: "svc.resume", ExecuteToolActivity: "svc.execute",
		Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			starts++
			expected := append(append([]*model.Message(nil), oldMessages...), userMsg("Reply with a new answer."))
			expectedBytes, err := transcript.EncodeRunLogDelta(expected)
			require.NoError(t, err)
			actualBytes, err := transcript.EncodeRunLogDelta(input.Messages)
			require.NoError(t, err)
			assert.Equal(t, expectedBytes, actualBytes)
			return finalPlannerResult("A new answer."), nil
		}},
	}))
	client := rt.MustClient("svc.agent")
	prepared, err := client.PrepareNextTurn(t.Context(), "session-1", "literal-completed", nil,
		[]*model.Message{userMsg("Reply with a new answer.")}, WithRunID("next-turn"), WithTurnID("next-turn"))
	require.NoError(t, err)
	handle, err := client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	out, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, out.Final)
	assert.Equal(t, "A new answer.", out.Final.Text())
	assert.Equal(t, 1, starts)
	after, err := store.ListRunRecords(t.Context(), "literal-completed", "", 256)
	require.NoError(t, err)
	afterBytes, err := json.Marshal(after)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(beforeBytes), sha256.Sum256(afterBytes))
}

// LoadRun represents persisted runs whose history is stored in ordinary
// transcript records. New runs retain their published initial-history seed.
func (s literalContinuationHistoryStore) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	meta, err := s.Store.LoadRun(ctx, runID)
	if runID == "literal-predecessor" || runID == "literal-completed" {
		meta.SeedEndID = ""
	}
	return meta, err
}
