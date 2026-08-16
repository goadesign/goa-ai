package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestContinuationActionBindsExactChainWithoutExposingCursor(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	outputs := []*planner.ToolOutput{sourceContinuationOutput(
		search.Name,
		"source-1",
		`{"query":"alarms","limit":10,"injected":"secret"}`,
		"opaque-next-page",
	)}

	actions, err := rt.availableContinuationActions("svc.agent", outputs)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Contains(t, actions[0].description, `{"limit":10,"query":"alarms"}`)
	assert.NotContains(t, actions[0].description, "secret")
	assert.NotContains(t, actions[0].description, "opaque-next-page")

	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    actions[0].modelName,
		Payload: rawjson.Message(`{}`),
	}}}
	require.NoError(t, rt.bindContinuationCursors(result, actions))
	call := result.ToolCalls[0]
	assert.Equal(t, continuation.Name, call.Name)
	assert.Equal(t, actions[0].modelName, call.ModelName)
	assert.Equal(t, "source-1", call.ContinuationRootToolCallID)
	assert.JSONEq(t, `{}`, string(call.ModelPayload))
	assert.JSONEq(t, `{"cursor":"opaque-next-page"}`, string(call.Payload))
}

func TestHistoricalContinuationRehydratesExactLatestPage(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	store := runloginmem.New()
	rt.RunEventStore = store
	const (
		sessionID = "session-1"
		agentID   = "svc.agent"
	)
	sourceCall := hooks.NewToolCallScheduledEvent(
		"run-1",
		agentID,
		sessionID,
		search.Name,
		"source-1",
		rawjson.Message(`{"query":"alarms"}`),
		"",
		"",
		0,
	)
	appendHistoricalHookEvent(t, store, sourceCall, "source-call", 1)
	firstCursor := "first"
	appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent(
		"run-1",
		agentID,
		sessionID,
		"run-1",
		search.Name,
		"source-1",
		"",
		rawjson.Message(`{"items":["page-1"]}`),
		len(`{"items":["page-1"]}`),
		false,
		"",
		nil,
		"page 1",
		&agent.Bounds{Returned: 1, Truncated: true, NextCursor: &firstCursor},
		time.Second,
		nil,
		nil,
	), "source-result", 2)

	continueCall := hooks.NewToolCallScheduledEvent(
		"run-1",
		agentID,
		sessionID,
		continuation.Name,
		"continue-1",
		rawjson.Message(`{"cursor":"first"}`),
		"",
		"",
		0,
	)
	continueCall.ContinuationRootToolCallID = "source-1"
	appendHistoricalHookEvent(t, store, continueCall, "continue-call", 3)
	secondCursor := "second"
	appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent(
		"run-1",
		agentID,
		sessionID,
		"run-1",
		continuation.Name,
		"continue-1",
		"",
		rawjson.Message(`{"items":["page-2"]}`),
		len(`{"items":["page-2"]}`),
		false,
		"",
		nil,
		"page 2",
		&agent.Bounds{Returned: 1, Truncated: true, NextCursor: &secondCursor},
		time.Second,
		nil,
		nil,
	), "continue-result", 4)

	input := &PlanActivityInput{
		AgentID: agentID,
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "source-1",
					Name:  search.Name.String(),
					Input: rawjson.Message(`{"query":"alarms"}`),
				},
				model.ToolUsePart{
					ID:    "continue-1",
					Name:  continuationActionName(continuation.Name, "source-1").String(),
					Input: rawjson.Message(`{}`),
				},
			},
		}},
		RunContext: run.Context{
			SessionID: sessionID,
		},
	}

	outputs, err := rt.loadHistoricalContinuationOutputs(t.Context(), input)
	require.NoError(t, err)
	require.Len(t, outputs, 2)
	actions, err := rt.availableContinuationActions(agentID, outputs)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Equal(t, continuationActionName(continuation.Name, "source-1"), actions[0].modelName)
	assert.NotContains(t, actions[0].description, firstCursor)
	assert.NotContains(t, actions[0].description, secondCursor)

	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    actions[0].modelName,
		Payload: rawjson.Message(`{}`),
	}}}
	require.NoError(t, rt.bindContinuationCursors(result, actions))
	require.Len(t, result.ToolCalls, 1)
	assert.Equal(t, continuation.Name, result.ToolCalls[0].Name)
	assert.Equal(t, "source-1", result.ToolCalls[0].ContinuationRootToolCallID)
	assert.JSONEq(t, `{"cursor":"second"}`, string(result.ToolCalls[0].Payload))
}

