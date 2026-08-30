package runtime

// This file checks startup validation, limit selection, continuation
// restoration, policy labels, and terminal execution without a planner turn.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestValidateLimitTerminalPlans(t *testing.T) {
	t.Parallel()

	rt := New(newTestStore())
	terminal := strictLimitTerminalSpec()
	reg := AgentRegistration{Definition: testRegistrationDefinition(agent.Ident("service.agent"), engine.WorkflowDefinition{}, []tools.ToolSpec{terminal}), WorkflowHandler: (engine.WorkflowDefinition{}).Handler}
	valid := testLimitTerminalPlans(terminal.Name)

	tests := []struct {
		name  string
		plans *LimitTerminalPlans
		want  string
	}{
		{
			name:  "complete set",
			plans: valid,
		},
		{
			name: "missing time call",
			plans: &LimitTerminalPlans{
				ToolCallCap: valid.ToolCallCap,
				RecoveryCap: valid.RecoveryCap,
			},
			want: "invalid time_budget terminal call",
		},
		{
			name: "unknown field",
			plans: &LimitTerminalPlans{
				TimeBudget: LimitTerminalCall{
					Name:    terminal.Name,
					Payload: rawjson.Message(`{"result":"time","extra":true}`),
				},
				ToolCallCap: valid.ToolCallCap,
				RecoveryCap: valid.RecoveryCap,
			},
			want: "unknown field",
		},
		{
			name: "non-object payload",
			plans: &LimitTerminalPlans{
				TimeBudget: LimitTerminalCall{
					Name:    terminal.Name,
					Payload: rawjson.Message(`"time"`),
				},
				ToolCallCap: valid.ToolCallCap,
				RecoveryCap: valid.RecoveryCap,
			},
			want: "payload must be a JSON object",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := rt.validateLimitTerminalPlans(reg, test.plans)
			if test.want == "" {
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestValidateLimitTerminalPlansRejectsNonTerminalTool(t *testing.T) {
	t.Parallel()

	rt := New(newTestStore())
	ordinary := strictLimitTerminalSpec()
	ordinary.TerminalRun = false
	reg := AgentRegistration{Definition: testRegistrationDefinition(agent.Ident("service.agent"), engine.WorkflowDefinition{}, []tools.ToolSpec{ordinary}), WorkflowHandler: (engine.WorkflowDefinition{}).Handler}

	err := rt.validateLimitTerminalPlans(reg, testLimitTerminalPlans(ordinary.Name))
	require.ErrorContains(t, err, "is not a terminal bookkeeping tool")
}

func TestValidateLimitTerminalPlansRejectsConfirmation(t *testing.T) {
	t.Parallel()

	terminal := strictLimitTerminalSpec()
	reg := AgentRegistration{Definition: testRegistrationDefinition(agent.Ident("service.agent"), engine.WorkflowDefinition{}, []tools.ToolSpec{terminal}), WorkflowHandler: (engine.WorkflowDefinition{}).Handler}
	t.Run("design requirement", func(t *testing.T) {
		withConfirmation := terminal
		withConfirmation.Confirmation = &tools.ConfirmationSpec{}
		reg.Definition = testAgentDefinition("service.agent", "service.agent.workflow", "test", []tools.ToolSpec{withConfirmation}, nil)

		err := New(newTestStore()).validateLimitTerminalPlans(reg, testLimitTerminalPlans(terminal.Name))
		require.ErrorContains(t, err, "requires confirmation")
	})
}

func TestExecuteWorkflowRejectsInvalidLimitPlansBeforePlanning(t *testing.T) {
	t.Parallel()

	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	terminal := strictLimitTerminalSpec()
	reg := AgentRegistration{Definition: testRegistrationDefinition("service.agent", engine.WorkflowDefinition{}, []tools.ToolSpec{terminal}), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, Planner: &stubPlanner{}}
	rt.agents[reg.Definition.route.ID] = reg
	input := &RunInput{
		AgentID: reg.Definition.route.ID,
		RunID:   "run-1",
		Policy: &PolicyOverrides{
			LimitTerminalPlans: &LimitTerminalPlans{},
		},
	}
	wfCtx := &routeWorkflowContext{
		ctx:   context.Background(),
		runID: input.RunID,
	}

	_, err := rt.ExecuteWorkflow(wfCtx, input)
	require.ErrorContains(t, err, "invalid time_budget terminal call")
	assert.Empty(t, wfCtx.lastPlannerCall.Name)
}

func TestLimitTerminalCallSelectsOnlyConfiguredLimits(t *testing.T) {
	t.Parallel()

	plans := testLimitTerminalPlans("service.tools.complete")
	tests := []struct {
		reason planner.TerminationReason
		want   string
		found  bool
	}{
		{reason: planner.TerminationReasonTimeBudget, want: "time", found: true},
		{reason: planner.TerminationReasonToolCap, want: "tools", found: true},
		{reason: planner.TerminationReasonRecoveryCap, want: "failures", found: true},
		{reason: planner.TerminationReasonToolFailure},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			call, found, err := limitTerminalCall(plans, test.reason)
			require.NoError(t, err)
			assert.Equal(t, test.found, found)
			if found {
				assert.JSONEq(t, `{"result":"`+test.want+`"}`, string(call.Payload))
			}
		})
	}

	_, _, err := limitTerminalCall(plans, planner.TerminationReason("unknown"))
	require.ErrorContains(t, err, "unsupported termination reason")
}

func TestLimitTerminalToolRequestLeavesRuntimeLabelsUnset(t *testing.T) {
	t.Parallel()

	call := LimitTerminalCall{
		Name:    "service.tools.complete",
		Payload: rawjson.Message(`{"result":"time"}`),
	}
	request := limitTerminalToolRequest(
		"run-1",
		"turn-1",
		2,
		call,
	)

	assert.Empty(t, request.Labels)
	assert.Empty(t, request.RunID)
	assert.NotEmpty(t, request.ToolCallID)
}

func TestWithLimitTerminalPlansCopiesPayloads(t *testing.T) {
	t.Parallel()

	plans := testLimitTerminalPlans("service.tools.complete")
	input := &RunInput{}
	WithLimitTerminalPlans(*plans)(input)
	plans.TimeBudget.Payload[0] = '['

	require.NotNil(t, input.Policy)
	require.NotNil(t, input.Policy.LimitTerminalPlans)
	assert.JSONEq(t, `{"result":"time"}`, string(input.Policy.LimitTerminalPlans.TimeBudget.Payload))
}

func TestRestoreContinuationRunInputCopiesLimitTerminalPlans(t *testing.T) {
	t.Parallel()

	plans := testLimitTerminalPlans("service.tools.complete")
	await := planner.AwaitClarificationItem(&planner.AwaitClarification{
		ID:       "clarification-1",
		Question: "Which facility?",
	})
	checkpoint := &workflowCheckpoint{
		AgentID:       "service.agent",
		SessionID:     "session-1",
		PreviousRunID: "run-1",
		Pending: []checkpointPendingInput{{
			Await: &await,
		}},
		Policy: &PolicyOverrides{
			LimitTerminalPlans: plans,
		},
	}
	input := &RunInput{
		AgentID:   "service.agent",
		RunID:     "run-2",
		SessionID: "session-1",
		Continuation: &api.RunContinuationInput{
			Response: &api.PendingInputResponse{
				Clarification: &api.ClarificationAnswer{ID: "clarification-1"},
			},
		},
	}

	require.NoError(t, restoreContinuationRunInput(input, checkpoint))
	require.NotNil(t, input.Policy)
	require.NotNil(t, input.Policy.LimitTerminalPlans)
	checkpoint.Policy.LimitTerminalPlans.TimeBudget.Payload[0] = '['
	assert.JSONEq(t, `{"result":"time"}`, string(input.Policy.LimitTerminalPlans.TimeBudget.Payload))
}

func TestContinuationRejectsLimitTerminalPlanOverride(t *testing.T) {
	t.Parallel()

	rt := New(newTestStore(), WithEngine(&stubEngine{}))
	input := &RunInput{
		AgentID: "service.agent",
		Policy: &PolicyOverrides{
			LimitTerminalPlans: testLimitTerminalPlans("service.tools.complete"),
		},
		Continuation: &api.RunContinuationInput{
			Response: &api.PendingInputResponse{
				Clarification: &api.ClarificationAnswer{},
			},
		},
	}

	_, err := rt.startRunOn(t.Context(), input, "workflow", "queue", false)
	require.ErrorContains(t, err, "cannot include caller-supplied checkpoint state")
}

func TestToolFailureUsesPlannerWhenLimitPlansExist(t *testing.T) {
	t.Parallel()

	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	input := &RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Policy: &PolicyOverrides{
			LimitTerminalPlans: testLimitTerminalPlans("service.tools.complete"),
		},
	}
	seedRunMeta(t, rt, input)
	reg := AgentRegistration{Definition: testRegistrationDefinition(input.AgentID, engine.WorkflowDefinition{}, nil), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, ResumeActivityName: "resume"}
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       input.RunID,
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": func(_ context.Context, call *PlanActivityInput) (*PlanActivityOutput, error) {
				if call.Finalize == nil {
					return nil, errors.New("finalization input is required")
				}
				if call.Finalize.Reason != planner.TerminationReasonToolFailure {
					return nil, errors.New("tool failure finalization reason is required")
				}
				return &PlanActivityOutput{
					PublicationBatchID: testPublicationBatchID,
					Result: &PlanResult{
						FinalResponse: &planner.FinalResponse{
							Message: &model.Message{
								Role:  model.ConversationRoleAssistant,
								Parts: []model.Part{model.TextPart{Text: "The tool failed."}},
							},
						},
					},
				}, nil
			},
		},
	}
	base := &planner.PlanInput{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Run the tool."}},
		}},
		RunContext: run.Context{
			RunID:     input.RunID,
			SessionID: input.SessionID,
			TurnID:    input.TurnID,
			Attempt:   1,
		},
	}

	out, err := rt.finalizeRun(
		wfCtx,
		reg,
		input,
		base,
		nil,
		nil,
		model.TokenUsage{},
		initialCaps(RunPolicy{}),
		2,
		input.TurnID,
		nil,
		planner.TerminationReasonToolFailure,
		wfCtx.Now().Add(time.Minute),
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
}

