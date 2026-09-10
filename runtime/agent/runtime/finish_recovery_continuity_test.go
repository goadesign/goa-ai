package runtime

// These tests run the workflow, planner activity, and validated model client
// together. A rejected response must not reopen operations forbidden by an
// earlier finish failure, and fetching another page must not erase that failure.

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

const (
	finishExhaustedAfterPage = "exhausted after page"
	finishMixedCompletion    = "mixed completion"
	finishTerminalCorrection = "terminal correction"
)

func TestFinishRecoverySurvivesRejectedInvocationAndPages(t *testing.T) {
	for _, rejection := range []string{"unadvertised name", "invalid arguments", "planning output", "answer", finishExhaustedAfterPage, finishMixedCompletion, finishTerminalCorrection} {
		t.Run(rejection, func(t *testing.T) {
			testFinishRecoverySurvivesRejectedResponse(t, rejection)
		})
	}
}

func TestFinishRecoveryRejectsLegacyExpandedCatalogBeforeExecution(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	h := newRecoveryHarness(t, "legacy-finish-catalog", []tools.ToolSpec{search},
		func(context.Context, *ToolCall) (*planner.ToolResult, error) {
			t.Fatal("legacy expanded catalog must not execute new work")
			return nil, nil
		},
		nil,
	)
	state := &runLoopState{
		PendingRecovery: &pendingToolRecovery{
			outputs: []*planner.ToolOutput{recoveryOutput(search.Name, "failed-call", planner.RecoveryFinish)},
			catalog: &RecoveryCatalog{Tools: []tools.Ident{search.Name}},
		},
	}
	loop := newWorkflowLoop(h.runtime, h.workflow, h.registration, h.input, h.base, state, h.input.TurnID, nil, runDeadlines{}, h.registration.ResumeActivityOptions, h.registration.ExecuteToolActivityOptions)
	// Earlier workers could return this accepted ordinary-tool plan after a
	// finish-recovery response was rejected. Its recorded catalog admits the
	// name, but it cannot satisfy the run's retained finish restriction.
	result := &PlanResult{ToolCalls: []ToolCall{{Name: search.Name, ToolCallID: "new-operation", Payload: rawjson.Message(`{}`)}}}
	program, err := h.runtime.normalizePlanResultContract(result, "")
	require.NoError(t, err)
	_, err = loop.runStep(program)
	var rejected *planner.OutputContractError
	require.ErrorAs(t, err, &rejected)
	require.ErrorContains(t, rejected.Unwrap(), `finish recovery cannot start operation "catalog.search"`)
}

