package runtime

// workflow_recovery_contract_test.go verifies that failed tool calls and
// rejected model invocations provide bounded correction evidence without
// dictating the planner's semantic next action.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

type recoveryHarness struct {
	runtime      *Runtime
	registration AgentRegistration
	workflow     *routeWorkflowContext
	input        *RunInput
	base         *planner.PlanInput
}

func TestRunLoopCombinesFailedCallsIntoFewerCorrections(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	list := newAnyJSONSpec("catalog.list")
	var resumes int
	h := newRecoveryHarness(
		t,
		"combine",
		[]tools.ToolSpec{search, list},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if string(call.Payload) != `{"query":"combined-a"}` &&
				string(call.Payload) != `{"query":"combined-b"}` {
				return invalidCallResult(call), nil
			}
			return successfulToolResult(call), nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1:
				assertAdvertisedTools(t, input, search.Name)
				require.Len(t, input.Reminders, 4)
				return &planner.PlanResult{
					ToolCalls: []planner.ToolRequest{
						{Name: search.Name, Payload: rawjson.Message(`{"query":"combined-a"}`)},
						{Name: search.Name, Payload: rawjson.Message(`{"query":"combined-b"}`)},
					},
					SynthesizeAfterTools: true,
				}, nil
			case 2:
				require.True(t, input.SynthesisOnly)
				assert.Empty(t, input.Reminders)
				return finalPlannerResult("combined"), nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		},
	)

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{ToolCallID: "bad-call-1", Name: search.Name, Payload: rawjson.Message(`{"query":"bad-1"}`)},
		{ToolCallID: "bad-call-2", Name: search.Name, Payload: rawjson.Message(`{"query":"bad-2"}`)},
		{ToolCallID: "bad-call-3", Name: search.Name, Payload: rawjson.Message(`{"query":"bad-3"}`)},
		{ToolCallID: "bad-call-4", Name: search.Name, Payload: rawjson.Message(`{"query":"bad-4"}`)},
	}}, initialCaps(RunPolicy{MaxToolCalls: 10}))

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "combined", out.Final.Text())
	assert.Equal(t, 2, resumes)
}

func TestRunLoopCorrectionMayRetryFailedToolOrAnswer(t *testing.T) {
	tests := []struct {
		name           string
		choose         func(search tools.ToolSpec) *planner.PlanResult
		wantToolEvents int
		wantAnswer     string
	}{
		{
			name: "retry failed tool",
			choose: func(search tools.ToolSpec) *planner.PlanResult {
				return &planner.PlanResult{
					ToolCalls: []planner.ToolRequest{{
						Name:    search.Name,
						Payload: rawjson.Message(`{"query":"corrected"}`),
					}},
					SynthesizeAfterTools: true,
				}
			},
			wantToolEvents: 2,
			wantAnswer:     "corrected",
		},
		{
			name: "final answer",
			choose: func(_ tools.ToolSpec) *planner.PlanResult {
				return finalPlannerResult("provisional")
			},
			wantToolEvents: 1,
			wantAnswer:     "provisional",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := newAnyJSONSpec("catalog.search")
			list := newAnyJSONSpec("catalog.list")
			resumes := 0
			h := newRecoveryHarness(
				t,
				"choice-"+tt.name,
				[]tools.ToolSpec{search, list},
				func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					if string(call.Payload) == `{"query":"bad"}` {
						return invalidCallResult(call), nil
					}
					return successfulToolResult(call), nil
				},
				func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumes++
					if resumes == 1 {
						assertAdvertisedTools(t, input, search.Name)
						require.Len(t, input.Reminders, 1)
						return tt.choose(search), nil
					}
					require.True(t, input.SynthesisOnly)
					return finalPlannerResult(tt.wantAnswer), nil
				},
			)

			out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
				ToolCallID: "bad-call",
				Name:       search.Name,
				Payload:    rawjson.Message(`{"query":"bad"}`),
			}}}, initialCaps(RunPolicy{MaxToolCalls: 4}))

			require.NoError(t, err)
			require.NotNil(t, out)
			assert.Equal(t, tt.wantAnswer, out.Final.Text())
			assert.Len(t, out.ToolEvents, tt.wantToolEvents)
		})
	}
}

