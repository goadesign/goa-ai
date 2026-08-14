package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
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
