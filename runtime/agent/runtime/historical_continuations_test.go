package runtime

import (
	"context"
	"encoding/json"
	"errors"
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
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestPlanStartPreservesHistoricalResultAfterContractChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bounds *agent.Bounds
	}{
		{name: "complete", bounds: &agent.Bounds{Returned: 1}},
		{name: "refinement only", bounds: &agent.Bounds{
			Returned: 1, Truncated: true, RefinementHint: "Narrow the query.",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			search, continuation := continuationTestSpecs()
			oldResult := rawjson.Message(`{"old_items":["recorded evidence"]}`)
			codecErr := errors.New("old_items is not in the current result contract")
			search.Result.Codec.FromJSON = func([]byte) (any, error) { return nil, codecErr }
			messages := []*model.Message{
				{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{
					ID: "source-1", Name: search.Name.String(), Input: rawjson.Message(`{"query":"records"}`),
				}}},
				{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
					ToolUseID: "source-1", Content: oldResult,
				}}},
				{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Tell me more."}}},
			}
			messageBytes, err := json.Marshal(messages)
			require.NoError(t, err)
			called := false
			pl := &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				called = true
				gotMessages, marshalErr := json.Marshal(input.Messages)
				require.NoError(t, marshalErr)
				assert.Equal(t, messageBytes, gotMessages)
				definitions := input.Agent.AdvertisedToolDefinitions()
				require.Len(t, definitions, 1)
				assert.Equal(t, search.Name.String(), definitions[0].Name)
				return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
					Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Answer."}},
				}}}, nil
			}}
			rt := newTestRuntimeWithPlanner("svc.agent", pl)
			seedTestToolSpecs(rt, search, continuation)
			seedTestToolDefinitions(rt, search, continuation)
			rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{"svc.agent": {search, continuation}}
			store := newTestStore()
			rt.Store = store
			call := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", search.Name,
				"source-1", rawjson.Message(`{"query":"records"}`), "", "", 0)
			result := hooks.NewToolResultReceivedEvent("run-1", "svc.agent", "session-1", "run-1",
				search.Name, "source-1", "", oldResult, nil, "Recorded evidence", tc.bounds, time.Second, nil, nil)
			appendHistoricalHookEvent(t, store, call, "source-call", 1)
			appendHistoricalHookEvent(t, store, result, "source-result", 2)
			before, err := store.ListSessionRunRecords(t.Context(), "session-1", "", 100)
			require.NoError(t, err)
			recordBytes, err := json.Marshal(before)
			require.NoError(t, err)
			_, err = rt.PlanStartActivity(t.Context(), &PlanActivityInput{
				AgentID: "svc.agent", RunID: "run-2", Messages: messages,
				RunContext: run.Context{RunID: "run-2", SessionID: "session-1"},
			})
			require.NoError(t, err)
			assert.True(t, called)
			after, err := store.ListSessionRunRecords(t.Context(), "session-1", "", 100)
			require.NoError(t, err)
			afterBytes, err := json.Marshal(after)
			require.NoError(t, err)
			assert.Equal(t, recordBytes, afterBytes)
			// The same old bytes are still rejected when actual work needs a typed result.
			events := &canonicalToolEvents{scheduled: call, result: result}
			_, err = rt.plannerToolOutputFromCanonicalEvents("run-1", "run-1", "source-1", events, events)
			assert.ErrorIs(t, err, codecErr)
		})
	}
}

