package runtime

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestPlanLimitFinalizationActivityPassesClosedLimitReason(t *testing.T) {
	tests := []struct {
		name string
		wire planner.LimitTerminationReason
		want planner.LimitTerminationReason
	}{
		{
			name: "time budget",
			wire: planner.LimitTerminationReasonTimeBudget,
			want: planner.LimitTerminationReasonTimeBudget,
		},
		{
			name: "tool cap",
			wire: planner.LimitTerminationReasonToolCap,
			want: planner.LimitTerminationReasonToolCap,
		},
		{
			name: "failure cap",
			wire: planner.LimitTerminationReasonFailureCap,
			want: planner.LimitTerminationReasonFailureCap,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pl := &stubPlanner{
				limitFinalize: func(
					_ context.Context,
					input *planner.LimitFinalizationInput,
				) (planner.LimitFinalizationDecision, error) {
					require.Equal(t, test.want, input.Reason)
					return planner.TerminalLimitFinalization(*finalPlannerResult("stopped")), nil
				},
			}
			rt := newTestRuntimeWithPlanner("service.agent", pl)

			out, err := rt.PlanLimitFinalizationActivity(t.Context(), &LimitFinalizationActivityInput{
				AgentID: "service.agent",
				PlannerInput: planner.LimitFinalizationInput{
					RunID:  "run-123",
					Reason: test.wire,
				},
			})

			require.NoError(t, err)
			require.NotNil(t, out.TerminalPlan().FinalResponse)
			require.Equal(t, api.LimitFinalizationDispositionTerminalPlan, out.Disposition())
		})
	}
}

func TestPlanLimitFinalizationActivityReturnsExplicitHistoryDecision(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})

	out, err := rt.PlanLimitFinalizationActivity(t.Context(), &LimitFinalizationActivityInput{
		AgentID: "service.agent",
		PlannerInput: planner.LimitFinalizationInput{
			RunID:  "run-123",
			Reason: planner.LimitTerminationReasonToolCap,
		},
	})

	require.NoError(t, err)
	require.Equal(t, api.LimitFinalizationDispositionHistoryRequired, out.Disposition())
	require.Nil(t, out.TerminalPlan())
}

