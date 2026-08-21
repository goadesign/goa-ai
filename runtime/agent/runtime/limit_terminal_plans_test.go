package runtime

// This file checks startup validation, limit selection, continuation
// restoration, policy labels, and terminal execution without a planner turn.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

	rt := New()
	terminal := strictLimitTerminalSpec()
	reg := AgentRegistration{
		ID:    agent.Ident("service.agent"),
		Specs: []tools.ToolSpec{terminal},
	}
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
				ToolCallCap:       valid.ToolCallCap,
				FailedToolCallCap: valid.FailedToolCallCap,
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
				ToolCallCap:       valid.ToolCallCap,
				FailedToolCallCap: valid.FailedToolCallCap,
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
				ToolCallCap:       valid.ToolCallCap,
				FailedToolCallCap: valid.FailedToolCallCap,
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

	rt := New()
	ordinary := strictLimitTerminalSpec()
	ordinary.TerminalRun = false
	reg := AgentRegistration{
		ID:    agent.Ident("service.agent"),
		Specs: []tools.ToolSpec{ordinary},
	}

	err := rt.validateLimitTerminalPlans(reg, testLimitTerminalPlans(ordinary.Name))
	require.ErrorContains(t, err, "is not a terminal bookkeeping tool")
}

func TestValidateLimitTerminalPlansRejectsConfirmation(t *testing.T) {
	t.Parallel()

	terminal := strictLimitTerminalSpec()
	reg := AgentRegistration{
		ID:    agent.Ident("service.agent"),
		Specs: []tools.ToolSpec{terminal},
	}
	t.Run("design requirement", func(t *testing.T) {
		withConfirmation := terminal
		withConfirmation.Confirmation = &tools.ConfirmationSpec{}
		reg.Specs = []tools.ToolSpec{withConfirmation}

		err := New().validateLimitTerminalPlans(reg, testLimitTerminalPlans(terminal.Name))
		require.ErrorContains(t, err, "requires confirmation")
	})
	t.Run("runtime requirement", func(t *testing.T) {
		reg.Specs = []tools.ToolSpec{terminal}
		rt := New(WithToolConfirmation(&ToolConfirmationConfig{
			Confirm: map[tools.Ident]*ToolConfirmation{
				terminal.Name: {
					Prompt: func(context.Context, *planner.ToolRequest) (string, error) {
						return "confirm", nil
					},
					DeniedResult: func(context.Context, *planner.ToolRequest) (any, error) {
						return map[string]any{"denied": true}, nil
					},
				},
			},
		}))

		err := rt.validateLimitTerminalPlans(reg, testLimitTerminalPlans(terminal.Name))
		require.ErrorContains(t, err, "requires confirmation")
	})
}

