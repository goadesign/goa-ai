package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestBindContinuationCursorFromCanonicalHistory(t *testing.T) {
	t.Parallel()

	const cursor = "opaque-next-page"
	search, continuation := continuationTestSpecs()
	rt := &Runtime{toolSpecs: map[tools.Ident]tools.ToolSpec{
		search.Name:       search,
		continuation.Name: continuation,
	}}
	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    continuation.Name,
		Payload: rawjson.Message(`{}`),
	}}}
	outputs := []*planner.ToolOutput{{
		Name: search.Name,
		Bounds: &agent.Bounds{
			Truncated:  true,
			NextCursor: pointer(cursor),
		},
	}}

	err := rt.bindContinuationCursors(result, outputs)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(result.ToolCalls[0].ModelPayload))
	assert.JSONEq(t, `{"cursor":"opaque-next-page"}`, string(result.ToolCalls[0].Payload))
}

func TestBindContinuationRetainsCanonicalQueryPayload(t *testing.T) {
	t.Parallel()

	search, continuation := continuationTestSpecs()
	continuation.Bounds.Paging.ReplayPayload = true
	rt := &Runtime{toolSpecs: map[tools.Ident]tools.ToolSpec{
		search.Name:       search,
		continuation.Name: continuation,
	}}
	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    continuation.Name,
		Payload: rawjson.Message(`{}`),
	}}}
	cursor := "second-page"
	outputs := []*planner.ToolOutput{{
		Name:    search.Name,
		Payload: rawjson.Message(`{"query":"alarms","limit":10}`),
		Bounds:  &agent.Bounds{Truncated: true, NextCursor: &cursor},
	}}

	err := rt.bindContinuationCursors(result, outputs)
	require.NoError(t, err)
	assert.JSONEq(t, `{}`, string(result.ToolCalls[0].ModelPayload))
	assert.JSONEq(t, `{"query":"alarms","limit":10,"cursor":"second-page"}`, string(result.ToolCalls[0].Payload))
}

func TestContinuationAvailabilityRejectsMultipleCompatibleResults(t *testing.T) {
	t.Parallel()

	search, continuation := continuationTestSpecs()
	rt := &Runtime{
		toolSpecs: map[tools.Ident]tools.ToolSpec{
			search.Name:       search,
			continuation.Name: continuation,
		},
		agentToolSpecs: map[agent.Ident][]tools.ToolSpec{
			"svc.agent": {search, continuation},
		},
	}
	firstCursor := "first"
	outputs := []*planner.ToolOutput{{
		Name:   search.Name,
		Bounds: &agent.Bounds{Truncated: true, NextCursor: &firstCursor},
	}, {
		Name:   continuation.Name,
		Bounds: &agent.Bounds{Truncated: false},
	}}

	_, err := rt.availableContinuationTools("svc.agent", outputs)
	assert.ErrorContains(t, err, "multiple compatible preceding pages")
}

func TestBindContinuationRejectsMultipleCallsForOneChain(t *testing.T) {
	t.Parallel()

	search, continuation := continuationTestSpecs()
	rt := &Runtime{toolSpecs: map[tools.Ident]tools.ToolSpec{
		search.Name:       search,
		continuation.Name: continuation,
	}}
	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{
		{Name: search.Name, Payload: rawjson.Message(`{"query":"first"}`)},
		{Name: search.Name, Payload: rawjson.Message(`{"query":"second"}`)},
	}}

	err := rt.bindContinuationCursors(result, nil)
	assert.ErrorContains(t, err, "cannot include multiple calls in one planner result")
}

func TestContinuationToolIsAdvertisedOnlyWhenAvailable(t *testing.T) {
	t.Parallel()

	search, continuation := continuationTestSpecs()
	rt := &Runtime{agentToolSpecs: map[agent.Ident][]tools.ToolSpec{
		"svc.agent": {search, continuation},
	}}
	hidden := &simplePlannerContext{rt: rt, agent: "svc.agent"}
	visible := &simplePlannerContext{
		rt:                     rt,
		agent:                  "svc.agent",
		availableContinuations: map[tools.Ident]struct{}{continuation.Name: {}},
	}

	assert.Len(t, hidden.AdvertisedToolDefinitions(), 1)
	assert.Len(t, visible.AdvertisedToolDefinitions(), 2)
}

func TestBindContinuationRejectsModelArguments(t *testing.T) {
	t.Parallel()

	search, continuation := continuationTestSpecs()
	rt := &Runtime{toolSpecs: map[tools.Ident]tools.ToolSpec{
		search.Name:       search,
		continuation.Name: continuation,
	}}
	cursor := "next"
	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    continuation.Name,
		Payload: rawjson.Message(`{"cursor":"model-authored"}`),
	}}}

	err := rt.bindContinuationCursors(result, []*planner.ToolOutput{{
		Name:   search.Name,
		Bounds: &agent.Bounds{Truncated: true, NextCursor: &cursor},
	}})
	assert.ErrorContains(t, err, "unknown field \"cursor\"")
}

func continuationTestSpecs() (tools.ToolSpec, tools.ToolSpec) {
	search := newAnyJSONSpec("tools.search", "svc.tools")
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

func pointer[T any](value T) *T {
	return &value
}
