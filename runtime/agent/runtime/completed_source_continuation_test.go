package runtime

// These tests retain completed source evidence whose old input no longer fits
// today's paging contract. Active sources and terminal pages remain validated.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCompletedSourceHistorySkipsCurrentInputCodec(t *testing.T) {
	for _, test := range []struct {
		name    string
		bounds  *agent.Bounds
		callID  string
		wantErr string
	}{
		{name: "without bounds", callID: "source"},
		{name: "complete", callID: "source", bounds: &agent.Bounds{Returned: 1}},
		{name: "truncated preview", callID: "source", bounds: &agent.Bounds{Returned: 1, Truncated: true, RefinementHint: "Narrow the query."}},
		{name: "missing source identity", bounds: &agent.Bounds{Returned: 1}, wantErr: "empty tool call id"},
		{name: "active incompatible source", callID: "source", bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("next")}, wantErr: "history input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt, spec := composedContinuationRuntime(t)
			payload := rawjson.Message(`{"legacy_query":"records"}`)
			_, err := spec.ExecutionPayloadCodec.FromJSON(payload)
			require.Error(t, err, "the old input must not fit the current contract")
			output := &planner.ToolOutput{Name: spec.Name, ToolCallID: test.callID, Payload: payload, Bounds: test.bounds}
			before, err := json.Marshal(output)
			require.NoError(t, err)
			actions, err := rt.availableContinuationActions("svc.agent", []*planner.ToolOutput{output}, false)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
			} else {
				require.NoError(t, err)
				assert.Empty(t, actions)
			}
			after, err := json.Marshal(output)
			require.NoError(t, err)
			assert.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
		})
	}
}

func TestCompletedComposedPageHistoryStillValidatesCorrelation(t *testing.T) {
	for _, test := range []struct {
		name, root, payload, wantErr string
	}{
		{name: "wrong terminal cursor", root: "source", payload: `{"query":"records","cursor":"wrong"}`, wantErr: "wrong cursor"},
		{name: "missing source correlation", payload: `{"query":"records","cursor":"next"}`, wantErr: "no source tool call id"},
		{name: "unknown source correlation", root: "unknown", payload: `{"query":"records","cursor":"next"}`, wantErr: "unknown source tool call"},
		{name: "source input with page correlation", root: "source", payload: `{"query":"records"}`, wantErr: "unexpectedly names a prior source call"},
		{name: "invalid terminal input", root: "source", payload: `{"cursor":"next"}`, wantErr: "history input"},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt, spec := composedContinuationRuntime(t)
			outputs := []*planner.ToolOutput{
				sourceContinuationOutput(spec.Name, "source", `{"query":"records"}`, "next"),
				{Name: spec.Name, ToolCallID: "page", ModelToolCallID: "model-page", ContinuationRootToolCallID: test.root, Payload: rawjson.Message(test.payload), Bounds: &agent.Bounds{Returned: 1}},
			}
			_, err := rt.availableContinuationActions("svc.agent", outputs, false)
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestCompletedSourcePrepareNextTurnPreservesOldInput(t *testing.T) {
	_, spec := composedContinuationRuntime(t)
	store := newTestStore()
	rt := New(store)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tools", Specs: []tools.ToolSpec{spec},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			t.Errorf("completed history unexpectedly executed tool %q", call.Name)
			return successfulToolResult(call), nil
		}),
	}))
	plannerCalled := false
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: NewAgentDefinition(
			AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"},
			[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, nil, nil,
		),
		WorkflowHandler: rt.ExecuteWorkflow, PlanActivityName: "records.plan",
		ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute",
		Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			plannerCalled = true
			assert.Empty(t, historicalActions(input.Agent))
			assert.Contains(t, input.Messages[0].Parts, model.ToolUsePart{
				ID: "old-provider", Name: spec.Name.String(), Input: rawjson.Message(`{"legacy_query":"records"}`),
			})
			return finalPlannerResult("Saved evidence is available."), nil
		}},
	}))
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("records.agent"))}
	payload := rawjson.Message(`{"legacy_query":"records"}`)
	_, err := spec.ExecutionPayloadCodec.FromJSON(payload)
	require.Error(t, err)
	call := ToolCall{Name: spec.Name, ToolCallID: "old-source", ModelToolCallID: "old-provider", Payload: payload}
	appendHistoricalHookEvent(t, store, newToolCallScheduledEvent("saved", subject, "completed-source", call, "", "", 0), "call", 1)
	appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent(
		"saved", subject, "completed-source", "saved", spec.Name, call.ToolCallID, "",
		rawjson.Message(`{"items":["record"]}`), nil, nil, "Saved preview",
		&agent.Bounds{Returned: 1, Truncated: true, RefinementHint: "Narrow the query."}, 0, nil, nil,
	), "result", 2)
	appendTestActivityHistory(t, rt, &PlanActivityInput{
		AgentID: "records.agent", RunID: "saved", RunContext: run.Context{RunID: "saved", SessionID: "completed-source"},
	}, []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "old-provider", Name: spec.Name.String(), Input: payload}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{ToolUseID: "old-provider", Content: rawjson.Message(`{"items":["record"]}`)}}},
	})
	meta, err := store.LoadRun(t.Context(), "saved")
	require.NoError(t, err)
	meta.Status = session.RunStatusCompleted
	admitRunForTest(t, store, meta)
	before, err := store.ListRunRecords(t.Context(), "saved", "", 100)
	require.NoError(t, err)
	require.Empty(t, before.NextCursor)
	beforeBytes, err := json.Marshal(before)
	require.NoError(t, err)
	client := rt.MustClient("records.agent")
	prepared, err := client.PrepareNextTurn(t.Context(), "completed-source", "saved", nil, nil, WithRunID("next"), WithTurnID("next"))
	require.NoError(t, err)
	handle, err := client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	assert.True(t, plannerCalled)
	after, err := store.ListRunRecords(t.Context(), "saved", "", 100)
	require.NoError(t, err)
	require.Empty(t, after.NextCursor)
	afterBytes, err := json.Marshal(after)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(beforeBytes), sha256.Sum256(afterBytes))
}
