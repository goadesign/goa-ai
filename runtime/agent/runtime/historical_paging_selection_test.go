package runtime

// These tests start a new turn after a static paging tool changes. Old completed
// results remain readable; valid pending pages and current validation survive.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/schema"
)

func TestNextTurnAfterHistoricalPagingContractChange(t *testing.T) {
	for _, test := range []struct {
		name        string
		payload     string
		pages       int
		complete    bool
		wantAction  bool
		changedPage bool
	}{
		{name: "completed renamed pages", payload: `{"old_query":"records"}`, pages: 4, complete: true},
		{name: "incompatible unfinished query", payload: `{"old_query":"records"}`, pages: 1},
		{name: "removed unfinished page tool", payload: `{"query":"records"}`, pages: 2},
		{name: "compatible unfinished query", payload: `{"query":"records"}`, pages: 1, wantAction: true},
		{name: "source fits but next page changed", payload: `{"query":"records"}`, pages: 1, changedPage: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, spec := composedContinuationRuntime(t)
			specs := []tools.ToolSpec{spec}
			if test.changedPage {
				source, page := continuationTestSpecs()
				page.ExecutionPayloadSchema = rawjson.Message(`{"type":"object","properties":{"cursor":{"type":"string"},"tenant":{"type":"string"}},"required":["cursor","tenant"],"additionalProperties":false}`)
				var err error
				page.ExecutionPayloadCodec, err = schema.Codec(page.ExecutionPayloadSchema)
				require.NoError(t, err)
				spec = source
				specs = []tools.ToolSpec{source, page}
			}
			store := newTestStore()
			rt := New(store)
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
				Name: "tools", Specs: specs,
				Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					t.Errorf("history unexpectedly executed tool %q", call.Name)
					return successfulToolResult(call), nil
				}),
			}))
			plannerCalled := false
			require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
				Definition: NewAgentDefinition(
					AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"},
					specs, nil, nil, []tools.Ident{spec.Name}, nil, nil,
				),
				WorkflowHandler: rt.ExecuteWorkflow, PlanActivityName: "records.plan",
				ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute",
				Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
					plannerCalled = true
					wantActions := 0
					if test.wantAction {
						wantActions = 1
					}
					assert.Len(t, historicalActions(input.Agent), wantActions)
					assert.Contains(t, input.Messages[0].Parts, model.ToolUsePart{
						ID: "provider-0", Name: spec.Name.String(), Input: rawjson.Message(test.payload),
					})
					return finalPlannerResult("The saved evidence remains available."), nil
				}},
			}))
			subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("records.agent"))}
			messages := make([]*model.Message, 0, 2*test.pages)
			for page := range test.pages {
				id := string(rune('a' + page))
				provider := "provider-" + string(rune('0'+page))
				call := ToolCall{Name: spec.Name, ToolCallID: id, ModelToolCallID: provider, Payload: rawjson.Message(test.payload)}
				if page > 0 {
					call.Name = "tools.old_next_page"
					call.ContinuationRootToolCallID = "a"
					call.Payload = rawjson.Message(`{"old_cursor":"accepted-page"}`)
				}
				bounds := &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("more")}
				if test.complete && page == test.pages-1 {
					bounds = &agent.Bounds{Returned: 1}
				}
				appendHistoricalHookEvent(t, store, newToolCallScheduledEvent("saved", subject, "paging-session", call, "", "", 0), "call-"+id, int64(page*2+1))
				appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent(
					"saved", subject, "paging-session", "saved", call.Name, id, "",
					rawjson.Message(`{"items":["record"]}`), nil, nil, "Saved item", bounds, 0, nil, nil,
				), "result-"+id, int64(page*2+2))
				messages = append(messages,
					&model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: provider, Name: call.Name.String(), Input: call.Payload}}},
					&model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{ToolUseID: provider, Content: rawjson.Message(`{"items":["record"]}`)}}},
				)
			}
			appendTestActivityHistory(t, rt, &PlanActivityInput{
				AgentID: "records.agent", RunID: "saved", RunContext: run.Context{RunID: "saved", SessionID: "paging-session"},
			}, messages)
			meta, err := store.LoadRun(t.Context(), "saved")
			require.NoError(t, err)
			meta.Status = session.RunStatusCompleted
			admitRunForTest(t, store, meta)
			before, err := store.ListRunRecords(t.Context(), "saved", "", 100)
			require.NoError(t, err)
			beforeBytes, err := json.Marshal(before)
			require.NoError(t, err)
			client := rt.MustClient("records.agent")
			prepared, err := client.PrepareNextTurn(t.Context(), "paging-session", "saved", nil, nil, WithRunID("next"), WithTurnID("next"))
			require.NoError(t, err)
			handle, err := client.StartPrepared(t.Context(), prepared)
			require.NoError(t, err)
			_, err = handle.Wait(t.Context())
			require.NoError(t, err)
			assert.True(t, plannerCalled)
			after, err := store.ListRunRecords(t.Context(), "saved", "", 100)
			require.NoError(t, err)
			afterBytes, err := json.Marshal(after)
			require.NoError(t, err)
			assert.Equal(t, sha256.Sum256(beforeBytes), sha256.Sum256(afterBytes))
		})
	}
}