func TestRunLoopPreservesCorrectionEvidenceAcrossClarification(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	list := newAnyJSONSpec("catalog.list")
	var resumes int
	h := newRecoveryHarness(
		t,
		"clarification",
		[]tools.ToolSpec{search, list},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if string(call.Payload) == `{"query":"bad"}` {
				return invalidCallResult(call), nil
			}
			return successfulToolResult(call), nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1:
				assertAdvertisedTools(t, input, search.Name)
				require.Len(t, input.Reminders, 1)
				return &planner.PlanResult{Await: planner.NewAwait(
					planner.AwaitClarificationItem(&planner.AwaitClarification{
						ID:            "clarify-query",
						Question:      "What should the search query be?",
						MissingFields: []string{"query"},
					}),
				)}, nil
			case 2:
				assertAdvertisedTools(t, input, search.Name)
				require.Len(t, input.Reminders, 1)
				return &planner.PlanResult{
					ToolCalls: []planner.ToolRequest{{
						Name:    search.Name,
						Payload: rawjson.Message(`{"query":"good"}`),
					}},
					SynthesizeAfterTools: true,
				}, nil
			case 3:
				require.True(t, input.SynthesisOnly)
				return finalPlannerResult("clarified"), nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		},
	)
	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "bad-call",
		Name:       search.Name,
		Payload:    rawjson.Message(`{"query":"bad"}`),
	}}}, initialCaps(RunPolicy{MaxToolCalls: 4}))

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
}

func TestRunLoopInvalidCallReachesFailureFinalization(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	var recoveryTurns int
	h := newRecoveryHarness(
		t,
		"failure-cap",
		[]tools.ToolSpec{search},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if string(call.Payload) == `{"query":"still-bad"}` {
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: testToolFailure(
						planner.FailureDomainRejection,
						planner.RecoveryReplan,
						"choose another action",
					),
				}, nil
			}
			return invalidCallResult(call), nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			if input.Finalize != nil {
				require.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
				return finalPlannerResult("stopped after repeated failures"), nil
			}
			recoveryTurns++
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    search.Name,
				Payload: rawjson.Message(`{"query":"still-bad"}`),
			}}}, nil
		},
	)

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "bad-call",
		Name:       search.Name,
		Payload:    rawjson.Message(`{"query":"bad"}`),
	}}}, policy.CapsState{
		MaxToolCalls:           5,
		RemainingToolCalls:     5,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "stopped after repeated failures", out.Final.Text())
	assert.Equal(t, 1, recoveryTurns)
	assert.Len(t, out.ToolEvents, 2)
}

func TestRunLoopRecoveryCatalogRejectsExcludedCallBeforeExecution(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	list := newAnyJSONSpec("catalog.list")
	var listCalls, searchCalls, resumes int
	h := newRecoveryHarness(
		t,
		"excluded-call",
		[]tools.ToolSpec{search, list},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			switch call.Name {
			case list.Name:
				listCalls++
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: testToolFailure(
						planner.FailureDomainRejection,
						planner.RecoveryReplan,
						"list query is too broad",
					),
				}, nil
			case search.Name:
				searchCalls++
				return successfulToolResult(call), nil
			case tools.ToolUnavailable:
				require.FailNow(t, "runtime unavailable tool reached catalog executor")
				return nil, nil
			default:
				require.FailNow(t, "unexpected catalog tool", call.Name)
				return nil, nil
			}
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1:
				assertAdvertisedTools(t, input, search.Name)
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{
					{Name: list.Name, Payload: rawjson.Message(`{"page":2}`)},
					{Name: search.Name, Payload: rawjson.Message(`{"query":"fallback"}`)},
				}}, nil
			case 2:
				return finalPlannerResult("recovered with search"), nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		},
	)

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name: list.Name, ToolCallID: "list-initial", Payload: rawjson.Message(`{"page":1}`),
	}}}, initialCaps(RunPolicy{MaxToolCalls: 5}))

	require.Error(t, err)
	assert.Nil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	assert.Equal(t, 1, listCalls)
	assert.Zero(t, searchCalls)
}

