package runtime

// Recovery keeps permitted domain choices without borrowing unrelated global
// registrations. These tests run the owning activity and public continuation
// path, including the catalog saved when a recovery plan waits for input.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestCorrectCallChoicesRespectRunPolicy(t *testing.T) {
	for _, test := range []struct {
		name      string
		policy    *PolicyOverrides
		wantError bool
	}{
		{name: "caller restricts to correction", policy: &PolicyOverrides{RestrictToTool: "catalog.failed"}},
		{name: "allowed capability", policy: &PolicyOverrides{TagClauses: []TagPolicyClause{{AllowedAny: []string{"read"}}}}},
		{name: "explicit deny", policy: &PolicyOverrides{TagClauses: []TagPolicyClause{{DeniedAny: []string{"write"}}}}},
		{name: "correction denied", policy: &PolicyOverrides{TagClauses: []TagPolicyClause{{DeniedAny: []string{"read"}}}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			failed, other, unrelated := newAnyJSONSpec("catalog.failed"), newAnyJSONSpec("catalog.other"), newAnyJSONSpec("unrelated.global")
			failed.Tags, other.Tags = []string{"read"}, []string{"write"}
			var resumes int
			h := newRecoveryHarness(t, test.name, []tools.ToolSpec{failed, other, unrelated},
				func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					return invalidCallResult(call), nil
				},
				func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumes++
					assertAdvertisedTools(t, input, failed.Name)
					return finalPlannerResult("qualified answer"), nil
				})
			h.runtime.agentToolSpecs[h.input.AgentID] = []tools.ToolSpec{failed, other}
			h.workflow.plannerRoutes["resume"] = func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
				input.Policy = test.policy
				return h.runtime.PlanResumeActivity(ctx, input)
			}
			_, err := h.run(&PlanResult{ToolCalls: []ToolCall{{Name: failed.Name, ToolCallID: "failed", Payload: rawjson.Message(`{}`)}}}, initialCaps(RunPolicy{MaxToolCalls: 2}))
			if test.wantError {
				require.ErrorContains(t, err, "is excluded from advertised tools")
				assert.Zero(t, resumes)
			} else {
				require.NoError(t, err)
				assert.Equal(t, 1, resumes)
			}
		})
	}
}

func TestCorrectCallChoicesPreserveMixedFailureExclusions(t *testing.T) {
	failed, rejected, alternative := newAnyJSONSpec("catalog.failed"), newAnyJSONSpec("catalog.rejected"), newAnyJSONSpec("catalog.alternative")
	var resumes int
	h := newRecoveryHarness(t, "mixed-choices", []tools.ToolSpec{failed, rejected, alternative},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			result := invalidCallResult(call)
			if string(call.Payload) == `{"replan":true}` {
				result.Failure.Recovery.Action = planner.RecoveryReplan
			}
			return result, nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			assertAdvertisedTools(t, input, failed.Name, alternative.Name)
			require.Len(t, input.Reminders, 3)
			return finalPlannerResult("all failures retained"), nil
		})
	_, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: failed.Name, ToolCallID: "correct", Payload: rawjson.Message(`{}`)},
		{Name: failed.Name, ToolCallID: "same-replan", Payload: rawjson.Message(`{"replan":true}`)},
		{Name: rejected.Name, ToolCallID: "other-replan", Payload: rawjson.Message(`{"replan":true}`)},
	}}, initialCaps(RunPolicy{MaxToolCalls: 5}))
	require.NoError(t, err)
	assert.Equal(t, 1, resumes)
}

func TestCorrectCallChoicesRejectConflictingAgentContract(t *testing.T) {
	failed := newAnyJSONSpec("catalog.failed")
	var resumes int
	h := newRecoveryHarness(t, "conflicting-contract", []tools.ToolSpec{failed},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return invalidCallResult(call), nil
		},
		func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			return finalPlannerResult("unexpected"), nil
		})
	changed := cloneToolSpec(failed)
	changed.Description = "different contract"
	h.runtime.agentToolSpecs[h.input.AgentID] = []tools.ToolSpec{changed}
	_, err := h.run(&PlanResult{ToolCalls: []ToolCall{{Name: failed.Name, ToolCallID: "failed", Payload: rawjson.Message(`{}`)}}}, initialCaps(RunPolicy{MaxToolCalls: 2}))
	require.ErrorContains(t, err, "conflicting agent and executable contracts")
	assert.Zero(t, resumes)
}