func testFinishRecoverySurvivesRejectedResponse(t *testing.T, rejection string) {
	t.Helper()
	search, continuation := continuationTestSpecs()
	load := newAnyJSONSpec("catalog.load")
	complete := newStrictRecoverySpec()
	complete.Name = "catalog.complete"
	complete.Bookkeeping = true
	complete.TerminalRun = true
	action := continuationActionName(continuation.Name, "search-call")
	var resumes, modelCalls, pages, submissions int
	var h *recoveryHarness
	h = newRecoveryHarness(t, "finish-continuity", []tools.ToolSpec{search, continuation, load, complete},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			switch call.Name {
			case search.Name:
				result := successfulToolResult(call)
				result.Bounds = &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer("page-2")}
				return result, nil
			case load.Name:
				return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID,
					Failure: testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "record service rejected the query")}, nil
			case continuation.Name:
				pages++
				assert.JSONEq(t, fmt.Sprintf(`{"cursor":"page-%d"}`, pages+1), string(call.Payload))
				result := successfulToolResult(call)
				result.Bounds = &agent.Bounds{Returned: 1, Truncated: true, NextCursor: pointer(fmt.Sprintf("page-%d", pages+2))}
				return result, nil
			case complete.Name:
				submissions++
				assert.Equal(t, string(planner.TerminationReasonToolFailure), call.Labels[FinalizationReasonLabel])
				if rejection == finishTerminalCorrection && submissions == 1 {
					return invalidCallResult(call), nil
				}
				return successfulToolResult(call), nil
			case tools.ToolUnavailable:
				return nil, errors.New("unavailable tool must not execute")
			default:
				return nil, fmt.Errorf("unexpected tool execution %q", call.Name)
			}
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			if rejection == finishExhaustedAfterPage && resumes == 4 {
				require.NotNil(t, input.Finalize)
				assert.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
				assert.NotContains(t, toolDefinitionNames(input.Agent.AdvertisedToolDefinitions()), action)
				return finalPlannerResult("replacement allowance exhausted"), nil
			}
			if resumes > 1 {
				if rejection == finishTerminalCorrection && resumes == 5 {
					assertAdvertisedTools(t, input, complete.Name)
					require.Len(t, input.Reminders, 2)
					assert.Contains(t, input.Reminders[0].Text+input.Reminders[1].Text, "query is invalid")
					assert.Contains(t, h.workflow.lastPlannerCall.Input.RecoveryToolCallIDs, "load-call")
				} else {
					assertAdvertisedTools(t, input, complete.Name, action)
					assert.Equal(t, []string{"load-call"}, h.workflow.lastPlannerCall.Input.RecoveryToolCallIDs)
				}
				require.NotNil(t, input.Finalize)
				assert.Equal(t, planner.TerminationReasonToolFailure, input.Finalize.Reason)
			}
			foundFailure := false
			for _, rem := range input.Reminders {
				if rem.ID == "tool_recovery.load-call" {
					assert.Contains(t, rem.Text, "record service rejected the query")
					foundFailure = true
				}
			}
			assert.True(t, foundFailure)
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{Model: "test", Tools: input.Agent.AdvertisedToolDefinitions()})
			if err != nil {
				return nil, err
			}
			if resumes == 1 && rejection == "planning output" {
				return nil, planner.NewRecoverableModelPlanningError(errors.New("submission is required"), &response.Content[0], "Choose a currently available action.")
			}
			if resumes == 1 && rejection == "answer" {
				return nil, planner.NewRecoverableModelAnswerError(errors.New("unsupported conclusion"), &planner.FinalResponse{Message: &response.Content[0]}, "State only supported findings.")
			}
			var requests []planner.ToolRequest
			for _, call := range response.ToolCalls() {
				request, err := planner.ToolRequestFromModelCall(call)
				if err != nil {
					return nil, err
				}
				requests = append(requests, request)
			}
			return &planner.PlanResult{ToolCalls: requests}, nil
		},
	)
	h.runtime.models["test"] = mustTestModelClient(stubModelClient{complete: func(context.Context, *model.Request) (*model.Response, error) {
		modelCalls++
		name := action
		payload := rawjson.Message(`{}`)
		switch modelCalls {
		case 1:
			switch rejection {
			case "unadvertised name", finishExhaustedAfterPage, finishMixedCompletion, finishTerminalCorrection:
				name = "catalog.unadvertised"
			case "invalid arguments":
				name = complete.Name
				payload = rawjson.Message(`{"query":42}`)
			case "planning output", "answer":
				return testModelResponse([]model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "unsupported conclusion"}}}}), nil
			}
		case 4, 5:
			name = complete.Name
			payload = rawjson.Message(`{"query":"accepted"}`)
		}
		if rejection == finishExhaustedAfterPage && modelCalls == 3 {
			name = "catalog.unadvertised"
		}
		if rejection == finishMixedCompletion && modelCalls == 4 {
			return testModelResponse(nil,
				model.ToolCall{ID: "submit", Name: complete.Name, Payload: payload},
				model.ToolCall{ID: "page", Name: action, Payload: rawjson.Message(`{}`)},
			), nil
		}
		return testModelResponse(nil, model.ToolCall{ID: fmt.Sprintf("model-%d", modelCalls), Name: name, Payload: payload}), nil
	}})
	recoveryTurns := 2
	if rejection == finishTerminalCorrection {
		recoveryTurns = 3
	}
	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: search.Name, ToolCallID: "search-call", Payload: rawjson.Message(`{"query":"reference"}`)},
		{Name: load.Name, ToolCallID: "load-call", Payload: rawjson.Message(`{"id":"one"}`)},
	}}, initialCaps(RunPolicy{MaxToolCalls: 8, MaxRecoveryTurns: recoveryTurns}))
	if rejection == finishMixedCompletion {
		require.ErrorContains(t, err, "cannot call budgeted tool")
		assert.Equal(t, 2, pages)
		assert.Zero(t, submissions)
		return
	}
	require.NoError(t, err)
	require.NotNil(t, out)
	if rejection == finishExhaustedAfterPage {
		assert.Equal(t, 3, modelCalls)
		assert.Equal(t, 1, pages)
		assert.Zero(t, submissions)
		return
	}
	if rejection == finishTerminalCorrection {
		assert.Equal(t, 5, modelCalls)
		assert.Equal(t, 2, pages)
		assert.Equal(t, 2, submissions)
		return
	}
	assert.Equal(t, 4, modelCalls)
	assert.Equal(t, 2, pages)
	assert.Equal(t, 1, submissions)
}