func TestRecoveryCatalogAndMixedFailureContracts(t *testing.T) {
	t.Parallel()

	search := newAnyJSONSpec("catalog.search")
	list := newAnyJSONSpec("catalog.list")
	outputs := []*planner.ToolOutput{
		recoveryOutput(search.Name, "search-correct", planner.RecoveryCorrectCall),
		recoveryOutput(search.Name, "search-replan", planner.RecoveryReplan),
		recoveryOutput(list.Name, "list-replan", planner.RecoveryReplan),
	}

	rt := New(newTestStore())
	seedTestToolSpecs(rt, search, list)
	assert.Equal(t, []tools.Ident{list.Name}, rt.recoveryUnavailableTools("", outputs, false))
	reminders := rt.recoveryReminders(outputs)
	require.Len(t, reminders, 3)
	assert.Contains(t, reminders[0].Text, "remains available")
	assert.Contains(t, reminders[1].Text, "Do not repeat this rejected request")

	result := &PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "search-call",
		Name:       search.Name,
	}}}
	require.NoError(t, validateRecoveryCatalog(
		outputs,
		&RecoveryCatalog{Tools: []tools.Ident{search.Name}},
		result,
	))
	require.ErrorContains(t, validateRecoveryCatalog(
		outputs,
		&RecoveryCatalog{Tools: []tools.Ident{list.Name}},
		result,
	), "outside the advertised recovery catalog")
	require.NoError(t, validateRecoveryCatalog(nil, nil, result))
	require.ErrorContains(t, validateRecoveryCatalog(
		outputs,
		nil,
		result,
	), "requires a recovery catalog")
	require.ErrorContains(t, validateRecoveryCatalog(
		nil,
		&RecoveryCatalog{Tools: []tools.Ident{search.Name}},
		result,
	), "without pending recovery failures")
	excluded := &PlanResult{ToolCalls: []ToolCall{{
		Name:       search.Name,
		ToolCallID: "search-1",
		Payload:    rawjson.Message(`{"query":"stale"}`),
	}}}
	require.ErrorContains(t, validateRecoveryCatalog(
		outputs,
		&RecoveryCatalog{Tools: []tools.Ident{list.Name}},
		excluded,
	), "outside the advertised recovery catalog")

	plainClarification := &PlanResult{Await: planner.NewAwait(
		planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID: "clarify", Question: "Which catalog?",
		}),
	)}
	require.NoError(t, validateRecoveryCatalog(
		outputs,
		&RecoveryCatalog{Tools: []tools.Ident{search.Name}},
		plainClarification,
	))
	toolBackedAwait := &PlanResult{Await: planner.NewAwait(
		planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
			ID:              "tool-clarify",
			ToolName:        list.Name,
			ModelToolCallID: "call-list",
			Question:        "Which page?",
		}),
	)}
	require.NoError(t, validateRecoveryCatalog(
		outputs,
		&RecoveryCatalog{Tools: []tools.Ident{list.Name}},
		toolBackedAwait,
	))
	require.ErrorContains(t, validateRecoveryCatalog(
		outputs,
		&RecoveryCatalog{Tools: []tools.Ident{search.Name}},
		toolBackedAwait,
	), "outside the advertised recovery catalog")
}

func TestFinishFailureFinalizesWithExactCause(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	load := newAnyJSONSpec("catalog.load")
	var resumes int
	h := newRecoveryHarness(
		t,
		"finish",
		[]tools.ToolSpec{search, load},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == load.Name {
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: testToolFailure(
						planner.FailureInternal,
						planner.RecoveryFinish,
						"load failed",
					),
				}, nil
			}
			return invalidCallResult(call), nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			require.False(t, input.SynthesisOnly)
			require.NotNil(t, input.Finalize)
			assert.Equal(t, planner.TerminationReasonToolFailure, input.Finalize.Reason)
			require.Len(t, input.Reminders, 1)
			assert.Contains(t, input.Reminders[0].Text, "load failed")
			return finalPlannerResult("partial result"), nil
		},
	)

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: search.Name, ToolCallID: "search-call", Payload: rawjson.Message(`{"query":"bad"}`)},
		{Name: load.Name, ToolCallID: "load-call", Payload: rawjson.Message(`{"id":"one"}`)},
	}}, initialCaps(RunPolicy{MaxToolCalls: 4}))

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "partial result", out.Final.Text())
	assert.Equal(t, 1, resumes)
	assert.Equal(t, []string{"load-call"}, h.workflow.lastPlannerCall.Input.RecoveryToolCallIDs)
}

func TestFinishFailureFinalizationRetainsOnlyTerminalTools(t *testing.T) {
	load := newAnyJSONSpec("catalog.load")
	progress := newAnyJSONSpec("catalog.progress")
	progress.Bookkeeping = true
	complete := newAnyJSONSpec("catalog.complete")
	complete.Bookkeeping = true
	complete.TerminalRun = true
	var completeCalls int
	h := newRecoveryHarness(
		t,
		"finish-with-terminal-tool",
		[]tools.ToolSpec{load, progress, complete},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			switch call.Name {
			case load.Name:
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: testToolFailure(
						planner.FailureInternal,
						planner.RecoveryFinish,
						"load failed",
					),
				}, nil
			case complete.Name:
				completeCalls++
				return successfulToolResult(call), nil
			case tools.ToolUnavailable:
				require.FailNow(t, "runtime unavailable tool reached catalog executor")
				return nil, nil
			default:
				require.FailNow(t, "unexpected recovery tool", call.Name)
				return nil, nil
			}
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			require.NotNil(t, input.Finalize)
			assert.Equal(t, planner.TerminationReasonToolFailure, input.Finalize.Reason)
			assertAdvertisedTools(t, input, complete.Name)
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    complete.Name,
				Payload: rawjson.Message(`{"result":"partial"}`),
			}}}, nil
		},
	)

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
		Name: load.Name, ToolCallID: "load-call", Payload: rawjson.Message(`{"id":"one"}`),
	}}}, initialCaps(RunPolicy{MaxToolCalls: 4}))

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, 1, completeCalls)
	assert.Len(t, out.ToolEvents, 2)
}

