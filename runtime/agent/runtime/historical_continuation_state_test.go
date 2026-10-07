package runtime

// These native runs preserve independent saved queries through new turns,
// pending confirmations, automatic pages and a sibling's finish failure.
// All provider responses and tools are local synthetic fixtures.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	singleRecordContinuationStore struct {
		storage.Store
	}
)

// ListRunRecords forces schedules and results onto separate read pages while
// leaving the runtime's requested page limit and source bounds unchanged.
func (s singleRecordContinuationStore) ListRunRecords(ctx context.Context, runID, after string, limit int) (runlog.Page, error) {
	return s.Store.ListRunRecords(ctx, runID, after, min(limit, 1))
}

func TestHistoricalContinuationIndependentChainsAndFinish(t *testing.T) {
	for _, checkpointed := range []bool{false, true} {
		t.Run(fmt.Sprintf("checkpoint=%t", checkpointed), func(t *testing.T) {
			testHistoricalContinuationChains(t, checkpointed)
		})
	}
}

func testHistoricalContinuationChains(t *testing.T, checkpointed bool) {
	t.Helper()
	search, continuation := continuationTestSpecs()
	failed, confirmation := newAnyJSONSpec("tools.failed"), newAnyJSONSpec("tools.confirm")
	specs := []tools.ToolSpec{search, continuation, failed, confirmation}
	store := storageinmem.New()
	rt := New(singleRecordContinuationStore{store}, WithLogger(telemetry.NoopLogger{}), WithToolConfirmation(&ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{confirmation.Name: {
			Prompt:       func(context.Context, *ToolCall) (string, error) { return "Proceed?", nil },
			DeniedResult: func(context.Context, *ToolCall) (any, error) { return map[string]any{"done": false}, nil },
		}},
	}))
	_, err := createSessionForTest(t.Context(), store, "chains")
	require.NoError(t, err)
	var mu sync.Mutex
	pages, searches, approvals := 0, 0, 0
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tools", Specs: specs,
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			mu.Lock()
			defer mu.Unlock()
			result := successfulToolResult(call)
			switch call.Name {
			case search.Name:
				searches++
				result.Bounds = &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("next-page")}
				if searches == 1 {
					result.Bounds = &agent.Bounds{Returned: 0, Truncated: true, NextCursor: pointer("first-page")}
				}
			case continuation.Name:
				pages++
				if pages == 1 {
					assert.JSONEq(t, `{"cursor":"first-page"}`, string(call.Payload))
				} else {
					assert.JSONEq(t, `{"cursor":"next-page"}`, string(call.Payload))
				}
				result.Bounds = &agent.Bounds{Returned: 1}
				if pages == 1 {
					result.Bounds.Truncated = true
					result.Bounds.NextCursor = pointer("next-page")
				}
			case failed.Name:
				result.Result = nil
				result.Failure = testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "synthetic failure")
			case confirmation.Name:
				approvals++
			case tools.ToolUnavailable:
				return nil, fmt.Errorf("unavailable tool reached executor")
			default:
				return nil, fmt.Errorf("unexpected tool %q", call.Name)
			}
			return result, nil
		}),
	}))
	var requested []model.ToolCall
	require.NoError(t, rt.RegisterModel("local", mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse(nil, requested...), nil
		},
	})))
	selectCalls := func(ctx context.Context, owner planner.PlannerContext, calls ...model.ToolCall) (*planner.PlanResult, error) {
		requested = calls
		client, ok := owner.PlannerModelClient("local")
		if !ok {
			return nil, fmt.Errorf("local model is not registered")
		}
		response, err := client.Complete(ctx, &model.Request{Model: "local", Tools: owner.AdvertisedToolDefinitions()})
		if err != nil {
			return nil, err
		}
		result := &planner.PlanResult{}
		for _, call := range response.ToolCalls() {
			request, err := planner.ToolRequestFromModelCall(call)
			if err != nil {
				return nil, err
			}
			result.ToolCalls = append(result.ToolCalls, request)
		}
		return result, nil
	}
	var roots []string
	starts, recoveryResumes := 0, 0
	definition := NewAgentDefinition(
		AgentRoute{ID: "chains.agent", WorkflowName: "chains.workflow", DefaultTaskQueue: "chains.queue"},
		specs, nil, nil, []tools.Ident{search.Name, continuation.Name, failed.Name, confirmation.Name}, nil, nil,
	)
	require.NoError(t, rt.RegisterAgent(t.Context(), AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "chains.plan", ResumeActivityName: "chains.resume", ExecuteToolActivity: "chains.execute",
		Planner: &stubPlanner{
			start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				starts++
				switch starts {
				case 1:
					calls := []model.ToolCall{
						{ID: "reused", Name: search.Name, Payload: rawjson.Message(`{"query":"records"}`)},
						{ID: "sibling", Name: search.Name, Payload: rawjson.Message(`{"query":"records"}`)},
					}
					if checkpointed {
						calls = append(calls,
							model.ToolCall{ID: "confirm-one", Name: confirmation.Name, Payload: rawjson.Message(`{}`)},
							model.ToolCall{ID: "confirm-two", Name: confirmation.Name, Payload: rawjson.Message(`{}`)})
					}
					return selectCalls(ctx, input.Agent, calls...)
				case 2:
					assert.Equal(t, []tools.Ident{continuationActionName(continuation.Name, roots[0]), continuationActionName(continuation.Name, roots[1])}, historicalActions(input.Agent))
					return selectCalls(ctx, input.Agent,
						model.ToolCall{ID: "reused", Name: continuationActionName(continuation.Name, roots[0]), Payload: rawjson.Message(`{}`)},
						model.ToolCall{ID: "sibling", Name: failed.Name, Payload: rawjson.Message(`{}`)})
				default:
					assert.Empty(t, historicalActions(input.Agent))
					return finalPlannerResult("No pages remain."), nil
				}
			},
			resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				if starts == 1 {
					return finalPlannerResult("Saved both queries."), nil
				}
				recoveryResumes++
				require.NotNil(t, input.Finalize)
				assert.Equal(t, planner.TerminationReasonToolFailure, input.Finalize.Reason)
				require.Len(t, input.Reminders, 1)
				assert.Contains(t, input.Reminders[0].Text, "synthetic failure")
				if recoveryResumes == 1 {
					assert.Equal(t, []tools.Ident{continuationActionName(continuation.Name, roots[1])}, historicalActions(input.Agent))
					assert.Len(t, input.ToolOutputs, 2, "inherited roots must not become current outputs")
					return selectCalls(ctx, input.Agent, model.ToolCall{
						ID: "reused", Name: continuationActionName(continuation.Name, roots[1]), Payload: rawjson.Message(`{}`),
					})
				}
				assert.Empty(t, historicalActions(input.Agent))
				require.Len(t, input.ToolOutputs, 3)
				assert.Equal(t, roots[1], input.ToolOutputs[2].ContinuationRootToolCallID)
				return finalPlannerResult("All pages received."), nil
			},
		},
	}))
	client := rt.MustClient("chains.agent")
	first, err := client.Run(t.Context(), "chains", nil, WithRunID("roots"), WithTurnID("roots"))
	require.NoError(t, err)
	source := "roots"
	if checkpointed {
		for index := 0; index < 2; index++ {
			require.NotNil(t, first.Suspension)
			next := fmt.Sprintf("approved-%d", index)
			first, err = client.Continue(t.Context(), "chains", source, next, next,
				&api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{
					ID: first.Suspension.Pending[0].Confirmation.ID, Approved: true, RequestedBy: "operator",
				}}, WorkflowOptions{})
			require.NoError(t, err)
			source = next
		}
		assert.Equal(t, 2, approvals)
	}
	require.Nil(t, first.Suspension)
	assert.Equal(t, 1, pages, "the source run advances its empty page without a model decision")
	sourceRecords, err := store.ListRunRecords(t.Context(), source, "", 256)
	require.NoError(t, err)
	automaticPages := 0
	for _, event := range sourceRecords.Events {
		if event.Type != hooks.ToolCallScheduled {
			continue
		}
		call, err := decodeToolCallScheduledRunlogEvent(event)
		require.NoError(t, err)
		if call.ToolName == continuation.Name {
			automaticPages++
			assert.Empty(t, call.ModelToolCallID)
			assert.NotEmpty(t, call.ContinuationRootToolCallID)
		}
	}
	assert.Equal(t, 1, automaticPages)
	rootRecords, err := store.ListRunRecords(t.Context(), "roots", "", 256)
	require.NoError(t, err)
	for _, event := range rootRecords.Events {
		if event.Type == hooks.ToolCallScheduled {
			call, err := decodeToolCallScheduledRunlogEvent(event)
			require.NoError(t, err)
			if call.ToolName == search.Name {
				roots = append(roots, call.ToolCallID)
			}
		}
	}
	require.Len(t, roots, 2)
	assert.NotEqual(t, roots[0], roots[1])
	before, err := json.Marshal(rootRecords)
	require.NoError(t, err)
	prepared, err := client.PrepareNextTurn(t.Context(), "chains", source, nil, nil, WithRunID("finish-pages"), WithTurnID("finish-pages"))
	require.NoError(t, err)
	handle, err := client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	out, err := handle.Wait(t.Context())
	require.NoError(t, err)
	assert.Equal(t, "All pages received.", out.Final.Text())
	assert.Equal(t, 3, out.ToolCount, "historical roots and automatic pages must not spend the new run's tool budget")
	assert.Equal(t, 2, searches)
	assert.Equal(t, 3, pages)
	assert.Equal(t, 2, recoveryResumes)

	// Re-read the exact start position after later pages finished. The excluded
	// suffix must not retire either query in that earlier selected history.
	startPage, err := store.ListRunRecords(t.Context(), "finish-pages", "", 1)
	require.NoError(t, err)
	require.Len(t, startPage.Events, 1)
	available, err := rt.continuationAvailableActivity(t.Context(), &api.ContinuationActivityInput{
		AgentID: "chains.agent", RunID: "finish-pages", SessionID: "chains", HistoryEndID: startPage.Events[0].ID,
	})
	require.NoError(t, err)
	assert.True(t, available)
	_, err = rt.continuationAvailableActivity(t.Context(), &api.ContinuationActivityInput{
		AgentID: "chains.agent", RunID: "finish-pages", SessionID: "foreign", HistoryEndID: startPage.Events[0].ID,
	})
	assert.True(t, engine.IsActivityErrorNonRetryable(err))
	prepared, err = client.PrepareNextTurn(t.Context(), "chains", "finish-pages", nil, nil, WithRunID("closed"), WithTurnID("closed"))
	require.NoError(t, err)
	handle, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	afterRecords, err := store.ListRunRecords(t.Context(), "roots", "", 256)
	require.NoError(t, err)
	after, err := json.Marshal(afterRecords)
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
}

func historicalActions(owner planner.PlannerContext) []tools.Ident {
	var actions []tools.Ident
	for _, definition := range owner.AdvertisedToolDefinitions() {
		name := tools.Ident(definition.Name)
		if IsGeneratedContinuationToolName(name) {
			actions = append(actions, name)
		}
	}
	return actions
}
