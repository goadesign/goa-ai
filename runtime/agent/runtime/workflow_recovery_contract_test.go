package runtime

// workflow_recovery_contract_test.go verifies that failed tool calls provide
// correction evidence without dictating the planner's semantic next action.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
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
	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
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
				assertAdvertisedTools(t, input, search.Name, list.Name)
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
	}}, policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 10})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "combined", agentMessageText(out.Final))
	assert.Equal(t, 2, resumes)
}

func TestRunLoopCorrectionMayChooseAnotherToolOrAnswer(t *testing.T) {
	tests := []struct {
		name           string
		choose         func(search, list tools.ToolSpec) *planner.PlanResult
		wantToolEvents int
		wantAnswer     string
	}{
		{
			name: "another tool",
			choose: func(_, list tools.ToolSpec) *planner.PlanResult {
				return &planner.PlanResult{
					ToolCalls: []planner.ToolRequest{{
						Name:    list.Name,
						Payload: rawjson.Message(`{"page":1}`),
					}},
					SynthesizeAfterTools: true,
				}
			},
			wantToolEvents: 2,
			wantAnswer:     "alternative",
		},
		{
			name: "multiple calls",
			choose: func(_, list tools.ToolSpec) *planner.PlanResult {
				return &planner.PlanResult{
					ToolCalls: []planner.ToolRequest{
						{Name: list.Name, Payload: rawjson.Message(`{"page":1}`)},
						{Name: list.Name, Payload: rawjson.Message(`{"page":2}`)},
					},
					SynthesizeAfterTools: true,
				}
			},
			wantToolEvents: 3,
			wantAnswer:     "expanded",
		},
		{
			name: "final answer",
			choose: func(_, _ tools.ToolSpec) *planner.PlanResult {
				return finalPlannerResult("provisional")
			},
			wantToolEvents: 1,
			wantAnswer:     "provisional",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			search := newAnyJSONSpec("catalog.search", "catalog")
			list := newAnyJSONSpec("catalog.list", "catalog")
			resumes := 0
			h := newRecoveryHarness(
				t,
				"choice-"+tt.name,
				[]tools.ToolSpec{search, list},
				func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
					if call.Name == search.Name {
						return invalidCallResult(call), nil
					}
					return successfulToolResult(call), nil
				},
				func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumes++
					if resumes == 1 {
						assertAdvertisedTools(t, input, search.Name, list.Name)
						require.Len(t, input.Reminders, 1)
						return tt.choose(search, list), nil
					}
					require.True(t, input.SynthesisOnly)
					return finalPlannerResult(tt.wantAnswer), nil
				},
			)

			out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{
				ToolCallID: "bad-call",
				Name:       search.Name,
				Payload:    rawjson.Message(`{"query":"bad"}`),
			}}}, policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4})

			require.NoError(t, err)
			require.NotNil(t, out)
			assert.Equal(t, tt.wantAnswer, agentMessageText(out.Final))
			assert.Len(t, out.ToolEvents, tt.wantToolEvents)
		})
	}
}

func TestRunLoopPreservesCorrectionEvidenceAcrossClarification(t *testing.T) {
	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
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
				assertAdvertisedTools(t, input, search.Name, list.Name)
				require.Len(t, input.Reminders, 1)
				return &planner.PlanResult{Await: planner.NewAwait(
					planner.AwaitClarificationItem(&planner.AwaitClarification{
						ID:            "clarify-query",
						Question:      "What should the search query be?",
						MissingFields: []string{"query"},
					}),
				)}, nil
			case 2:
				assertAdvertisedTools(t, input, search.Name, list.Name)
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
	}}}, policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4})

	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, out.Suspension)
}

func TestRunLoopInvalidCallReachesFailureFinalization(t *testing.T) {
	search := newAnyJSONSpec("catalog.search", "catalog")
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
	assert.Equal(t, "stopped after repeated failures", agentMessageText(out.Final))
	assert.Equal(t, 1, recoveryTurns)
	assert.Len(t, out.ToolEvents, 2)
}