func TestCorrectCallChoicesFinalizationRetainsOnlyFailedTerminal(t *testing.T) {
	terminal, other := newAnyJSONSpec("catalog.finish"), newAnyJSONSpec("catalog.lookup")
	terminal.TerminalRun, terminal.Bookkeeping = true, true
	var resumes int
	h := newRecoveryHarness(t, "finalizer-correction", []tools.ToolSpec{terminal, other},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return invalidCallResult(call), nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			require.NotNil(t, input.Finalize)
			assertAdvertisedTools(t, input, terminal.Name)
			return finalPlannerResult("finalization remains bounded"), nil
		})
	h.workflow.plannerRoutes["resume"] = func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
		input.Finalize = &planner.Termination{Reason: planner.TerminationReasonToolCap}
		return h.runtime.PlanResumeActivity(ctx, input)
	}
	_, err := h.run(&PlanResult{ToolCalls: []ToolCall{{Name: terminal.Name, ToolCallID: "failed-finish", Payload: rawjson.Message(`{}`)}}}, initialCaps(RunPolicy{MaxToolCalls: 2}))
	require.NoError(t, err)
	assert.Equal(t, 1, resumes)
}

func TestCorrectCallChoicesPublicContinuationRetainsAlternateAsk(t *testing.T) {
	failed, ask := newAnyJSONSpec("catalog.failed"), newAnyJSONSpec("catalog.ask")
	rt := New(newTestStore(), WithEngine(engineinmem.New()), WithLogger(telemetry.NoopLogger{}))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "catalog", Specs: []tools.ToolSpec{failed, ask},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			assert.Equal(t, failed.Name, call.Name)
			return invalidCallResult(call), nil
		}),
	}))
	var modelCalls, resumes int
	rt.models["test"] = mustTestModelClient(stubModelClient{complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
		modelCalls++
		assert.ElementsMatch(t, []tools.Ident{failed.Name, ask.Name}, toolDefinitionNames(request.Tools))
		name, id := failed.Name, "provider-failed"
		if modelCalls == 2 {
			name, id = ask.Name, "provider-ask"
		}
		return &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: id, Name: name.String(), Input: rawjson.Message(`{}`)},
		}}}, StopReason: "tool_use"}, nil
	}})
	p := &stubPlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			messages := input.Messages
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			summary, err := client.Complete(ctx, &model.Request{Model: "test", Messages: messages, Tools: input.Agent.AdvertisedToolDefinitions()})
			if err != nil {
				return nil, err
			}
			calls := summary.ToolCalls()
			require.Len(t, calls, 1)
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: calls[0].Name, ModelToolCallID: calls[0].ID, Payload: calls[0].Payload}}}, nil
		},
		resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			if resumes > 1 {
				return finalPlannerResult("answer used supplied evidence"), nil
			}
			messages := input.Messages
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			summary, err := client.Complete(ctx, &model.Request{Model: "test", Messages: messages, Tools: input.Agent.AdvertisedToolDefinitions()})
			if err != nil {
				return nil, err
			}
			calls := summary.ToolCalls()
			require.Len(t, calls, 1)
			call := calls[0]
			return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
				ID: "ask-input", Items: []planner.AwaitToolItem{{Name: call.Name, ModelToolCallID: call.ID, Payload: call.Payload}},
			}))}, nil
		},
	}
	require.NoError(t, rt.RegisterAgent(t.Context(), correctionTestRegistration(rt, p, []tools.ToolSpec{failed, ask})))
	_, err := createSessionForTest(t.Context(), rt.Store, "session-choices")
	require.NoError(t, err)
	client := rt.MustClient("catalog.agent")
	first, err := client.Run(t.Context(), "session-choices", []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Find the answer."}}}}, WithRunID("before-ask"), WithTurnID("turn-1"))
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	var checkpoint workflowCheckpoint
	require.NoError(t, json.Unmarshal(first.Suspension.Checkpoint, &checkpoint))
	require.Equal(t, &RecoveryCatalog{Tools: []tools.Ident{ask.Name, failed.Name}}, checkpoint.State.PendingRecoveryCatalog)
	awaited := first.Suspension.Pending[0].Await.ExternalTools.Items[0]
	removedAsk := rt.MustClientFor(testAgentDefinition("catalog.agent", "catalog.agent.workflow", "test", []tools.ToolSpec{failed}, nil))
	_, err = removedAsk.PrepareContinuation(t.Context(), "session-choices", "before-ask", "denied-ask", "turn-denied", &api.PendingInputResponse{
		ToolResults: &api.ToolResultsSet{ID: "ask-input", Results: []*api.ProvidedToolResult{{Name: ask.Name, ToolCallID: awaited.ToolCallID, Success: &api.ProvidedToolSuccess{Result: rawjson.Message(`{"answer":"observed"}`)}}}},
	}, WorkflowOptions{})
	require.ErrorIs(t, err, ErrContinuationRejected)
	require.ErrorContains(t, err, `requires tool "catalog.ask" removed from the current agent definition`)
	second, err := client.Continue(t.Context(), "session-choices", "before-ask", "after-ask", "turn-2", &api.PendingInputResponse{
		ToolResults: &api.ToolResultsSet{ID: "ask-input", Results: []*api.ProvidedToolResult{{
			Name: ask.Name, ToolCallID: awaited.ToolCallID, Success: &api.ProvidedToolSuccess{Result: rawjson.Message(`{"answer":"observed"}`)},
		}}},
	}, WorkflowOptions{})
	require.NoError(t, err)
	assert.Equal(t, "answer used supplied evidence", second.Final.Text())
	assert.Equal(t, 2, modelCalls)
	assert.Equal(t, 2, resumes)
}