func TestFinishFailurePreservesLiveContinuation(t *testing.T) {
	search, continuation := continuationTestSpecs()
	load := newAnyJSONSpec("catalog.load")
	continueAction := continuationActionName(continuation.Name, "search-call")
	var resumes int
	h := newRecoveryHarness(
		t,
		"finish-with-continuation",
		[]tools.ToolSpec{search, continuation, load},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			switch call.Name {
			case search.Name:
				result := successfulToolResult(call)
				result.Bounds = &agent.Bounds{
					Returned:   40,
					Truncated:  true,
					NextCursor: pointer("page-2"),
				}
				return result, nil
			case continuation.Name:
				assert.JSONEq(t, `{"cursor":"page-2"}`, string(call.Payload))
				result := successfulToolResult(call)
				result.Bounds = &agent.Bounds{Returned: 7}
				return result, nil
			case load.Name:
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: testToolFailure(
						planner.FailureInternal,
						planner.RecoveryFinish,
						"load failed",
					),
				}, nil
			case tools.ToolUnavailable:
				require.FailNow(t, "runtime unavailable tool reached catalog executor")
				return nil, nil
			default:
				require.FailNow(t, "unexpected tool call", call.Name)
				return nil, nil
			}
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1:
				require.False(t, input.SynthesisOnly)
				assertAdvertisedTools(t, input, continueAction)
				require.Len(t, input.Reminders, 1)
				assert.Contains(t, input.Reminders[0].Text, "load failed")
				client, ok := input.Agent.PlannerModelClient("test")
				require.True(t, ok)
				response, err := client.Complete(ctx, &model.Request{
					Model: "test",
					Tools: input.Agent.AdvertisedToolDefinitions(),
				})
				require.NoError(t, err)
				calls := response.ToolCalls()
				require.Len(t, calls, 1)
				request, err := planner.ToolRequestFromModelCall(calls[0])
				require.NoError(t, err)
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{request}}, nil
			case 2:
				require.False(t, input.SynthesisOnly)
				assertAdvertisedTools(t, input, search.Name, load.Name)
				assert.Empty(t, input.Reminders)
				return finalPlannerResult("all pages collected"), nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		},
	)
	h.runtime.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse(nil, model.ToolCall{
				ID:      "continue-call",
				Name:    continueAction,
				Payload: rawjson.Message(`{}`),
			}), nil
		},
	})

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: search.Name, ToolCallID: "search-call", Payload: rawjson.Message(`{"query":"alarms"}`)},
		{Name: load.Name, ToolCallID: "load-call", Payload: rawjson.Message(`{"id":"one"}`)},
	}}, initialCaps(RunPolicy{MaxToolCalls: 5}))

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "all pages collected", out.Final.Text())
	assert.Equal(t, 2, resumes)
	assert.Len(t, out.ToolEvents, 3)
}

func TestRunLoopRecoversRejectedModelAnswer(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	resumes := 0
	h := newRecoveryHarness(
		t,
		"model-output",
		[]tools.ToolSpec{search},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			if resumes == 1 {
				client, ok := input.Agent.ModelClient("test")
				require.True(t, ok)
				response, err := client.Complete(ctx, &model.Request{Model: "test"})
				require.NoError(t, err)
				return nil, planner.NewRecoverableModelOutputError(
					errors.New("answer used too many references"),
					&planner.FinalResponse{Message: &response.Content[len(response.Content)-1]},
					"Use at most eight evidence references.",
				)
			}
			require.True(t, input.SynthesisOnly)
			assert.Empty(t, input.Agent.AdvertisedToolDefinitions())
			require.Len(t, input.Reminders, 1)
			assert.Contains(t, input.Reminders[0].Text, "Use at most eight evidence references.")
			return finalPlannerResult("corrected answer"), nil
		},
	)
	h.runtime.models["test"] = newRecoveryTestModel(t)

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "search-call",
			Name:       search.Name,
			Payload:    rawjson.Message(`{"query":"evidence"}`),
		}},
		SynthesizeAfterTools: true,
	}, policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       2,
		RemainingRecoveryTurns: 2,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "corrected answer", out.Final.Text())
	assert.Equal(t, 2, resumes)
	assert.Len(t, out.ToolEvents, 1)
}

func TestRunLoopSharesRecoveryBudgetBetweenToolAndModelRejections(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	resumes := 0
	h := newRecoveryHarness(
		t,
		"shared-budget",
		[]tools.ToolSpec{search},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return invalidCallResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1:
				require.Nil(t, input.Finalize)
				require.False(t, input.SynthesisOnly)
				require.Len(t, input.ToolOutputs, 1)
				require.Equal(t, "search-call", input.ToolOutputs[0].ToolCallID)
				require.NotNil(t, input.ToolOutputs[0].Failure)
				client, ok := input.Agent.ModelClient("test")
				require.True(t, ok)
				response, err := client.Complete(ctx, &model.Request{Model: "test"})
				require.NoError(t, err)
				return nil, planner.NewRecoverableModelOutputError(
					errors.New("replacement answer is invalid"),
					&planner.FinalResponse{Message: &response.Content[len(response.Content)-1]},
					"Replace the invalid final answer.",
				)
			case 2:
				require.NotNil(t, input.Finalize)
				require.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
				return finalPlannerResult("shared recovery budget exhausted"), nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		},
	)
	h.runtime.models["test"] = newRecoveryTestModel(t)

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "search-call",
			Name:       search.Name,
			Payload:    rawjson.Message(`{"query":"invalid"}`),
		}},
	}, policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "shared recovery budget exhausted", out.Final.Text())
	assert.Equal(t, 2, resumes)
}