func TestRunLoopRecoveryCatalogRejectsExcludedCallBeforeExecution(t *testing.T) {
	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
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
	}}}, policy.CapsState{MaxToolCalls: 5, RemainingToolCalls: 5})

	require.Error(t, err)
	assert.Nil(t, out)
	var outputErr *planner.OutputContractError
	require.ErrorAs(t, err, &outputErr)
	assert.Equal(t, 1, listCalls)
	assert.Zero(t, searchCalls)
}

func TestRecoveryCatalogAndMixedFailureContracts(t *testing.T) {
	t.Parallel()

	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
	outputs := []*planner.ToolOutput{
		recoveryOutput(search.Name, "search-correct", planner.RecoveryCorrectCall),
		recoveryOutput(search.Name, "search-replan", planner.RecoveryReplan),
		recoveryOutput(list.Name, "list-replan", planner.RecoveryReplan),
	}

	rt := New()
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
	search := newAnyJSONSpec("catalog.search", "catalog")
	load := newAnyJSONSpec("catalog.load", "catalog")
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
	}}, policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "partial result", agentMessageText(out.Final))
	assert.Equal(t, 1, resumes)
	assert.Equal(t, []string{"load-call"}, h.workflow.lastPlannerCall.Input.RecoveryToolCallIDs)
}

func TestFinishFailureFinalizationRetainsOnlyTerminalTools(t *testing.T) {
	load := newAnyJSONSpec("catalog.load", "catalog")
	progress := newAnyJSONSpec("catalog.progress", "catalog")
	progress.Bookkeeping = true
	complete := newAnyJSONSpec("catalog.complete", "catalog")
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
	}}}, policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, 1, completeCalls)
	assert.Len(t, out.ToolEvents, 2)
}

func TestFinishFailurePreservesLiveContinuation(t *testing.T) {
	search, continuation := continuationTestSpecs()
	search.Toolset = "catalog"
	continuation.Toolset = "catalog"
	load := newAnyJSONSpec("catalog.load", "catalog")
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
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			switch resumes {
			case 1:
				require.False(t, input.SynthesisOnly)
				assertAdvertisedTools(t, input, continueAction)
				require.Len(t, input.Reminders, 1)
				assert.Contains(t, input.Reminders[0].Text, "load failed")
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name:    continueAction,
					Payload: rawjson.Message(`{}`),
				}}}, nil
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

	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{
		{Name: search.Name, ToolCallID: "search-call", Payload: rawjson.Message(`{"query":"alarms"}`)},
		{Name: load.Name, ToolCallID: "load-call", Payload: rawjson.Message(`{"id":"one"}`)},
	}}, policy.CapsState{MaxToolCalls: 5, RemainingToolCalls: 5})

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "all pages collected", agentMessageText(out.Final))
	assert.Equal(t, 2, resumes)
	assert.Len(t, out.ToolEvents, 3)
}

func TestRunLoopRecoversRejectedModelAnswer(t *testing.T) {
	search := newAnyJSONSpec("catalog.search", "catalog")
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
	assert.Equal(t, "corrected answer", agentMessageText(out.Final))
	assert.Equal(t, 2, resumes)
	assert.Len(t, out.ToolEvents, 1)
}