func TestWorkflowLimitsExecuteTerminalPlansWithoutPlannerResume(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		reason     planner.TerminationReason
		result     string
		withLabels bool
	}{
		{
			name:       "time budget with conflicting labels",
			reason:     planner.TerminationReasonTimeBudget,
			result:     "time",
			withLabels: true,
		},
		{
			name:       "tool cap with conflicting labels",
			reason:     planner.TerminationReasonToolCap,
			result:     "tools",
			withLabels: true,
		},
		{
			name:       "recovery cap with conflicting labels",
			reason:     planner.TerminationReasonRecoveryCap,
			result:     "failures",
			withLabels: true,
		},
		{
			name:   "time budget with empty labels",
			reason: planner.TerminationReasonTimeBudget,
			result: "time",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executed := executeWorkflowLimitTerminalPlan(t, test.reason, test.withLabels)
			assert.JSONEq(t, `{"result":"`+test.result+`"}`, string(executed.Payload))
			assert.Equal(t, string(test.reason), executed.Labels[FinalizationReasonLabel])
			if !test.withLabels {
				assert.Equal(t, map[string]string{
					FinalizationReasonLabel: string(test.reason),
				}, executed.Labels)
			}
		})
	}
}

// executeWorkflowLimitTerminalPlan drives the complete workflow transition for
// one limit and returns the fixed terminal request received by the tool.
func executeWorkflowLimitTerminalPlan(
	t *testing.T,
	reason planner.TerminationReason,
	withLabels bool,
) *ToolCall {
	t.Helper()

	rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
	if withLabels {
		rt.Policy = limitTerminalLabelPolicy{}
	}
	terminal := strictLimitTerminalSpec()
	work := newAnyJSONSpec("service.tools.work")
	var executed *ToolCall
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "service.tools",
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == work.Name {
				assert.NotContains(t, call.Labels, FinalizationReasonLabel)
				result := &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Result:     map[string]any{"worked": true},
				}
				if reason == planner.TerminationReasonRecoveryCap {
					result.Result = nil
					result.Failure = testToolFailure(
						planner.FailureInvalidCall,
						planner.RecoveryCorrectCall,
						"work failed",
					)
				}
				return result, nil
			}
			executed = call
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"recorded": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{work, terminal},
		ToolMetadataLookup: func(name tools.Ident) (policy.ToolMetadata, bool) {
			if name == work.Name {
				return policy.ToolMetadata{
					ID:          name,
					Title:       "Work",
					BudgetClass: policy.ToolBudgetClassBudgeted,
				}, true
			}
			return policy.ToolMetadata{
				ID:          name,
				Title:       "Complete",
				BudgetClass: policy.ToolBudgetClassBookkeeping,
			}, true
		},
	}))
	reg := AgentRegistration{Definition: testRegistrationDefinition("service.agent", engine.WorkflowDefinition{}, []tools.ToolSpec{work, terminal}), WorkflowHandler: (engine.WorkflowDefinition{}).Handler, PlanActivityName: "plan",
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
		Policy: RunPolicy{
			FinalizerGrace: time.Minute,
		},
	}
	switch reason {
	case planner.TerminationReasonTimeBudget:
		reg.Policy.TimeBudget = time.Second
	case planner.TerminationReasonToolCap:
		reg.Policy.MaxToolCalls = 1
	case planner.TerminationReasonRecoveryCap:
		reg.Policy.MaxToolCalls = 2
		reg.Policy.MaxRecoveryTurns = 1
	case planner.TerminationReasonToolFailure:
		t.Fatal("tool failure is not a runtime limit")
	default:
		t.Fatalf("unsupported reason %q", reason)
	}
	rt.agents[reg.Definition.route.ID] = reg
	input := &RunInput{
		AgentID:   reg.Definition.route.ID,
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Do the work."}},
		}},
		Policy: &PolicyOverrides{
			LimitTerminalPlans: testLimitTerminalPlans(terminal.Name),
		},
	}
	if withLabels {
		input.Labels = map[string]string{
			"account":               "caller-account",
			FinalizationReasonLabel: "incorrect-run-value",
		}
	}
	_, err := createSessionForTest(context.Background(), rt.Store, input.SessionID)
	require.NoError(t, err)
	current := time.Unix(100, 0)
	plannerCalls := 0
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       input.RunID,
		now:         func() time.Time { return current },
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				plannerCalls++
				if reason == planner.TerminationReasonTimeBudget {
					current = current.Add(time.Second)
					return nil, engine.ErrPlannerActivityDeadlineExceeded
				}
				callID := fmt.Sprintf("work-call-%d", plannerCalls)
				return &PlanActivityOutput{
					PublicationBatchID: testPublicationBatchID,
					Result: &PlanResult{
						ToolCalls: []ToolCall{{
							ToolCallID:      callID,
							ModelToolCallID: callID,
							Name:            work.Name,
							Payload:         rawjson.Message(`{}`),
						}},
					},
				}, nil
			},
			"resume": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				plannerCalls++
				if reason == planner.TerminationReasonToolCap ||
					reason == planner.TerminationReasonRecoveryCap {
					callID := fmt.Sprintf("work-call-%d", plannerCalls)
					output := &PlanActivityOutput{
						PublicationBatchID: testPublicationBatchID,
						Result: &PlanResult{
							ToolCalls: []ToolCall{{
								ToolCallID:      callID,
								ModelToolCallID: callID,
								Name:            work.Name,
								Payload:         rawjson.Message(`{}`),
							}},
						},
					}
					if reason == planner.TerminationReasonRecoveryCap {
						output.RecoveryCatalog = &RecoveryCatalog{Tools: []tools.Ident{work.Name}}
					}
					return output, nil
				}
				return nil, errors.New("fixed limit call must not resume the planner")
			},
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, input)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.NotNil(t, executed)
	expectedPlannerCalls := 1
	if reason == planner.TerminationReasonToolCap ||
		reason == planner.TerminationReasonRecoveryCap {
		expectedPlannerCalls = 2
	}
	assert.Equal(t, expectedPlannerCalls, plannerCalls)
	assert.Equal(t, terminal.Name, executed.Name)
	if withLabels {
		assert.Equal(t, "policy-account", executed.Labels["account"])
		assert.Equal(t, "policy-value", executed.Labels["policy"])
	}
	assert.Equal(t, string(reason), executed.Labels[FinalizationReasonLabel])
	assert.NotEmpty(t, executed.ToolCallID)
	return executed
}