func TestWorkflowRecoversInitialRejectedModelAnswer(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	sessionID := "session-initial-model-output"
	_, err := createSessionForTest(context.Background(), rt.Store, sessionID)
	require.NoError(t, err)

	agentID := agent.Ident("catalog.initial-model-output")
	resumes := 0
	registration := AgentRegistration{Definition: testRegistrationDefinition(agentID, engine.WorkflowDefinition{}, []tools.ToolSpec{search}), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, PlanActivityName: "plan",
		ResumeActivityName: "resume",
		Planner: &stubPlanner{
			start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				client, ok := input.Agent.ModelClient("test")
				require.True(t, ok)
				response, err := client.Complete(ctx, &model.Request{Model: "test"})
				require.NoError(t, err)
				return nil, planner.NewRecoverableModelOutputError(
					errors.New("answer used too many references"),
					&planner.FinalResponse{Message: &response.Content[len(response.Content)-1]},
					"Use at most eight evidence references.",
				)
			},
			resume: func(
				_ context.Context,
				input *planner.PlanResumeInput,
			) (*planner.PlanResult, error) {
				resumes++
				require.True(t, input.SynthesisOnly)
				assert.Empty(t, input.Agent.AdvertisedToolDefinitions())
				require.Len(t, input.Reminders, 1)
				assert.Contains(t, input.Reminders[0].Text, "Use at most eight evidence references.")
				return finalPlannerResult("corrected initial answer"), nil
			},
		},
		Policy: RunPolicy{MaxRecoveryTurns: 1},
	}
	rt.agents[agentID] = registration
	rt.agentToolSpecs[agentID] = registration.Definition.specs
	seedTestToolDefinitions(rt, search)
	rt.models["test"] = newRecoveryTestModel(t)

	runInput := &RunInput{
		AgentID:   agentID,
		RunID:     "run-initial-model-output",
		SessionID: sessionID,
		TurnID:    "turn-initial-model-output",
	}
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       runInput.RunID,
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan":   rt.PlanStartActivity,
			"resume": rt.PlanResumeActivity,
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, runInput)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "corrected initial answer", out.Final.Text())
	assert.Equal(t, 1, resumes)
	assert.Equal(t, 11, out.Usage.InputTokens)
}

func TestRunLoopStopsAfterConfiguredModelOutputRecoveryTurns(t *testing.T) {
	search := newAnyJSONSpec("catalog.search")
	recoveryAttempts := 0
	h := newRecoveryHarness(
		t,
		"model-output-cap",
		[]tools.ToolSpec{search},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			if input.Finalize != nil {
				require.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
				assert.Contains(t, input.Finalize.Message, "repeated recovery attempts")
				assert.NotContains(t, input.Finalize.Message, "tool failures")
				return finalPlannerResult("stopped after recovery cap"), nil
			}
			recoveryAttempts++
			client, ok := input.Agent.ModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{Model: "test"})
			require.NoError(t, err)
			return nil, planner.NewRecoverableModelOutputError(
				errors.New("answer remains invalid"),
				&planner.FinalResponse{Message: &response.Content[len(response.Content)-1]},
				"Replace the invalid final answer.",
			)
		},
	)
	h.runtime.models["test"] = newRecoveryTestModel(t)

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "search-call",
			Name:       search.Name,
			Payload:    rawjson.Message(`{"query":"evidence"}`),
		}},
		SynthesizeAfterTools: true,
	}, policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       2,
		RemainingRecoveryTurns: 2,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "stopped after recovery cap", out.Final.Text())
	assert.Equal(t, 3, recoveryAttempts)
}