func TestContinuationActionRetainsCanonicalQueryPayload(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	continuation.Bounds.Paging.ReplayPayload = true
	rt.toolSpecs[continuation.Name] = continuation
	rt.agentToolSpecs["svc.agent"][1] = continuation
	outputs := []*planner.ToolOutput{sourceContinuationOutput(
		search.Name,
		"source-1",
		`{"query":"alarms","limit":10}`,
		"second-page",
	)}
	actions, err := rt.availableContinuationActions("svc.agent", outputs)
	require.NoError(t, err)
	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    actions[0].modelName,
		Payload: rawjson.Message(`{}`),
	}}}

	require.NoError(t, rt.bindContinuationCursors(result, actions))
	assert.JSONEq(t, `{"query":"alarms","limit":10,"cursor":"second-page"}`, string(result.ToolCalls[0].Payload))
}

func TestContinuationActionSupportsSourceWithoutModelFields(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	search.Payload.FieldJSONTypes = nil
	rt.toolSpecs[search.Name] = search
	rt.agentToolSpecs["svc.agent"][0] = search
	actions, err := rt.availableContinuationActions("svc.agent", []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"injected":"secret"}`, "next"),
	})

	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Contains(t, actions[0].description, "original input {}")
	assert.NotContains(t, actions[0].description, "secret")
	assert.Equal(t, continuation.Name, actions[0].spec.Name)
}

func TestAutomaticContinuationAdvancesOnlyEmptyLiveChains(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	empty := sourceContinuationOutput(search.Name, "source-empty", `{"query":"may"}`, "may-next")
	empty.Bounds.Returned = 0
	outputs := []*planner.ToolOutput{
		empty,
		sourceContinuationOutput(search.Name, "source-data", `{"query":"june"}`, "june-next"),
	}
	actions, err := rt.availableContinuationActions("svc.agent", outputs)
	require.NoError(t, err)

	result, automatic, err := rt.automaticContinuationPlan(actions)
	require.NoError(t, err)
	require.True(t, automatic)
	require.Len(t, result.ToolCalls, 1)
	call := result.ToolCalls[0]
	assert.Equal(t, continuation.Name, call.Name)
	assert.Empty(t, call.ModelName)
	assert.Equal(t, "source-empty", call.ContinuationRootToolCallID)
	assert.JSONEq(t, `{"cursor":"may-next"}`, string(call.Payload))
}

func TestAutomaticContinuationLeavesNonEmptyPagesForModelDecision(t *testing.T) {
	t.Parallel()

	rt, search, _ := continuationTestRuntime()
	actions, err := rt.availableContinuationActions("svc.agent", []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"query":"alarms"}`, "next"),
	})
	require.NoError(t, err)

	result, automatic, err := rt.automaticContinuationPlan(actions)
	require.NoError(t, err)
	assert.False(t, automatic)
	assert.Nil(t, result)
}

func TestContinuationActionNameStaysStableAsChainAdvances(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	outputs := make([]*planner.ToolOutput, 0, 2)
	outputs = append(outputs, sourceContinuationOutput(
		search.Name,
		"source-1",
		`{"query":"alarms"}`,
		"first",
	))
	first, err := rt.availableContinuationActions("svc.agent", outputs)
	require.NoError(t, err)

	outputs = append(outputs, &planner.ToolOutput{
		Name:                       continuation.Name,
		ToolCallID:                 "continue-1",
		ContinuationRootToolCallID: "source-1",
		Payload:                    rawjson.Message(`{"cursor":"first"}`),
		Bounds:                     &agent.Bounds{Returned: 10, Truncated: true, NextCursor: pointer("second")},
	})
	second, err := rt.availableContinuationActions("svc.agent", outputs)
	require.NoError(t, err)
	require.Len(t, second, 1)
	assert.Equal(t, first[0].modelName, second[0].modelName)

	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    second[0].modelName,
		Payload: rawjson.Message(`{}`),
	}}}
	require.NoError(t, rt.bindContinuationCursors(result, second))
	assert.JSONEq(t, `{"cursor":"second"}`, string(result.ToolCalls[0].Payload))
}

func TestGeneratedContinuationToolNameFormat(t *testing.T) {
	t.Parallel()

	require.True(t, IsGeneratedContinuationToolName(
		continuationActionName("tools.continue_search", "source-1"),
	))
	for _, name := range []tools.Ident{
		"continue_",
		"continue_search",
		"continue_9ce1cab750fa5b0523c1363",
		"continue_9ce1cab750fa5b0523c13632f",
		"continue_9ce1cab750fa5b0523c1363z",
		"continue_9CE1CAB750FA5B0523C13632",
		"tools.continue_search",
		"continued_search",
	} {
		assert.False(t, IsGeneratedContinuationToolName(name), name)
	}
}