// limitTerminalLabelPolicy adds one label while the terminal call is prepared.
type limitTerminalLabelPolicy struct{}

// Decide requires the same valid active recovery state used during ordinary
// tool execution, then returns it unchanged with labels for the execution
// assertion.
func (limitTerminalLabelPolicy) Decide(_ context.Context, input policy.Input) (policy.Decision, error) {
	if input.RemainingCaps.MaxRecoveryTurns <= 0 {
		return policy.Decision{}, errors.New("finalization policy received no recovery maximum")
	}
	return policy.Decision{
		Caps: input.RemainingCaps,
		Labels: map[string]string{
			"account":               "policy-account",
			"policy":                "policy-value",
			FinalizationReasonLabel: "incorrect-policy-value",
		},
	}, nil
}

// strictLimitTerminalSpec returns a terminal tool whose payload accepts one
// required result field and rejects unknown fields.
func strictLimitTerminalSpec() tools.ToolSpec {
	type payload struct {
		Result string `json:"result"`
	}
	return tools.ToolSpec{
		Name:        "service.tools.complete",
		Bookkeeping: true,
		TerminalRun: true,
		Payload: tools.TypeSpec{
			Codec: tools.JSONCodec[any]{
				FromJSON: func(data []byte) (any, error) {
					decoder := json.NewDecoder(bytes.NewReader(data))
					decoder.DisallowUnknownFields()
					var value payload
					if err := decoder.Decode(&value); err != nil {
						return nil, err
					}
					if value.Result == "" {
						return nil, errors.New("result is required")
					}
					return value, nil
				},
			},
		},
		Result: tools.TypeSpec{Codec: tools.AnyJSONCodec},
	}
}

// testLimitTerminalPlans assigns a distinct valid payload to each runtime
// limit.
func testLimitTerminalPlans(name tools.Ident) *LimitTerminalPlans {
	return &LimitTerminalPlans{
		TimeBudget: LimitTerminalCall{
			Name:    name,
			Payload: rawjson.Message(`{"result":"time"}`),
		},
		ToolCallCap: LimitTerminalCall{
			Name:    name,
			Payload: rawjson.Message(`{"result":"tools"}`),
		},
		RecoveryCap: LimitTerminalCall{
			Name:    name,
			Payload: rawjson.Message(`{"result":"failures"}`),
		},
	}
}