func TestExecuteWorkflowRejectsInvalidLimitPlansBeforePlanning(t *testing.T) {
	t.Parallel()

	rt := New(WithLogger(telemetry.NoopLogger{}))
	terminal := strictLimitTerminalSpec()
	reg := AgentRegistration{
		ID:      "service.agent",
		Planner: &stubPlanner{},
		Specs:   []tools.ToolSpec{terminal},
	}
	rt.agents[reg.ID] = reg
	input := &RunInput{
		AgentID: reg.ID,
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
		{reason: planner.TerminationReasonFailureCap, want: "failures", found: true},
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

func TestLimitTerminalToolRequestRecordsReasonOnly(t *testing.T) {
	t.Parallel()

	call := LimitTerminalCall{
		Name:    "service.tools.complete",
		Payload: rawjson.Message(`{"result":"time"}`),
	}
	request := limitTerminalToolRequest(call, planner.TerminationReasonTimeBudget)

	assert.Len(t, request.Labels, 1)
	assert.Equal(t, string(planner.TerminationReasonTimeBudget), request.Labels[LimitReasonLabel])
	assert.Empty(t, request.RunID)
	assert.Empty(t, request.ToolCallID)
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
	checkpoint := &workflowCheckpoint{
		AgentID:   "service.agent",
		SessionID: "session-1",
		BaseContext: run.Context{
			RunID: "run-1",
		},
		Policy: &PolicyOverrides{
			LimitTerminalPlans: plans,
		},
	}
	input := &RunInput{
		AgentID:   "service.agent",
		RunID:     "run-2",
		SessionID: "session-1",
	}

	require.NoError(t, restoreContinuationRunInput(input, checkpoint))
	require.NotNil(t, input.Policy)
	require.NotNil(t, input.Policy.LimitTerminalPlans)
	checkpoint.Policy.LimitTerminalPlans.TimeBudget.Payload[0] = '['
	assert.JSONEq(t, `{"result":"time"}`, string(input.Policy.LimitTerminalPlans.TimeBudget.Payload))
}

func TestContinuationRejectsLimitTerminalPlanOverride(t *testing.T) {
	t.Parallel()

	rt := New(WithEngine(&stubEngine{}))
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

	rt := New(WithLogger(telemetry.NoopLogger{}))
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
	reg := AgentRegistration{
		ID:                 input.AgentID,
		ResumeActivityName: "resume",
	}
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
					Result: &planner.PlanResult{
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
		reason planner.TerminationReason
		result string
	}{
		{reason: planner.TerminationReasonTimeBudget, result: "time"},
		{reason: planner.TerminationReasonToolCap, result: "tools"},
		{reason: planner.TerminationReasonFailureCap, result: "failures"},
	}
	for _, test := range tests {
		t.Run(string(test.reason), func(t *testing.T) {
			executed := executeWorkflowLimitTerminalPlan(t, test.reason)
			assert.JSONEq(t, `{"result":"`+test.result+`"}`, string(executed.Payload))
			assert.Equal(t, string(test.reason), executed.Labels[LimitReasonLabel])
		})
	}
}

// executeWorkflowLimitTerminalPlan drives the complete workflow transition for
// one limit and returns the fixed terminal request received by the tool.
func executeWorkflowLimitTerminalPlan(t *testing.T, reason planner.TerminationReason) *planner.ToolRequest {
	t.Helper()

	rt := New(
		WithLogger(telemetry.NoopLogger{}),
		WithPolicy(limitTerminalLabelPolicy{}),
	)
	terminal := strictLimitTerminalSpec()
	work := newAnyJSONSpec("service.tools.work", terminal.Toolset)
	var executed *planner.ToolRequest
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: terminal.Toolset,
		Execute: wrapExecute(func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			if call.Name == work.Name {
				result := &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Result:     map[string]any{"worked": true},
				}
				if reason == planner.TerminationReasonFailureCap {
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
	reg := AgentRegistration{
		ID:                  "service.agent",
		Specs:               []tools.ToolSpec{work, terminal},
		PlanActivityName:    "plan",
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
	case planner.TerminationReasonFailureCap:
		reg.Policy.MaxToolCalls = 2
		reg.Policy.MaxConsecutiveFailedToolCalls = 1
	case planner.TerminationReasonToolFailure:
		t.Fatal("tool failure is not a runtime limit")
	default:
		t.Fatalf("unsupported reason %q", reason)
	}
	rt.agents[reg.ID] = reg
	input := &RunInput{
		AgentID:   reg.ID,
		RunID:     "run-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Do the work."}},
		}},
		Labels: map[string]string{"account": "caller-account"},
		Policy: &PolicyOverrides{
			LimitTerminalPlans: testLimitTerminalPlans(terminal.Name),
		},
	}
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
				return &PlanActivityOutput{
					Result: &planner.PlanResult{
						ToolCalls: []planner.ToolRequest{{
							Name:    work.Name,
							Payload: rawjson.Message(`{}`),
						}},
					},
				}, nil
			},
			"resume": func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
				plannerCalls++
				if reason == planner.TerminationReasonToolCap {
					return &PlanActivityOutput{
						Result: &planner.PlanResult{
							ToolCalls: []planner.ToolRequest{{
								Name:    work.Name,
								Payload: rawjson.Message(`{}`),
							}},
						},
					}, nil
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
	if reason == planner.TerminationReasonToolCap {
		expectedPlannerCalls = 2
	}
	assert.Equal(t, expectedPlannerCalls, plannerCalls)
	assert.Equal(t, terminal.Name, executed.Name)
	assert.Equal(t, "policy-account", executed.Labels["account"])
	assert.Equal(t, "policy-value", executed.Labels["policy"])
	assert.Equal(t, string(reason), executed.Labels[LimitReasonLabel])
	assert.NotEmpty(t, executed.ToolCallID)
	return executed
}

// limitTerminalLabelPolicy adds one label while the terminal call is prepared.
type limitTerminalLabelPolicy struct{}

// Decide returns the configured caps unchanged and adds a label used by the
// execution assertion.
func (limitTerminalLabelPolicy) Decide(_ context.Context, input policy.Input) (policy.Decision, error) {
	return policy.Decision{
		Caps: input.RemainingCaps,
		Labels: map[string]string{
			"account":        "policy-account",
			"policy":         "policy-value",
			LimitReasonLabel: "incorrect-policy-value",
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
		Toolset:     "service.tools",
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
		FailedToolCallCap: LimitTerminalCall{
			Name:    name,
			Payload: rawjson.Message(`{"result":"failures"}`),
		},
	}
}
