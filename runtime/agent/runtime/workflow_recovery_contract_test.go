package runtime

// workflow_recovery_contract_test.go verifies that failed tool calls provide
// correction evidence without dictating the planner's semantic next action.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/interrupt"
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
		func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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

	out, err := h.run(&planner.PlanResult{ToolCalls: []planner.ToolRequest{
		{Name: search.Name, Payload: rawjson.Message(`{"query":"bad-1"}`)},
		{Name: search.Name, Payload: rawjson.Message(`{"query":"bad-2"}`)},
		{Name: search.Name, Payload: rawjson.Message(`{"query":"bad-3"}`)},
		{Name: search.Name, Payload: rawjson.Message(`{"query":"bad-4"}`)},
	}}, policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 10}, nil)

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
						Name: list.Name, Payload: rawjson.Message(`{"page":1}`),
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
				func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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

			out, err := h.run(&planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name: search.Name, Payload: rawjson.Message(`{"query":"bad"}`),
			}}}, policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4}, nil)

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
		func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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
						Name: search.Name, Payload: rawjson.Message(`{"query":"good"}`),
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
	h.workflow.ensureSignals()
	ctrl := interrupt.NewController(h.workflow)
	h.workflow.clarifyCh <- &api.ClarificationAnswer{ID: "clarify-query", Answer: "Use good."}

	out, err := h.run(&planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name: search.Name, Payload: rawjson.Message(`{"query":"bad"}`),
	}}}, policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4}, ctrl)

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "clarified", agentMessageText(out.Final))
}

func TestRunLoopRepeatedInvalidCallsReachFailureFinalization(t *testing.T) {
	search := newAnyJSONSpec("catalog.search", "catalog")
	var recoveryTurns int
	h := newRecoveryHarness(
		t,
		"failure-cap",
		[]tools.ToolSpec{search},
		func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return invalidCallResult(call), nil
		},
		func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			if input.Finalize != nil {
				require.Equal(t, planner.TerminationReasonFailureCap, input.Finalize.Reason)
				return finalPlannerResult("stopped after repeated failures"), nil
			}
			recoveryTurns++
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name: search.Name, Payload: rawjson.Message(`{"query":"still-bad"}`),
			}}}, nil
		},
	)

	out, err := h.run(&planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name: search.Name, Payload: rawjson.Message(`{"query":"bad"}`),
	}}}, policy.CapsState{
		MaxToolCalls:                        5,
		RemainingToolCalls:                  5,
		MaxConsecutiveFailedToolCalls:       2,
		RemainingConsecutiveFailedToolCalls: 2,
	}, nil)

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "stopped after repeated failures", agentMessageText(out.Final))
	assert.Equal(t, 1, recoveryTurns)
	assert.Len(t, out.ToolEvents, 2)
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

	assert.Equal(t, []tools.Ident{list.Name}, replanUnavailableTools(outputs))
	rt := New()
	seedTestToolSpecs(rt, search, list)
	reminders := rt.recoveryReminders(outputs)
	require.Len(t, reminders, 3)
	assert.Contains(t, reminders[0].Text, "remains available")
	assert.Contains(t, reminders[1].Text, "Do not repeat this rejected request")

	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: search.Name}}}
	require.NoError(t, validateRecoveryCatalog(
		&RecoveryCatalog{Tools: []tools.Ident{search.Name}},
		result,
	))
	require.ErrorContains(t, validateRecoveryCatalog(
		&RecoveryCatalog{Tools: []tools.Ident{list.Name}},
		result,
	), "outside the advertised recovery catalog")
	require.NoError(t, validateRecoveryCatalog(nil, result), "legacy histories have no catalog")

	plainClarification := &planner.PlanResult{Await: planner.NewAwait(
		planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID: "clarify", Question: "Which catalog?",
		}),
	)}
	require.NoError(t, validateRecoveryCatalog(&RecoveryCatalog{Tools: []tools.Ident{search.Name}}, plainClarification))
	toolBackedAwait := &planner.PlanResult{Await: planner.NewAwait(
		planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
			ID:         "tool-clarify",
			ToolName:   list.Name,
			ToolCallID: "call-list",
			Question:   "Which page?",
		}),
	)}
	require.NoError(t, validateRecoveryCatalog(
		&RecoveryCatalog{Tools: []tools.Ident{list.Name}},
		toolBackedAwait,
	))
	require.ErrorContains(t, validateRecoveryCatalog(
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
		func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
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

	out, err := h.run(&planner.PlanResult{ToolCalls: []planner.ToolRequest{
		{Name: search.Name, ToolCallID: "search-call", Payload: rawjson.Message(`{"query":"bad"}`)},
		{Name: load.Name, ToolCallID: "load-call", Payload: rawjson.Message(`{"id":"one"}`)},
	}}, policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4}, nil)

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "partial result", agentMessageText(out.Final))
	assert.Equal(t, 1, resumes)
	assert.Equal(t, []string{"load-call"}, h.workflow.lastPlannerCall.Input.RecoveryToolCallIDs)
}

// newRecoveryHarness assembles one in-memory workflow whose planner and tool
// behavior are supplied by the test. It uses the real resume activity so tests
// cover canonical run-log loading, recovery reminders, and advertised tools.
func newRecoveryHarness(
	t *testing.T,
	name string,
	specs []tools.ToolSpec,
	execute func(context.Context, *planner.ToolRequest) (*planner.ToolResult, error),
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
func (h *recoveryHarness) run(
	initial *planner.PlanResult,
	caps policy.CapsState,
	ctrl *interrupt.Controller,
) (*RunOutput, error) {
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
		ctrl,
	)
}

// invalidCallResult returns a correctable validation failure while preserving
// the executed call's identity.
func invalidCallResult(call *planner.ToolRequest) *planner.ToolResult {
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
func successfulToolResult(call *planner.ToolRequest) *planner.ToolResult {
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
