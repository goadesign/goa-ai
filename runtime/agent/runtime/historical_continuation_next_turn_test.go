package runtime

// This test starts two ordinary turns against the in-memory engine and store.
// A local provider returns fixed tool calls so saved history must restore the
// remaining page without changing provider IDs or reading an external service.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestHistoricalContinuationPrepareNextTurnPreservesProviderIdentity(t *testing.T) {
	search, continuation := continuationTestSpecs()
	store := storageinmem.New()
	rt := New(store, WithLogger(telemetry.NoopLogger{}))
	_, err := createSessionForTest(t.Context(), store, "history-session")
	require.NoError(t, err)

	// Each provider response uses the same ID. Its lifetime is one response;
	// the runtime creates a separate execution ID for each selected call.
	provider, err := model.NewClient(stubModelClient{
		complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
			name, payload := search.Name, rawjson.Message(`{"query":"records"}`)
			for _, definition := range request.Tools {
				if IsGeneratedContinuationToolName(tools.Ident(definition.Name)) {
					name, payload = tools.Ident(definition.Name), rawjson.Message(`{}`)
				}
			}
			return &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.ToolUsePart{
						ID: "provider-call", Name: name.String(), Input: payload,
					}},
				}},
				StopReason: "tool_use",
			}, nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, rt.RegisterModel("local-script", provider))

	var rootID, pageID string
	executions := 0
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tools", Specs: []tools.ToolSpec{search, continuation},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			bounds := &agent.Bounds{Returned: 1}
			if call.Name == search.Name {
				rootID = call.ToolCallID
				assert.NotEqual(t, "provider-call", rootID)
				bounds.Truncated = true
				bounds.NextCursor = pointer("page-two")
			} else {
				assert.Equal(t, continuation.Name, call.Name)
				pageID = call.ToolCallID
				assert.JSONEq(t, `{"cursor":"page-two"}`, string(call.Payload))
			}
			return &planner.ToolResult{
				Name: call.Name, ToolCallID: call.ToolCallID,
				Result: map[string]any{"items": []string{"record"}}, Bounds: bounds,
			}, nil
		}),
	}))
	starts := 0
	registration := AgentRegistration{
		Definition: NewAgentDefinition(
			AgentRoute{ID: "history.agent", WorkflowName: "history.workflow", DefaultTaskQueue: "history.queue"},
			[]tools.ToolSpec{search, continuation}, nil, nil,
			[]tools.Ident{search.Name, continuation.Name}, nil, nil,
		),
		WorkflowHandler:  rt.ExecuteWorkflow,
		PlanActivityName: "history.plan", ResumeActivityName: "history.resume", ExecuteToolActivity: "history.execute",
		Planner: &stubPlanner{
			start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				starts++
				if starts == 2 {
					var actions []tools.Ident
					for _, definition := range input.Agent.AdvertisedToolDefinitions() {
						if IsGeneratedContinuationToolName(tools.Ident(definition.Name)) {
							actions = append(actions, tools.Ident(definition.Name))
						}
					}
					if !assert.Equal(t, []tools.Ident{continuationActionName(continuation.Name, rootID)}, actions) {
						return nil, fmt.Errorf("next turn lost the saved continuation")
					}
				}
				client, ok := input.Agent.PlannerModelClient("local-script")
				if !ok {
					return nil, fmt.Errorf("local provider is not registered")
				}
				response, err := client.Complete(ctx, &model.Request{
					Model: "local-script", Tools: input.Agent.AdvertisedToolDefinitions(),
				})
				if err != nil {
					return nil, err
				}
				calls := response.ToolCalls()
				if len(calls) != 1 {
					return nil, fmt.Errorf("local provider returned %d calls", len(calls))
				}
				request, err := planner.ToolRequestFromModelCall(calls[0])
				if err != nil {
					return nil, err
				}
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{request}}, nil
			},
			resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				for _, output := range input.ToolOutputs {
					assert.Equal(t, "provider-call", output.ModelToolCallID)
					assert.NotEqual(t, output.ModelToolCallID, output.ToolCallID)
					if output.Name == continuation.Name {
						assert.Equal(t, rootID, output.ContinuationRootToolCallID)
					}
				}
				return finalPlannerResult("Page received."), nil
			},
		},
	}
	require.NoError(t, rt.RegisterAgent(t.Context(), registration))
	client := rt.MustClient("history.agent")
	_, err = client.Run(t.Context(), "history-session", nil, WithRunID("first-turn"), WithTurnID("first"))
	require.NoError(t, err)
	history, err := transcript.BuildMessagesFromRunLog(t.Context(), store, "first-turn")
	require.NoError(t, err)
	require.NoError(t, transcript.ValidatePlannerTranscript(history))
	require.NotEmpty(t, history)
	call, ok := history[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	assert.Equal(t, "provider-call", call.ID)
	before, err := store.ListRunRecords(t.Context(), "first-turn", "", 256)
	require.NoError(t, err)
	require.Empty(t, before.NextCursor)
	beforeBytes, err := json.Marshal(before)
	require.NoError(t, err)

	// The next turn inherits the exact saved source through the normal client
	// entry. It must execute the retained cursor and leave source records intact.
	prepared, err := client.PrepareNextTurn(t.Context(), "history-session", "first-turn", nil,
		[]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Continue."}}}},
		WithRunID("second-turn"), WithTurnID("second"), WithoutPriorReasoning())
	require.NoError(t, err)
	handle, err := client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	assert.Equal(t, 2, starts)
	assert.Equal(t, 2, executions)
	secondRecords, err := store.ListRunRecords(t.Context(), "second-turn", "", 256)
	require.NoError(t, err)
	require.Empty(t, secondRecords.NextCursor)
	var savedRoot string
	for _, event := range secondRecords.Events {
		if event.Type != hooks.ToolCallScheduled {
			continue
		}
		call, err := decodeToolCallScheduledRunlogEvent(event)
		require.NoError(t, err)
		if call.ToolCallID == pageID {
			savedRoot = call.ContinuationRootToolCallID
		}
	}
	assert.Equal(t, rootID, savedRoot)
	after, err := store.ListRunRecords(t.Context(), "first-turn", "", 256)
	require.NoError(t, err)
	require.Empty(t, after.NextCursor)
	afterBytes, err := json.Marshal(after)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(beforeBytes), sha256.Sum256(afterBytes))
}