func TestPlanLimitFinalizationActivityRejectsNonLimitInputs(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	tests := []struct {
		name  string
		input LimitFinalizationActivityInput
		want  string
	}{
		{
			name: "missing reason",
			input: LimitFinalizationActivityInput{
				AgentID: "service.agent",
				PlannerInput: planner.LimitFinalizationInput{
					RunID: "run-123",
				},
			},
			want: "unsupported termination reason",
		},
		{
			name: "tool failure",
			input: LimitFinalizationActivityInput{
				AgentID: "service.agent",
				PlannerInput: planner.LimitFinalizationInput{
					RunID: "run-123",
					Reason: planner.LimitTerminationReason(
						planner.TerminationReasonToolFailure,
					),
				},
			},
			want: "requires message history",
		},
		{
			name: "unknown reason",
			input: LimitFinalizationActivityInput{
				AgentID: "service.agent",
				PlannerInput: planner.LimitFinalizationInput{
					RunID:  "run-123",
					Reason: planner.LimitTerminationReason("unknown"),
				},
			},
			want: "unsupported termination reason",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := rt.PlanLimitFinalizationActivity(t.Context(), &test.input)
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestPlanLimitFinalizationActivityRejectsInvalidReasonBeforeEndedSession(t *testing.T) {
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	_, err := rt.CreateSession(t.Context(), "session-ended")
	require.NoError(t, err)
	_, err = rt.DeleteSession(t.Context(), "session-ended")
	require.NoError(t, err)

	tests := []struct {
		name   string
		reason planner.LimitTerminationReason
		want   string
	}{
		{
			name:   "unknown reason",
			reason: planner.LimitTerminationReason("unknown"),
			want:   "unsupported termination reason",
		},
		{
			name:   "tool failure",
			reason: planner.LimitTerminationReason(planner.TerminationReasonToolFailure),
			want:   "requires message history",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := rt.PlanLimitFinalizationActivity(t.Context(), &LimitFinalizationActivityInput{
				AgentID:   "service.agent",
				SessionID: "session-ended",
				PlannerInput: planner.LimitFinalizationInput{
					RunID:  "run-123",
					Reason: test.reason,
				},
			})
			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestPlanLimitFinalizationActivityRejectsMissingAgentAndDecision(t *testing.T) {
	t.Run("missing agent", func(t *testing.T) {
		rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
		delete(rt.agents, "service.agent")

		_, err := rt.PlanLimitFinalizationActivity(t.Context(), &LimitFinalizationActivityInput{
			AgentID: "service.agent",
			PlannerInput: planner.LimitFinalizationInput{
				RunID:  "run-123",
				Reason: planner.LimitTerminationReasonFailureCap,
			},
		})

		require.ErrorIs(t, err, ErrAgentNotFound)
	})

	t.Run("nil decision", func(t *testing.T) {
		rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
			limitFinalize: func(
				context.Context,
				*planner.LimitFinalizationInput,
			) (planner.LimitFinalizationDecision, error) {
				return nil, nil
			},
		})

		_, err := rt.PlanLimitFinalizationActivity(t.Context(), &LimitFinalizationActivityInput{
			AgentID: "service.agent",
			PlannerInput: planner.LimitFinalizationInput{
				RunID:  "run-123",
				Reason: planner.LimitTerminationReasonFailureCap,
			},
		})

		require.ErrorContains(t, err, "nil decision")
	})

	t.Run("streamed terminal plan", func(t *testing.T) {
		rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
			limitFinalize: func(
				context.Context,
				*planner.LimitFinalizationInput,
			) (planner.LimitFinalizationDecision, error) {
				result := finalPlannerResult("stopped")
				result.Streamed = true
				return planner.TerminalLimitFinalization(*result), nil
			},
		})

		_, err := rt.PlanLimitFinalizationActivity(t.Context(), &LimitFinalizationActivityInput{
			AgentID: "service.agent",
			PlannerInput: planner.LimitFinalizationInput{
				RunID:  "run-123",
				Reason: planner.LimitTerminationReasonFailureCap,
			},
		})

		require.ErrorContains(t, err, "cannot return a streamed result")
	})
}

func TestPlanLimitFinalizationActivityRejectsMalformedTerminalPlans(t *testing.T) {
	tests := []struct {
		name   string
		result planner.PlanResult
		want   string
	}{
		{
			name: "missing final message",
			result: planner.PlanResult{
				FinalResponse: &planner.FinalResponse{},
			},
			want: "missing its message",
		},
		{
			name: "tool use in final response",
			result: planner.PlanResult{
				FinalResponse: &planner.FinalResponse{Message: &model.Message{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.ToolUsePart{
						ID:    "call-1",
						Name:  "search",
						Input: rawjson.Message(`{"query":"status"}`),
					}},
				}},
			},
			want: "contains tool use",
		},
		{
			name: "malformed terminal tool payload",
			result: planner.PlanResult{
				ToolCalls: []planner.ToolRequest{{
					Name:    "terminal",
					Payload: rawjson.Message(`{"invalid"`),
				}},
			},
			want: "payload is not valid JSON",
		},
		{
			name: "malformed final tool result",
			result: planner.PlanResult{
				FinalToolResult: &planner.FinalToolResult{
					Result: rawjson.Message(`{"invalid"`),
				},
			},
			want: "final tool result is not valid JSON",
		},
		{
			name: "malformed final tool server data",
			result: planner.PlanResult{
				FinalToolResult: &planner.FinalToolResult{
					Result:     rawjson.Message(`{"status":"ok"}`),
					ServerData: rawjson.Message(`[`),
				},
			},
			want: "final tool server data is not valid JSON",
		},
		{
			name: "omitted final tool result contains bytes",
			result: planner.PlanResult{
				FinalToolResult: &planner.FinalToolResult{
					Result:              rawjson.Message(`{"status":"ok"}`),
					ResultOmitted:       true,
					ResultOmittedReason: "workflow_budget",
				},
			},
			want: "marked omitted but contains a result",
		},
		{
			name: "omitted final tool result lacks reason",
			result: planner.PlanResult{
				FinalToolResult: &planner.FinalToolResult{
					ResultOmitted: true,
				},
			},
			want: "marked omitted without a reason",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{
				limitFinalize: func(
					context.Context,
					*planner.LimitFinalizationInput,
				) (planner.LimitFinalizationDecision, error) {
					return planner.TerminalLimitFinalization(test.result), nil
				},
			})

			_, err := rt.PlanLimitFinalizationActivity(t.Context(), &LimitFinalizationActivityInput{
				AgentID: "service.agent",
				PlannerInput: planner.LimitFinalizationInput{
					RunID:  "run-123",
					Reason: planner.LimitTerminationReasonFailureCap,
				},
			})

			require.ErrorContains(t, err, test.want)
		})
	}
}