func TestExecuteWorkflowRecoversInitialGeneratedModelToolCall(t *testing.T) {
	lookup := newStrictRecoverySpec()
	var providerCalls, lookupCalls, resumes int
	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	sessionID := "session-initial-recovery"
	_, err := createSessionForTest(context.Background(), rt.Store, sessionID)
	require.NoError(t, err)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name:  "catalog",
		Specs: []tools.ToolSpec{lookup},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			lookupCalls++
			return successfulToolResult(call), nil
		}),
	}))
	rt.models["test"] = newPreResponseRecoveryModel(t, &providerCalls, false)

	agentID := agent.Ident("catalog.initial-recovery")
	pl := &stubPlanner{
		start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			_, err := client.Complete(ctx, &model.Request{
				Model: "test",
				Tools: input.Agent.AdvertisedToolDefinitions(),
			})
			return nil, err
		},
		resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			if len(input.ToolOutputs) > 0 {
				return finalPlannerResult("initial replacement completed"), nil
			}
			assert.False(t, input.SynthesisOnly)
			assertAdvertisedTools(t, input, lookup.Name)
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{
				Model: "test",
				Tools: input.Agent.AdvertisedToolDefinitions(),
			})
			require.NoError(t, err)
			calls := response.ToolCalls()
			require.Len(t, calls, 1)
			request, err := planner.ToolRequestFromModelCall(calls[0])
			require.NoError(t, err)
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{request}}, nil
		},
	}
	rt.agents[agentID] = AgentRegistration{Definition: testRegistrationDefinition(agentID, engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: pl,
		PlanActivityName:    "start",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
	}
	rt.agentToolSpecs[agentID] = []tools.ToolSpec{lookup}
	input := &RunInput{
		AgentID:   agentID,
		RunID:     "run-initial-recovery",
		SessionID: sessionID,
	}
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       input.RunID,
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"start":  rt.PlanStartActivity,
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, input)

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "initial replacement completed", out.Final.Text())
	assert.Equal(t, 2, providerCalls)
	assert.Equal(t, 1, lookupCalls)
	assert.Equal(t, 2, resumes)
}

func TestRunLoopRecoversGeneratedModelToolCallBeforeExecution(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.kickoff")
	lookup := newStrictRecoverySpec()
	var providerCalls, lookupCalls, resumes int
	h := newRecoveryHarness(
		t,
		"model-invocation",
		[]tools.ToolSpec{kickoff, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == lookup.Name {
				lookupCalls++
				assert.JSONEq(t, `{"query":"accepted"}`, string(call.Payload))
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			assert.False(t, input.SynthesisOnly)
			assertAdvertisedTools(t, input, kickoff.Name, lookup.Name)
			if len(input.ToolOutputs) == 2 {
				return finalPlannerResult("replacement completed"), nil
			}
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{
				Model: "test",
				Tools: input.Agent.AdvertisedToolDefinitions(),
			})
			if err != nil {
				var validationErr *model.OutputValidationError
				require.ErrorAs(t, err, &validationErr)
				require.NotEmpty(t, validationErr.RecoveryCorrection())
				return nil, err
			}
			calls := response.ToolCalls()
			require.Len(t, calls, 1)
			request, err := planner.ToolRequestFromModelCall(calls[0])
			require.NoError(t, err)
			require.Len(t, input.Reminders, 1)
			assert.Equal(t, "model_invocation_recovery", input.Reminders[0].ID)
			assert.Contains(t, input.Reminders[0].Text, "did not match its advertised input schema")
			assert.NotContains(t, input.Reminders[0].Text, "privateSecret")
			assert.NotContains(t, input.Reminders[0].Text, "submitted-secret")
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{request}}, nil
		},
	)
	h.runtime.models["test"] = newPreResponseRecoveryModel(t, &providerCalls, false)

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "kickoff-call",
			Name:       kickoff.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}, policy.CapsState{
		MaxToolCalls:           3,
		RemainingToolCalls:     3,
		MaxRecoveryTurns:       2,
		RemainingRecoveryTurns: 2,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "replacement completed", out.Final.Text())
	assert.Equal(t, 2, providerCalls)
	assert.Equal(t, 1, lookupCalls)
	assert.Equal(t, 3, resumes)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 22, out.Usage.TotalTokens)
}

func TestRunLoopModelInvocationRecoveryUsesSharedTurnCap(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.kickoff")
	lookup := newStrictRecoverySpec()
	var providerCalls, lookupCalls, recoveryAttempts int
	h := newRecoveryHarness(
		t,
		"model-invocation-cap",
		[]tools.ToolSpec{kickoff, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == lookup.Name {
				lookupCalls++
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			if input.Finalize != nil {
				require.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
				return finalPlannerResult("stopped after invocation recovery cap"), nil
			}
			recoveryAttempts++
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			_, err := client.Complete(ctx, &model.Request{
				Model: "test",
				Tools: input.Agent.AdvertisedToolDefinitions(),
			})
			return nil, err
		},
	)
	h.runtime.models["test"] = newPreResponseRecoveryModel(t, &providerCalls, true)

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "kickoff-call",
			Name:       kickoff.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}, policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       2,
		RemainingRecoveryTurns: 2,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "stopped after invocation recovery cap", out.Final.Text())
	assert.Equal(t, 3, recoveryAttempts)
	assert.Equal(t, 3, providerCalls)
	assert.Zero(t, lookupCalls)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 30, out.Usage.TotalTokens)
}