func TestHistoricalContinuationRetiresCompletedChainAfterContractChange(t *testing.T) {
	rt, search, continuation := continuationTestRuntime()
	store := newTestStore()
	rt.Store = store
	continuation.Result.Codec.FromJSON = func([]byte) (any, error) {
		return nil, errors.New("old page shape")
	}
	rt.toolSpecs[continuation.Name] = continuation
	for index, callID := range []string{"source-1", "last-page"} {
		name := search.Name
		payload := rawjson.Message(`{"query":"records"}`)
		bounds := &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("page-2")}
		if index == 1 {
			name = continuation.Name
			payload = rawjson.Message(`{"cursor":"page-2"}`)
			bounds = &agent.Bounds{Returned: 1}
		}
		call := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", name,
			callID, payload, "", "", 0)
		if index == 1 {
			call.ContinuationRootToolCallID = "source-1"
		}
		appendHistoricalHookEvent(t, store, call, callID, int64(index*2+1))
		result := hooks.NewToolResultReceivedEvent("run-1", "svc.agent", "session-1", "run-1",
			name, callID, "", rawjson.Message(`{"old_items":["evidence"]}`), nil, "Page", bounds, time.Second, nil, nil)
		appendHistoricalHookEvent(t, store, result, callID+"-result", int64(index*2+2))
	}
	outputs, err := rt.loadHistoricalContinuationOutputs(t.Context(), &PlanActivityInput{
		AgentID: "svc.agent", RunContext: run.Context{SessionID: "session-1"},
		Messages: []*model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "source-1", Name: search.Name.String()},
			model.ToolUsePart{ID: "last-page", Name: continuationActionName(continuation.Name, "source-1").String()},
		}}},
	})
	require.NoError(t, err)
	require.Len(t, outputs, 2)
	actions, err := rt.availableContinuationActions("svc.agent", outputs)
	require.NoError(t, err)
	assert.Empty(t, actions)
}

func TestHistoricalContinuationRejectsDamagedMetadata(t *testing.T) {
	for _, tc := range []struct {
		name    string
		mutate  func(*hooks.ToolResultReceivedEvent)
		wantErr string
	}{
		{name: "correlation", mutate: func(result *hooks.ToolResultReceivedEvent) {
			result.CallRunID = "wrong-run"
		}, wantErr: "identity mismatch"},
		{name: "byte count", mutate: func(result *hooks.ToolResultReceivedEvent) {
			result.ResultBytes++
		}, wantErr: "size mismatch"},
		{name: "missing bounds", mutate: func(result *hooks.ToolResultReceivedEvent) {
			result.Bounds = nil
		}, wantErr: "without bounds"},
		{name: "empty cursor", mutate: func(result *hooks.ToolResultReceivedEvent) {
			result.Bounds.NextCursor = pointer("")
		}, wantErr: "empty next_cursor"},
		{name: "cursor without truncation", mutate: func(result *hooks.ToolResultReceivedEvent) {
			result.Bounds.NextCursor = pointer("page-2")
		}, wantErr: "without truncation"},
		{name: "truncation without next step", mutate: func(result *hooks.ToolResultReceivedEvent) {
			result.Bounds.Truncated = true
		}, wantErr: "without next_cursor or refinement_hint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt, search, _ := continuationTestRuntime()
			store := newTestStore()
			rt.Store = store
			call := hooks.NewToolCallScheduledEvent("run-1", "svc.agent", "session-1", search.Name,
				"source-1", rawjson.Message(`{"query":"records"}`), "", "", 0)
			result := hooks.NewToolResultReceivedEvent("run-1", "svc.agent", "session-1", "run-1",
				search.Name, "source-1", "", rawjson.Message(`{"items":[]}`), nil, "Page",
				&agent.Bounds{Returned: 0}, time.Second, nil, nil)
			tc.mutate(result)
			appendHistoricalHookEvent(t, store, call, "source-call", 1)
			appendHistoricalHookEvent(t, store, result, "source-result", 2)
			_, err := rt.loadHistoricalContinuationOutputs(t.Context(), &PlanActivityInput{
				AgentID: "svc.agent", RunContext: run.Context{SessionID: "session-1"},
				Messages: []*model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{
					model.ToolUsePart{ID: "source-1", Name: search.Name.String()},
				}}},
			})
			assert.ErrorContains(t, err, tc.wantErr)
		})
	}
}