func TestRunLimitFinalizationActivityRejectsMalformedWorkerOutput(t *testing.T) {
	rt := New()
	wfCtx := &routeWorkflowContext{
		ctx: context.Background(),
		limitRoutes: map[string]func(
			context.Context,
			*LimitFinalizationActivityInput,
		) (*LimitFinalizationActivityOutput, error){
			"resume.limit_finalization": func(
				ctx context.Context,
				_ *LimitFinalizationActivityInput,
			) (*LimitFinalizationActivityOutput, error) {
				return api.TerminalLimitFinalizationActivityOutput(planner.PlanResult{
					FinalResponse: &planner.FinalResponse{},
				}), ctx.Err()
			},
		},
	}

	_, _, err := rt.runLimitFinalizationActivity(
		wfCtx,
		"resume.limit_finalization",
		engine.ActivityOptions{},
		LimitFinalizationActivityInput{},
		time.Time{},
	)

	require.ErrorContains(t, err, "missing its message")
}

func TestFinalizeWithPlannerDoesNotLoadOrValidateMessagesForTerminalLimitPlan(t *testing.T) {
	rt, terminalTool, wfCtx := newTerminalFinalizationRuntime(t)
	wfCtx.plannerRoutes["resume"] = func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
		t.Fatal("history-backed resume must not run")
		return nil, nil
	}
	wfCtx.limitRoutes["resume.limit_finalization"] = func(
		ctx context.Context,
		input *LimitFinalizationActivityInput,
	) (*LimitFinalizationActivityOutput, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		require.Equal(t, "policy-account", input.PlannerInput.Labels["account"])
		require.Equal(t, "basic", input.PlannerInput.Labels["policy_engine"])
		return api.TerminalLimitFinalizationActivityOutput(planner.PlanResult{
			ToolCalls: []planner.ToolRequest{{
				Name:    terminalTool.Name,
				Payload: rawjson.Message(`{}`),
			}},
		}), nil
	}
	base := &planner.PlanInput{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.TextPart{Text: strings.Repeat("x", maxPlanActivityInputBytes+1)},
				model.ToolUsePart{
					ID:    "unfinished-call",
					Name:  "search",
					Input: rawjson.Message(`{"query":"status"}`),
				},
			},
		}},
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
			Labels: map[string]string{
				"account":       "policy-account",
				"policy_engine": "basic",
			},
		},
	}
	input := &RunInput{
		AgentID:   "agent-1",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}

	out, err := rt.finalizeWithPlanner(
		wfCtx,
		AgentRegistration{
			ExecuteToolActivity: "execute",
			ResumeActivityName:  "resume",
		},
		input,
		base,
		nil,
		nil,
		model.TokenUsage{},
		2,
		"turn-1",
		nil,
		planner.TerminationReasonFailureCap,
		time.Time{},
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Len(t, out.ToolEvents, 1)
	require.Equal(t, "resume.limit_finalization", wfCtx.lastLimitCall.Name)
}

func TestFinalizeWithPlannerLoadsMessagesOnlyWhenPlannerRequestsThem(t *testing.T) {
	rt, _, wfCtx := newTerminalFinalizationRuntime(t)
	wfCtx.limitRoutes["resume.limit_finalization"] = historyRequiredLimitActivity
	wfCtx.plannerRoutes["resume"] = func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error) {
		t.Fatal("invalid saved messages must be rejected before PlanResume")
		return nil, nil
	}
	base := &planner.PlanInput{
		Messages: []*model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "unfinished-call",
				Name:  "search",
				Input: rawjson.Message(`{"query":"status"}`),
			}},
		}},
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}

	_, err := rt.finalizeWithPlanner(
		wfCtx,
		AgentRegistration{ResumeActivityName: "resume"},
		&RunInput{
			AgentID:   "agent-1",
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
		},
		base,
		nil,
		nil,
		model.TokenUsage{},
		2,
		"turn-1",
		nil,
		planner.TerminationReasonFailureCap,
		time.Time{},
	)

	require.ErrorContains(t, err, "must be followed by user tool_result")
}