func TestRunLoopCancellationPreventsModelInvocationReplacement(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.kickoff")
	lookup := newStrictRecoverySpec()
	var providerCalls, plannerCalls int
	h := newRecoveryHarness(
		t,
		"model-invocation-cancel",
		[]tools.ToolSpec{kickoff, lookup},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			_, err := client.Complete(ctx, &model.Request{
				Model: "test",
				Tools: input.Agent.AdvertisedToolDefinitions(),
			})
			return nil, err
		},
	)
	h.runtime.models["test"] = newPreResponseRecoveryModel(t, &providerCalls, true)
	workflowCtx, cancel := context.WithCancel(context.Background())
	h.workflow.ctx = workflowCtx
	resume := h.workflow.plannerRoutes["resume"]
	h.workflow.plannerRoutes["resume"] = func(
		ctx context.Context,
		input *PlanActivityInput,
	) (*PlanActivityOutput, error) {
		plannerCalls++
		output, err := resume(ctx, input)
		if output != nil && output.ModelInvocationRecovery != nil {
			cancel()
		}
		return output, err
	}

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "kickoff-call",
			Name:       kickoff.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}, policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       2,
		RemainingRecoveryTurns: 2,
	})

	require.Nil(t, out)
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, plannerCalls)
	assert.Equal(t, 1, providerCalls)
}

func TestRunLoopConsumesRecordedModelInvocationRecoveryWithoutProviderCall(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.kickoff")
	var providerCalls, plannerCalls int
	h := newRecoveryHarness(
		t,
		"model-invocation-replay",
		[]tools.ToolSpec{kickoff},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			require.FailNow(t, "recorded activity output should bypass planner code")
			return nil, nil
		},
	)
	h.runtime.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			providerCalls++
			return nil, errors.New("provider must not run during replay")
		},
	})
	h.workflow.plannerRoutes["resume"] = func(
		_ context.Context,
		input *PlanActivityInput,
	) (*PlanActivityOutput, error) {
		if input == nil {
			return nil, errors.New("replayed planner input is required")
		}
		plannerCalls++
		if plannerCalls == 1 {
			return &PlanActivityOutput{
				PublicationBatchID: "00000000-0000-4000-8000-000000000001",
				ModelInvocationRecovery: &ModelInvocationRecovery{
					Correction: "Field \"query\" must contain a JSON string.",
				},
			}, nil
		}
		require.NotNil(t, input.ModelInvocationRecovery)
		return &PlanActivityOutput{
			PublicationBatchID: "00000000-0000-4000-8000-000000000002",
			Result: &PlanResult{
				FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "replayed replacement"}},
					},
				},
			},
		}, nil
	}

	out, err := h.run(&PlanResult{
		ToolCalls: []ToolCall{{
			ToolCallID: "kickoff-call",
			Name:       kickoff.Name,
			Payload:    rawjson.Message(`{}`),
		}},
	}, policy.CapsState{
		MaxToolCalls:           2,
		RemainingToolCalls:     2,
		MaxRecoveryTurns:       1,
		RemainingRecoveryTurns: 1,
	})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "replayed replacement", out.Final.Text())
	assert.Equal(t, 2, plannerCalls)
	assert.Zero(t, providerCalls)
}

// newRecoveryHarness assembles one in-memory workflow whose planner and tool
// behavior are supplied by the test. It uses the real resume activity so tests
// cover canonical run-log loading, recovery reminders, and advertised tools.
func newRecoveryHarness(
	t *testing.T,
	name string,
	specs []tools.ToolSpec,
	execute func(context.Context, *ToolCall) (*planner.ToolResult, error),
	resume func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error),
) *recoveryHarness {
	t.Helper()

	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	sessionID := "session-" + name
	_, err := createSessionForTest(context.Background(), rt.Store, sessionID)
	require.NoError(t, err)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name:    "catalog",
		Execute: wrapExecute(execute),
		Specs:   specs,
	}))

	agentID := agent.Ident("catalog." + name)
	registration := AgentRegistration{Definition: testRegistrationDefinition(agentID, engine.WorkflowDefinition{}, specs), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: &stubPlanner{resume: resume},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}
	rt.agents[agentID] = registration
	rt.agentToolSpecs[agentID] = specs
	runID := "run-" + name
	turnID := "turn-" + name
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       runID,
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}
	input := &RunInput{
		AgentID:   agentID,
		RunID:     runID,
		SessionID: sessionID,
		TurnID:    turnID,
	}
	seedRunMeta(t, rt, input)
	return &recoveryHarness{
		runtime:      rt,
		registration: registration,
		workflow:     wfCtx,
		input:        input,
		base: &planner.PlanInput{RunContext: run.Context{
			RunID: runID, SessionID: sessionID, TurnID: turnID, Attempt: 1,
		}},
	}
}