func TestCorrectCallChoicesRetainBoundContinuation(t *testing.T) {
	failed := newAnyJSONSpec("catalog.failed")
	search, continuation := continuationTestSpecs()
	var resumes, continued int
	h := newRecoveryHarness(t, "continuation-choice", []tools.ToolSpec{failed, search, continuation},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			switch call.Name.String() {
			case failed.Name.String():
				return invalidCallResult(call), nil
			case search.Name.String():
				return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"items": []string{"first"}}, Bounds: &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("saved-next")}}, nil
			case continuation.Name.String():
				continued++
				assert.JSONEq(t, `{"cursor":"saved-next"}`, string(call.Payload))
				return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"items": []string{"last"}}, Bounds: &agent.Bounds{Returned: 1, Truncated: false}}, nil
			default:
				t.Fatalf("unexpected tool %s", call.Name)
				return nil, nil
			}
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			if resumes > 1 {
				return finalPlannerResult("remaining page read"), nil
			}
			definitions := input.Agent.AdvertisedToolDefinitions()
			require.Len(t, definitions, 3)
			var chosen *model.ToolDefinition
			for _, definition := range definitions {
				if strings.HasPrefix(definition.Name, continuationToolNamePrefix) {
					chosen = definition
				}
			}
			require.NotNil(t, chosen)
			assert.True(t, chosen.NoArguments)
			assert.NotContains(t, chosen.Description, "saved-next")
			messages := input.Messages
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{Model: "test", Messages: messages, Tools: []*model.ToolDefinition{chosen}})
			if err != nil {
				return nil, err
			}
			calls := response.ToolCalls()
			require.Len(t, calls, 1)
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: calls[0].Name, ModelToolCallID: calls[0].ID, Payload: calls[0].Payload}}}, nil
		})
	h.runtime.models["test"] = mustTestModelClient(stubModelClient{complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
		require.Len(t, request.Tools, 1) // The planner's request-specific filtering remains intact.
		return &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "provider-page", Name: request.Tools[0].Name, Input: rawjson.Message(`{}`)}}}}, StopReason: "tool_use"}, nil
	}})
	_, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: failed.Name, ToolCallID: "failed", Payload: rawjson.Message(`{}`)},
		{Name: search.Name, ToolCallID: "source", Payload: rawjson.Message(`{"query":"records"}`)},
	}}, initialCaps(RunPolicy{MaxToolCalls: 4}))
	require.NoError(t, err)
	assert.Equal(t, 1, continued)
	assert.Equal(t, 2, resumes)
}