func TestContinuationActionsKeepParallelChainsIndependent(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	outputs := []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"query":"may"}`, "same-cursor"),
		sourceContinuationOutput(search.Name, "source-2", `{"query":"june"}`, "same-cursor"),
	}
	actions, err := rt.availableContinuationActions("svc.agent", outputs)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	assert.NotEqual(t, actions[0].modelName, actions[1].modelName)

	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{
		{Name: actions[0].modelName, Payload: rawjson.Message(`{}`)},
		{Name: actions[1].modelName, Payload: rawjson.Message(`{}`)},
	}}
	require.NoError(t, rt.bindContinuationCursors(result, actions))
	assert.Equal(t, continuation.Name, result.ToolCalls[0].Name)
	assert.Equal(t, "source-1", result.ToolCalls[0].ContinuationRootToolCallID)
	assert.Equal(t, "source-2", result.ToolCalls[1].ContinuationRootToolCallID)
}

func TestContinuationCompletionRemovesOnlyCompletedChain(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	outputs := []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"query":"may"}`, "may-next"),
		sourceContinuationOutput(search.Name, "source-2", `{"query":"june"}`, "june-next"),
		{
			Name:                       continuation.Name,
			ToolCallID:                 "continue-1",
			ContinuationRootToolCallID: "source-1",
			Payload:                    rawjson.Message(`{"cursor":"may-next"}`),
			Bounds:                     &agent.Bounds{Truncated: false},
		},
	}

	actions, err := rt.availableContinuationActions("svc.agent", outputs)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.Contains(t, actions[0].description, `"june"`)
}

func TestContinuationHistoryRejectsMissingCorrelation(t *testing.T) {
	t.Parallel()

	rt, _, continuation := continuationTestRuntime()
	outputs := []*planner.ToolOutput{{
		Name:       continuation.Name,
		ToolCallID: "continue-1",
		Payload:    rawjson.Message(`{"cursor":"first"}`),
		Bounds:     &agent.Bounds{Truncated: false},
	}}

	_, err := rt.availableContinuationActions("svc.agent", outputs)
	assert.ErrorContains(t, err, "history has no source tool call id")
}

func TestContinuationHistoryRejectsWrongCursor(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	outputs := []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"query":"alarms"}`, "first"),
		{
			Name:                       continuation.Name,
			ToolCallID:                 "continue-1",
			ContinuationRootToolCallID: "source-1",
			Payload:                    rawjson.Message(`{"cursor":"other"}`),
			Bounds:                     &agent.Bounds{Truncated: false},
		},
	}

	_, err := rt.availableContinuationActions("svc.agent", outputs)
	assert.ErrorContains(t, err, "with the wrong cursor")
}

func TestContinuationHistoryRejectsCursorWithoutProgress(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	outputs := []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"query":"alarms"}`, "same"),
		{
			Name:                       continuation.Name,
			ToolCallID:                 "continue-1",
			ContinuationRootToolCallID: "source-1",
			Payload:                    rawjson.Message(`{"cursor":"same"}`),
			Bounds:                     &agent.Bounds{Truncated: true, NextCursor: pointer("same")},
		},
	}

	_, err := rt.availableContinuationActions("svc.agent", outputs)
	assert.ErrorContains(t, err, "did not advance")
}

func TestPlannerToolOutputHydrationPreservesContinuationRoot(t *testing.T) {
	t.Parallel()

	scheduled := hooks.NewToolCallScheduledEvent(
		"run-1",
		"agent-1",
		"session-1",
		tools.Ident("tools.continue_search"),
		"continue-1",
		rawjson.Message(`{"cursor":"next"}`),
		"tools",
		"",
		0,
	)
	scheduled.ContinuationRootToolCallID = "source-1"
	events := &canonicalToolEvents{
		scheduled: scheduled,
		result: &hooks.ToolResultReceivedEvent{
			ToolName:    scheduled.ToolName,
			ToolCallID:  scheduled.ToolCallID,
			ResultJSON:  rawjson.Message(`{}`),
			ResultBytes: 2,
		},
	}
	output, err := plannerToolOutputFromCanonicalEvents("run-1", "run-1", "continue-1", events, events)
	require.NoError(t, err)
	assert.Equal(t, "source-1", output.ContinuationRootToolCallID)
}