func TestWorkflowRecoversInitialRejectedModelAnswer(t *testing.T) {
	search := newAnyJSONSpec("catalog.search", "catalog")
	rt := New(WithLogger(telemetry.NoopLogger{}))
	sessionID := "session-initial-model-output"
	_, err := rt.CreateSession(context.Background(), sessionID)
	require.NoError(t, err)

	agentID := agent.Ident("catalog.initial-model-output")
	resumes := 0
	registration := AgentRegistration{
		ID:                 agentID,
		Specs:              []tools.ToolSpec{search},
		PlanActivityName:   "plan",
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
	rt.agentToolSpecs[agentID] = registration.Specs
	rt.models["test"] = newRecoveryTestModel(t)

	runInput := &RunInput{
		AgentID:   agentID,
		RunID:     "run-initial-model-output",
		SessionID: sessionID,
		TurnID:    "turn-initial-model-output",
	}
	seedRunMeta(t, rt, runInput)
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
	assert.Equal(t, "corrected initial answer", agentMessageText(out.Final))
	assert.Equal(t, 1, resumes)
	assert.Equal(t, 11, out.Usage.InputTokens)
}

func TestRunLoopStopsAfterConfiguredModelOutputRecoveryTurns(t *testing.T) {
	search := newAnyJSONSpec("catalog.search", "catalog")
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
	assert.Equal(t, "stopped after recovery cap", agentMessageText(out.Final))
	assert.Equal(t, 3, recoveryAttempts)
}

func TestRunLoopPreservesLegacyFailedBatchExhaustion(t *testing.T) {
	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
	correctionTurns := 0
	h := newRecoveryHarness(
		t,
		"legacy-failure-streak",
		[]tools.ToolSpec{search, list},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Failure: testToolFailure(
					planner.FailureDomainRejection,
					planner.RecoveryReplan,
					"choose another action",
				),
			}, nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			if input.Finalize != nil {
				require.FailNow(t, "finalization should use the prevalidated workflow route")
			}
			correctionTurns++
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    list.Name,
				Payload: rawjson.Message(`{"query":"still-invalid"}`),
			}}}, nil
		},
	)
	resumeActivity := h.workflow.plannerRoutes["resume"]
	finalizationTurns := 0
	h.workflow.plannerRoutes["resume"] = func(
		ctx context.Context,
		input *PlanActivityInput,
	) (*PlanActivityOutput, error) {
		if input.Finalize == nil {
			return resumeActivity(ctx, input)
		}
		finalizationTurns++
		require.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
		return &PlanActivityOutput{
			PublicationBatchID: "00000000-0000-4000-8000-000000000001",
			Result: &PlanResult{
				FinalResponse: finalPlannerResult("legacy failure cap exhausted").FinalResponse,
			},
		}, nil
	}

	out, err := h.runLegacy(&PlanResult{ToolCalls: []ToolCall{{
		ToolCallID: "initial-invalid",
		Name:       search.Name,
		Payload:    rawjson.Message(`{"query":"invalid"}`),
	}}}, policy.CapsState{
		MaxToolCalls:           4,
		RemainingToolCalls:     4,
		MaxRecoveryTurns:       2,
		RemainingRecoveryTurns: 2,
	})

	assert.Equal(t, 1, correctionTurns)
	assert.Equal(t, 1, finalizationTurns)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "legacy failure cap exhausted", agentMessageText(out.Final))
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

	rt := New(WithLogger(telemetry.NoopLogger{}))
	sessionID := "session-" + name
	_, err := rt.CreateSession(context.Background(), sessionID)
	require.NoError(t, err)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name:    "catalog",
		Execute: wrapExecute(execute),
		Specs:   specs,
	}))

	agentID := agent.Ident("catalog." + name)
	registration := AgentRegistration{
		ID:                  agentID,
		Planner:             &stubPlanner{resume: resume},
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

// runLegacy executes the focused workflow with the failed-batch accounting
// restored from an older Temporal history or version-four suspension.
func (h *recoveryHarness) runLegacy(
	initial *PlanResult,
	caps policy.CapsState,
) (*RunOutput, error) {
	for i := range initial.ToolCalls {
		if initial.ToolCalls[i].ModelToolCallID == "" {
			initial.ToolCalls[i].ModelToolCallID = initial.ToolCalls[i].ToolCallID
		}
	}
	state := newRunLoopState(initial, nil, model.TokenUsage{}, caps, 2)
	state.LegacyFailureStreak = true
	return h.runtime.runLoopWithState(
		h.workflow,
		h.registration,
		h.input,
		h.base,
		state,
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