// run executes a complete workflow from the selected initial planner result.
// These fixtures represent model calls, so their provider IDs match the
// human-readable execution IDs chosen by each test.
func (h *recoveryHarness) run(
	initial *PlanResult,
	caps policy.CapsState,
) (*RunOutput, error) {
	for i := range initial.ToolCalls {
		if initial.ToolCalls[i].ModelToolCallID == "" {
			initial.ToolCalls[i].ModelToolCallID = initial.ToolCalls[i].ToolCallID
		}
	}
	return h.runtime.runLoop(
		h.workflow,
		h.registration,
		h.input,
		h.base,
		initial,
		caps,
		time.Time{},
		time.Time{},
		h.input.TurnID,
		nil,
	)
}

// newRecoveryTestModel returns one validated model client whose completed
// response can be correlated with a planner-authored recovery error.
func newRecoveryTestModel(t *testing.T) model.Client {
	t.Helper()
	return mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "answer with too many references"}},
				}},
				StopReason: "end_turn",
				Usage: model.TokenUsage{
					InputTokens:  11,
					OutputTokens: 5,
					TotalTokens:  16,
				},
			}, nil
		},
	})
}

// newStrictRecoverySpec simulates one generated codec that rejects malformed
// provider arguments with structured field issues and accepts only the
// replacement payload used by these workflow tests.
func newStrictRecoverySpec() tools.ToolSpec {
	return tools.ToolSpec{
		Name:        "catalog.lookup",
		Description: "Looks up one synthetic record.",
		Payload: tools.TypeSpec{
			Name:   "LookupPayload",
			Schema: rawjson.Message(`{"type":"object","properties":{"query":{"type":"string"}},"required":["query"],"additionalProperties":false}`),
			FieldJSONTypes: map[string]string{
				"$payload": "object",
				"query":    "string",
			},
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					if string(data) == `{"query":"accepted"}` {
						return map[string]any{"query": "accepted"}, nil
					}
					return nil, tools.NewValidationError(
						"provider submitted an invalid lookup payload",
						[]*tools.FieldIssue{
							{
								Field:            "query",
								Constraint:       "invalid_field_type",
								ExpectedJSONType: "string",
								ActualJSONType:   "number",
							},
							{
								Field:      "privateSecret",
								Constraint: "unknown_field",
								Allowed:    []string{"query"},
							},
						},
						nil,
					)
				},
			},
		},
		Result: tools.TypeSpec{
			Name:   "LookupResult",
			Schema: rawjson.Message(`{"type":"object"}`),
			Codec:  tools.AnyJSONCodec,
		},
	}
}

// newPreResponseRecoveryModel returns malformed generated tool arguments on
// the first invocation, then either repeats the failure or returns one valid
// replacement. Each response reports a distinct invocation total.
func newPreResponseRecoveryModel(
	t *testing.T,
	providerCalls *int,
	alwaysInvalid bool,
) model.Client {
	t.Helper()
	return mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			(*providerCalls)++
			payload := rawjson.Message(`{"query":42,"privateSecret":"submitted-secret"}`)
			usage := model.TokenUsage{
				InputTokens:  6,
				OutputTokens: 4,
				TotalTokens:  10,
			}
			if *providerCalls > 1 && !alwaysInvalid {
				payload = rawjson.Message(`{"query":"accepted"}`)
				usage = model.TokenUsage{
					InputTokens:  7,
					OutputTokens: 5,
					TotalTokens:  12,
				}
			}
			return testModelResponseWithUsage(
				nil,
				usage,
				model.ToolCall{
					ID:      fmt.Sprintf("lookup-call-%d", *providerCalls),
					Name:    "catalog.lookup",
					Payload: payload,
				},
			), nil
		},
	})
}

// invalidCallResult returns a correctable validation failure while preserving
// the executed call's identity.
func invalidCallResult(call *ToolCall) *planner.ToolResult {
	return &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			planner.RecoveryCorrectCall,
			"query is invalid",
		),
	}
}

// successfulToolResult returns the minimal canonical success used by recovery
// workflow tests.
func successfulToolResult(call *ToolCall) *planner.ToolResult {
	return &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Result:     map[string]any{"ok": true},
	}
}

// finalPlannerResult returns one terminal assistant response.
func finalPlannerResult(text string) *planner.PlanResult {
	return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: text}},
	}}}
}

// assertAdvertisedTools verifies the recovery turn retains exactly the tools
// authorized for the run.
func assertAdvertisedTools(t *testing.T, input *planner.PlanResumeInput, want ...tools.Ident) {
	t.Helper()
	definitions := input.Agent.AdvertisedToolDefinitions()
	got := make([]string, len(definitions))
	for i, definition := range definitions {
		got[i] = definition.Name
	}
	expected := make([]string, len(want))
	for i, name := range want {
		expected[i] = name.String()
	}
	assert.ElementsMatch(t, expected, got)
}

// recoveryOutput creates one canonical failure selected for a recovery turn.
func recoveryOutput(name tools.Ident, callID string, action planner.RecoveryAction) *planner.ToolOutput {
	return &planner.ToolOutput{
		Name:       name,
		ToolCallID: callID,
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			action,
			"failed",
		),
	}
}
