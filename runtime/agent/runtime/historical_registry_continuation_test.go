package runtime

// A new turn must use the registration retained by each saved query even when
// the current catalog offers the same names under a different registration.
// This fixture saves synthetic events, then uses the normal next-turn client.

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestHistoricalRegistryContinuationRetainsEachRegistration(t *testing.T) {
	store := newTestStore()
	rt := New(store)
	resolutions := 0
	require.NoError(t, rt.RegisterRegistry("company", &genregistry.Client{
		ResolveToolsetEndpoint: func(context.Context, any) (any, error) {
			resolutions++
			return testRegistryPagingResolution(strings.Repeat("c", 64)), nil
		},
	}, unusedRegistryPulse{}))
	definition := testRegistryAgentDefinition(testRegistrySources{})
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "records.plan", ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute",
		Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			assert.Len(t, historicalActions(input.Agent), 2)
			return finalPlannerResult("Saved pages remain."), nil
		}},
	}))
	admitRunForTest(t, store, session.RunMeta{AgentID: string(definition.route.ID), RunID: "saved", SessionID: "registry-history", Status: session.RunStatusRunning})
	messages := make([]*model.Message, 0, 4)
	for index, token := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		resolved, err := registrycontract.Resolve(testRegistryPagingResolution(token))
		require.NoError(t, err)
		binding, err := resolved.Select("company", "records.find")
		require.NoError(t, err)
		id := []string{"first-query", "second-query"}[index]
		call := ToolCall{
			Name: "records.find", ToolCallID: id, ModelToolCallID: "reused-provider",
			Registry: binding, Payload: rawjson.Message(`{"value":1}`),
		}
		appendHistoricalHookEvent(t, store, newToolCallScheduledEvent("saved", definition.route.ID, "registry-history", call, "", "", 0), id, int64(index*2+1))
		appendHistoricalHookEvent(t, store, hooks.NewToolResultReceivedEvent(
			"saved", definition.route.ID, "registry-history", "saved", call.Name, id, "",
			rawjson.Message(`{"value":1}`), nil, nil, "Saved result", &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("same-cursor")}, 0, nil, nil,
		), id+"-result", int64(index*2+2))
		messages = append(messages,
			&model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{
				ID: "reused-provider", Name: call.Name.String(), Input: call.Payload,
			}}},
			&model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
				ToolUseID: "reused-provider", Content: rawjson.Message(`{"value":1}`),
			}}})
	}
	appendTestActivityHistory(t, rt, &PlanActivityInput{
		AgentID: definition.route.ID, RunID: "saved", RunContext: run.Context{RunID: "saved", SessionID: "registry-history"},
	}, messages)
	meta, err := store.LoadRun(t.Context(), "saved")
	require.NoError(t, err)
	meta.Status = session.RunStatusCompleted
	admitRunForTest(t, store, meta)
	client := rt.MustClient(definition.route.ID)
	prepared, err := client.PrepareNextTurn(t.Context(), "registry-history", "saved", nil, nil, WithRunID("next"), WithTurnID("next"))
	require.NoError(t, err)
	handle, err := client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)

	page, err := store.ListRunRecords(t.Context(), "next", "", 1)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	input := &PlanActivityInput{AgentID: definition.route.ID, RunID: "next", HistoryEndID: page.Events[0].ID,
		RunContext: run.Context{RunID: "next", SessionID: "registry-history"}}
	resolvedInput, err := rt.resolvePlanActivityInput(t.Context(), input)
	require.NoError(t, err)
	actions, err := rt.continuationActionsForHistory(t.Context(), resolvedInput, nil, nil)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	for index, action := range actions {
		request := planner.ToolRequest{Name: action.modelName, ModelToolCallID: "selected", Payload: rawjson.Message(`{}`)}
		calls, err := rt.compilePlannerToolCallsForRun(input.RunContext, []planner.ToolRequest{request}, actions, map[string]model.ToolCall{
			"selected": {ID: "selected", Name: action.modelName, Payload: request.Payload},
		})
		require.NoError(t, err)
		require.Len(t, calls, 1)
		selected, err := registrycontract.Read(calls[0].Registry)
		require.NoError(t, err)
		assert.Equal(t, []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}[index], selected.Registered.RegistrationToken)
		assert.Equal(t, tools.Ident("records.continue"), calls[0].Name)
		assert.JSONEq(t, `{"cursor":"same-cursor"}`, string(calls[0].Payload))
		assert.Equal(t, []string{"first-query", "second-query"}[index], calls[0].ContinuationRootToolCallID)
	}
	readsBefore := resolutions
	request := &api.ContinuationActivityInput{AgentID: definition.route.ID, RunID: input.RunID, SessionID: "registry-history", HistoryEndID: input.HistoryEndID}
	available, err := rt.continuationAvailableActivity(t.Context(), request)
	require.NoError(t, err)
	assert.True(t, available)
	assert.Equal(t, readsBefore, resolutions, "availability must not resolve a replacement registry catalog")
	request.RestrictToTool = "records.find"
	available, err = rt.continuationAvailableActivity(t.Context(), request)
	require.NoError(t, err)
	assert.False(t, available, "availability uses the same tool restriction as planning")
	request.RestrictToTool = ""
	request.ToolOutputs = []*api.ToolOutputRef{nil}
	_, err = rt.continuationAvailableActivity(t.Context(), request)
	require.Error(t, err)
	request.ToolOutputs = nil

	// Legacy literal callers identify executions directly. A saved registration
	// must supply their paging contract to the availability read as it does to
	// Start, without fetching a replacement registry catalog.
	literalMessages := make([]*model.Message, 0, 4)
	for _, callID := range []string{"first-query", "second-query"} {
		literalMessages = append(literalMessages,
			&model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{
				ID: callID, Name: "records.find", Input: rawjson.Message(`{"value":1}`),
			}}},
			&model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
				ToolUseID: callID, Content: rawjson.Message(`{"value":1}`),
			}}})
	}
	_, err = client.Run(t.Context(), "registry-history", literalMessages, WithRunID("literal"), WithTurnID("literal"))
	require.NoError(t, err)
	literalPage, err := store.ListRunRecords(t.Context(), "literal", "", 1)
	require.NoError(t, err)
	require.Len(t, literalPage.Events, 1)
	readsBefore = resolutions
	available, err = rt.continuationAvailableActivity(t.Context(), &api.ContinuationActivityInput{
		AgentID: definition.route.ID, RunID: "literal", SessionID: "registry-history", HistoryEndID: literalPage.Events[0].ID,
	})
	require.NoError(t, err)
	assert.True(t, available)
	assert.Equal(t, readsBefore, resolutions)
	prepared, err = client.PrepareNextTurn(t.Context(), "registry-history", "literal", nil, nil,
		WithRunID("literal-next"), WithTurnID("literal-next"))
	require.NoError(t, err)
	handle, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)

	registration := rt.agents[definition.route.ID]
	registration.Planner = &stubPlanner{
		start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			assert.Empty(t, historicalActions(input.Agent))
			return finalPlannerResult("Finished."), nil
		},
		resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			assert.Empty(t, historicalActions(input.Agent))
			return finalPlannerResult("Finished."), nil
		},
	}
	rt.agents[definition.route.ID] = registration
	for _, synthesis := range []bool{false, true} {
		wire := *input
		wire.SynthesisOnly = synthesis
		if !synthesis {
			wire.Finalize = &planner.Termination{Reason: planner.TerminationReasonToolFailure}
		}
		_, err := rt.PlanStartActivity(t.Context(), &wire)
		require.NoError(t, err)
		_, err = rt.PlanResumeActivity(t.Context(), &wire)
		require.NoError(t, err)
	}
	registration.Definition.registryTools = nil
	rt.agents[definition.route.ID] = registration
	available, err = rt.continuationAvailableActivity(t.Context(), request)
	require.NoError(t, err)
	assert.False(t, available, "saved bindings cannot grant current consumption")
}