func TestBindContinuationRejectsDuplicateActionCalls(t *testing.T) {
	t.Parallel()

	rt, search, _ := continuationTestRuntime()
	actions, err := rt.availableContinuationActions("svc.agent", []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"query":"alarms"}`, "next"),
	})
	require.NoError(t, err)
	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{
		{Name: actions[0].modelName, Payload: rawjson.Message(`{}`)},
		{Name: actions[0].modelName, Payload: rawjson.Message(`{}`)},
	}}

	err = rt.bindContinuationCursors(result, actions)
	assert.ErrorContains(t, err, "cannot be called more than once")
}

func TestContinuationActionsAreAdvertisedInsteadOfCanonicalTool(t *testing.T) {
	t.Parallel()

	rt, search, _ := continuationTestRuntime()
	actions, err := rt.availableContinuationActions("svc.agent", []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"query":"alarms"}`, "next"),
	})
	require.NoError(t, err)
	hidden := &simplePlannerContext{rt: rt, agent: "svc.agent"}
	visible := &simplePlannerContext{rt: rt, agent: "svc.agent", continuationActions: actions}

	require.Len(t, hidden.AdvertisedToolDefinitions(), 1)
	require.Len(t, visible.AdvertisedToolDefinitions(), 2)
	assert.Equal(t, actions[0].modelName.String(), visible.AdvertisedToolDefinitions()[1].Name)
}

func TestBindContinuationRejectsCanonicalToolAndModelArguments(t *testing.T) {
	t.Parallel()

	rt, search, continuation := continuationTestRuntime()
	actions, err := rt.availableContinuationActions("svc.agent", []*planner.ToolOutput{
		sourceContinuationOutput(search.Name, "source-1", `{"query":"alarms"}`, "next"),
	})
	require.NoError(t, err)

	canonical := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    continuation.Name,
		Payload: rawjson.Message(`{}`),
	}}}
	require.ErrorContains(t, rt.bindContinuationCursors(canonical, actions), "is not model-callable")

	withArguments := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    actions[0].modelName,
		Payload: rawjson.Message(`{"cursor":"model-authored"}`),
	}}}
	assert.ErrorContains(t, rt.bindContinuationCursors(withArguments, actions), `unknown field "cursor"`)
}

func continuationTestSpecs() (tools.ToolSpec, tools.ToolSpec) {
	search := newAnyJSONSpec("tools.search", "svc.tools")
	search.Payload.FieldJSONTypes = map[string]string{
		"limit": "integer",
		"query": "string",
	}
	search.Bounds = &tools.BoundsSpec{Paging: &tools.PagingSpec{
		ContinueTool:    "tools.continue_search",
		CursorField:     "cursor",
		NextCursorField: "next_cursor",
	}}
	continuation := newAnyJSONSpec("tools.continue_search", "svc.tools")
	continuation.Bounds = &tools.BoundsSpec{Paging: &tools.PagingSpec{
		ContinueTool:    continuation.Name,
		SourceTool:      search.Name,
		CursorField:     "cursor",
		NextCursorField: "next_cursor",
	}}
	return search, continuation
}

func continuationTestRuntime() (*Runtime, tools.ToolSpec, tools.ToolSpec) {
	search, continuation := continuationTestSpecs()
	return &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			search.Name:       search,
			continuation.Name: continuation,
		},
		agentToolSpecs: map[agent.Ident][]tools.ToolSpec{
			"svc.agent": {search, continuation},
		},
	}, search, continuation
}

func sourceContinuationOutput(
	name tools.Ident,
	toolCallID string,
	payload string,
	cursor string,
) *planner.ToolOutput {
	return &planner.ToolOutput{
		Name:       name,
		ToolCallID: toolCallID,
		Payload:    rawjson.Message(payload),
		Bounds: &agent.Bounds{
			Returned:   10,
			Truncated:  true,
			NextCursor: pointer(cursor),
		},
	}
}

func pointer[T any](value T) *T {
	return &value
}

// appendHistoricalHookEvent records one canonical event in the session log used
// by the cross-run continuation reconstruction test.
func appendHistoricalHookEvent(
	t *testing.T,
	store runlog.Store,
	event hooks.Event,
	eventKey string,
	timestampMS int64,
) {
	t.Helper()
	input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{
		TurnID:      "turn-1",
		EventKey:    eventKey,
		TimestampMS: timestampMS,
	})
	require.NoError(t, err)
	_, err = store.Append(context.Background(), &runlog.Event{
		EventKey:  input.EventKey,
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   input.Payload,
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	})
	require.NoError(t, err)
}