func TestHistoricalPagingSelectionKeepsIndependentAndCurrentWork(t *testing.T) {
	rt, spec := composedContinuationRuntime(t)
	first := sourceContinuationOutput(spec.Name, "first", `{"old_query":"records"}`, "shared")
	second := sourceContinuationOutput(spec.Name, "second", `{"query":"records"}`, "shared")
	closed := &planner.ToolOutput{Name: "tools.old_next_page", ToolCallID: "closed", ContinuationRootToolCallID: "first", Bounds: &agent.Bounds{Returned: 1}}
	outputs := []*planner.ToolOutput{first, second, closed}
	for _, output := range outputs {
		output.CallRunID, output.ResultRunID = "old", "old"
	}
	input := &resolvedPlanActivityInput{PlanActivityInput: &PlanActivityInput{RunID: "current"}}
	specs := map[tools.Ident]tools.ToolSpec{spec.Name: spec}
	selected, err := rt.selectHistoricalContinuations(input, outputs, nil, specs)
	require.NoError(t, err)
	actions, err := rt.availableContinuationActions("svc.agent", selected, false)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "second", actions[0].state.rootToolCallID)

	for _, test := range []struct {
		name, callRun, resultRun string
		explicit                 bool
	}{
		{name: "current schedule", callRun: "current", resultRun: "old"},
		{name: "current result", callRun: "old", resultRun: "current"},
		{name: "explicit checkpoint output", callRun: "old", resultRun: "old", explicit: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bad := sourceContinuationOutput(spec.Name, "bad", `{"old_query":"records"}`, "next")
			bad.CallRunID, bad.ResultRunID = test.callRun, test.resultRun
			var current []*planner.ToolOutput
			if test.explicit {
				current = []*planner.ToolOutput{bad}
			}
			selected, err := rt.selectHistoricalContinuations(input, []*planner.ToolOutput{bad}, current, specs)
			require.NoError(t, err)
			_, err = rt.availableContinuationActions("svc.agent", selected, false)
			assert.ErrorContains(t, err, "history input")
		})
	}

	page := &planner.ToolOutput{
		Name: spec.Name, ToolCallID: "new-page", CallRunID: "current", ResultRunID: "current",
		ContinuationRootToolCallID: "second", Payload: rawjson.Message(`{"query":"records","cursor":"wrong"}`),
		Bounds: &agent.Bounds{Returned: 1},
	}
	selected, err = rt.selectHistoricalContinuations(input, []*planner.ToolOutput{second, page}, nil, specs)
	require.NoError(t, err)
	_, err = rt.availableContinuationActions("svc.agent", selected, false)
	assert.ErrorContains(t, err, "wrong cursor")
}

func TestHistoricalPagingSelectionRetainsRegistrationAndRejectsBrokenRelationships(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	outputs := make([]*planner.ToolOutput, 0, 2)
	for index, token := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		resolved, err := registrycontract.Resolve(testRegistryPagingResolution(token))
		require.NoError(t, err)
		binding, err := resolved.Select("company", "records.find")
		require.NoError(t, err)
		output := sourceContinuationOutput("records.find", []string{"first", "second"}[index], `{"value":1}`, "shared")
		output.Registry, output.CallRunID, output.ResultRunID = binding, "old", "old"
		outputs = append(outputs, output)
	}
	closed := &planner.ToolOutput{
		Name: "records.continue", ToolCallID: "closed", CallRunID: "old", ResultRunID: "old",
		ContinuationRootToolCallID: "first", Registry: outputs[0].Registry,
		Bounds: &agent.Bounds{Returned: 1},
	}
	input := &resolvedPlanActivityInput{PlanActivityInput: &PlanActivityInput{RunID: "new"}}
	selected, err := rt.selectHistoricalContinuations(input, append(outputs, closed), nil, nil)
	require.NoError(t, err)
	actions, err := rt.availableContinuationActions(definition.route.ID, selected, false)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, "second", actions[0].state.rootToolCallID)
	retained, err := registrycontract.Read(actions[0].registry, nil)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("b", 64), retained.Registered.RegistrationToken)

	t.Run("different registration", func(t *testing.T) {
		other := *closed
		other.Registry = outputs[1].Registry
		_, err := rt.selectHistoricalContinuations(input, append(outputs, &other), nil, nil)
		assert.ErrorContains(t, err, "unknown source")
	})
	t.Run("missing source", func(t *testing.T) {
		_, err := rt.selectHistoricalContinuations(input, []*planner.ToolOutput{closed}, nil, nil)
		assert.ErrorContains(t, err, "unknown source")
	})
	t.Run("duplicate call", func(t *testing.T) {
		_, err := rt.selectHistoricalContinuations(input, []*planner.ToolOutput{outputs[0], outputs[0]}, nil, nil)
		assert.ErrorContains(t, err, "duplicate")
	})
}
