package runtime

// workflow_bookkeeping_contract_test.go verifies the stronger bookkeeping
// contract: bookkeeping-only batches must either finish or await in the same
// turn, or fail fast, while mixed batches still resume with only budgeted
// results.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestNormalizeStepRejectsContradictoryTerminalShapes(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	budgeted := newAnyJSONSpec(tools.Ident("svc.lookup"), "svc")
	terminalTool := newAnyJSONSpec(tools.Ident("svc.complete"), "svc")
	terminalTool.Bookkeeping = true
	terminalTool.TerminalRun = true
	seedTestToolSpecs(rt, budgeted, terminalTool)

	final := &planner.FinalResponse{
		Message: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "done"}},
		},
	}
	cases := []struct {
		name   string
		result *planner.PlanResult
		want   string
	}{
		{
			name: "terminal plus await",
			result: &planner.PlanResult{
				FinalResponse: final,
				Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{
					ID:       "clarify-1",
					Question: "Which item?",
				})),
			},
			want: "cannot combine terminal payload and await",
		},
		{
			name: "terminal plus budgeted tool",
			result: &planner.PlanResult{
				ToolCalls:     []planner.ToolRequest{{Name: budgeted.Name}},
				FinalResponse: final,
			},
			want: "cannot accompany budgeted tool",
		},
		{
			name: "terminal plus terminal tool",
			result: &planner.PlanResult{
				ToolCalls:     []planner.ToolRequest{{Name: terminalTool.Name}},
				FinalResponse: final,
			},
			want: "cannot accompany terminal tool",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := rt.normalizeStep(tt.result)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestRunLoopBookkeepingOnlyFinalResponseFinishesWithoutResume(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{bookkeeping},
	}))

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			Name:    bookkeeping.Name,
			Payload: rawjson.Message(`{}`),
		}},
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "done"}},
			},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "done", agentMessageText(out.Final))
	require.Len(t, out.ToolEvents, 1)
	require.Empty(t, wfCtx.lastPlannerCall.Name, "bookkeeping-only final turns must not resume")
}

func TestRunLoopBookkeepingOnlyWithoutTerminalPayloadFailsFast(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{bookkeeping},
	}))

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			Name:    bookkeeping.Name,
			Payload: rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.Error(t, err)
	require.Nil(t, out)
	require.Contains(t, err.Error(), "bookkeeping-only tool batch requires a terminal tool or terminal planner payload")
	require.Empty(t, wfCtx.lastPlannerCall.Name, "invalid bookkeeping-only turns must fail before resume")
}

func TestRunLoopRetryableBookkeepingTerminalFailureResumes(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	terminal := newAnyJSONSpec(tools.Ident("tasks.progress.complete"), "tasks.progress")
	terminal.Bookkeeping = true
	terminal.TerminalRun = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Error:      planner.NewToolError("brief.summary length must be <= 600"),
				RetryHint: &planner.RetryHint{
					Reason:             planner.RetryReasonInvalidArguments,
					Tool:               call.Name,
					ClarifyingQuestion: "Please resend tasks.progress.complete with a payload that satisfies: brief.summary length must be <= 600.",
				},
			}, nil
		}),
		Specs: []tools.ToolSpec{terminal},
	}))

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Payload: []byte("null"),
			Error:   "brief.summary length must be <= 600",
			RetryHint: &planner.RetryHint{
				Reason:             planner.RetryReasonInvalidArguments,
				Tool:               terminal.Name,
				ClarifyingQuestion: "Please resend tasks.progress.complete with a payload that satisfies: brief.summary length must be <= 600.",
			},
		},
		planResult: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "repaired"}},
				},
			},
		},
		hasPlanResult: true,
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			Name:    terminal.Name,
			Payload: rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Len(t, wfCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, "run-1/turn-1/attempt-1/tasks-progress-complete/0", wfCtx.lastPlannerCall.Input.ToolOutputs[0].ToolCallID)
	require.Len(t, wfCtx.lastPlannerCall.Input.Messages, 3)
	require.Equal(t, model.ConversationRoleAssistant, wfCtx.lastPlannerCall.Input.Messages[0].Role)
	require.Equal(t, model.ConversationRoleUser, wfCtx.lastPlannerCall.Input.Messages[1].Role)
	require.Equal(t, model.ConversationRoleSystem, wfCtx.lastPlannerCall.Input.Messages[2].Role)
}

func TestRunLoopProviderEmptyToolCallIDsAdvanceAcrossResumeAttempts(t *testing.T) {
	cases := []struct {
		name string
		tool tools.Ident
		want []string
	}{
		{
			name: "tool unavailable resumes",
			tool: tools.ToolUnavailable,
			want: []string{
				"run-1/turn-1/attempt-1/runtime-tool_unavailable/0",
				"run-1/turn-1/attempt-2/runtime-tool_unavailable/0",
				"run-1/turn-1/attempt-3/runtime-tool_unavailable/0",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := New(WithLogger(telemetry.NoopLogger{}))
			_, err := rt.CreateSession(context.Background(), "sess-1")
			require.NoError(t, err)
			agentID := agent.Ident("agent-1")
			var resumeAttempts []int
			rt.agents[agentID] = AgentRegistration{
				ID: agentID,
				Planner: &stubPlanner{resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumeAttempts = append(resumeAttempts, input.RunContext.Attempt)
					switch len(resumeAttempts) {
					case 1, 2:
						require.Len(t, input.ToolOutputs, len(resumeAttempts))
						return &planner.PlanResult{
							ToolCalls: []planner.ToolRequest{{
								Name:    tc.tool,
								Payload: rawjson.Message(`{}`),
							}},
						}, nil
					case 3:
						require.Len(t, input.ToolOutputs, len(tc.want))
						return &planner.PlanResult{
							FinalResponse: &planner.FinalResponse{
								Message: &model.Message{
									Role:  model.ConversationRoleAssistant,
									Parts: []model.Part{model.TextPart{Text: "done"}},
								},
							},
						}, nil
					default:
						require.FailNow(t, "unexpected resume attempt")
					}
					return nil, nil
				}},
			}
			wfCtx := &routeWorkflowContext{
				ctx:         context.Background(),
				runID:       "run-1",
				hookRuntime: rt,
				plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
					"resume": func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
						return rt.PlanResumeActivity(ctx, input)
					},
				},
			}
			base := &planner.PlanInput{
				RunContext: run.Context{
					RunID:     "run-1",
					SessionID: "sess-1",
					TurnID:    "turn-1",
					Attempt:   1,
				},
			}
			input := &RunInput{
				AgentID:   agentID,
				RunID:     "run-1",
				SessionID: "sess-1",
				TurnID:    "turn-1",
			}
			initial := &planner.PlanResult{
				ToolCalls: []planner.ToolRequest{{
					Name:    tc.tool,
					Payload: rawjson.Message(`{}`),
				}},
			}

			out, err := rt.runLoop(
				wfCtx,
				AgentRegistration{ID: agentID, ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
				input,
				base,
				initial,
				policy.CapsState{},
				time.Time{},
				time.Time{},
				"turn-1",
				nil,
			)
			require.NoError(t, err)
			require.NotNil(t, out)
			require.Equal(t, []int{2, 3, 4}, resumeAttempts)
			require.Len(t, out.ToolEvents, len(tc.want))
			for i, want := range tc.want {
				require.Equal(t, want, out.ToolEvents[i].ToolCallID)
			}
			require.NotEqual(t, out.ToolEvents[1].ToolCallID, out.ToolEvents[2].ToolCallID)
		})
	}
}

func TestRunLoopProviderEmptyToolCallIDsUseBatchIndexes(t *testing.T) {
	cases := []struct {
		name  string
		calls []planner.ToolRequest
		want  []string
	}{
		{
			name: "two tool unavailable calls",
			calls: []planner.ToolRequest{
				{Name: tools.ToolUnavailable, Payload: rawjson.Message(`{}`)},
				{Name: tools.ToolUnavailable, Payload: rawjson.Message(`{}`)},
			},
			want: []string{
				"run-1/turn-1/attempt-1/runtime-tool_unavailable/0",
				"run-1/turn-1/attempt-1/runtime-tool_unavailable/1",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt := New(WithLogger(telemetry.NoopLogger{}))
			_, err := rt.CreateSession(context.Background(), "sess-1")
			require.NoError(t, err)
			agentID := agent.Ident("agent-1")
			rt.agents[agentID] = AgentRegistration{
				ID: agentID,
				Planner: &stubPlanner{resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					require.Len(t, input.ToolOutputs, len(tc.want))
					return &planner.PlanResult{
						FinalResponse: &planner.FinalResponse{
							Message: &model.Message{
								Role:  model.ConversationRoleAssistant,
								Parts: []model.Part{model.TextPart{Text: "done"}},
							},
						},
					}, nil
				}},
			}
			wfCtx := &routeWorkflowContext{
				ctx:         context.Background(),
				runID:       "run-1",
				hookRuntime: rt,
				plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
					"resume": func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
						return rt.PlanResumeActivity(ctx, input)
					},
				},
			}
			base := &planner.PlanInput{
				RunContext: run.Context{
					RunID:     "run-1",
					SessionID: "sess-1",
					TurnID:    "turn-1",
					Attempt:   1,
				},
			}
			input := &RunInput{
				AgentID:   agentID,
				RunID:     "run-1",
				SessionID: "sess-1",
				TurnID:    "turn-1",
			}
			initial := &planner.PlanResult{
				ToolCalls: tc.calls,
			}

			out, err := rt.runLoop(
				wfCtx,
				AgentRegistration{ID: agentID, ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
				input,
				base,
				initial,
				policy.CapsState{},
				time.Time{},
				time.Time{},
				"turn-1",
				nil,
			)
			require.NoError(t, err)
			require.NotNil(t, out)
			require.Len(t, out.ToolEvents, len(tc.want))
			for i, want := range tc.want {
				require.Equal(t, want, out.ToolEvents[i].ToolCallID)
			}
			require.NotEqual(t, out.ToolEvents[0].ToolCallID, out.ToolEvents[1].ToolCallID)
		})
	}
}

func TestRunLoopMixedBudgetedAndBookkeepingStillResumes(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	budgeted := newAnyJSONSpec(tools.Ident("svc.tools.lookup"), "svc.tools")
	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "svc.tools",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"name": call.Name},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{budgeted},
	}))
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: wrapExecute(func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				Result:     map[string]any{"ok": true},
				ToolCallID: call.ToolCallID,
			}, nil
		}),
		Specs: []tools.ToolSpec{bookkeeping},
	}))

	wfCtx := &testWorkflowContext{
		ctx:           context.Background(),
		planResult:    &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
	}
	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{
			{Name: budgeted.Name, Payload: rawjson.Message(`{}`)},
			{Name: bookkeeping.Name, Payload: rawjson.Message(`{}`)},
		},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Len(t, wfCtx.lastPlannerCall.Input.ToolOutputs, 1)
	require.Equal(t, "run-1/turn-1/attempt-1/svc-tools-lookup/0", wfCtx.lastPlannerCall.Input.ToolOutputs[0].ToolCallID)
}

func TestRunLoopBookkeepingOnlyToolPausePreservesTranscriptWithoutToolOutput(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"ok": true},
					ToolCallID: call.ToolCallID,
				},
				Pause: &ToolPause{
					Clarification: &ToolPauseClarification{
						ID:       "task-input-1",
						Question: "Which alarm should I investigate?",
					},
				},
			}, nil
		},
		Specs: []tools.ToolSpec{bookkeeping},
	}))
	resultJSON, err := json.Marshal(map[string]any{"ok": true})
	require.NoError(t, err)

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Payload: rawjson.Message(resultJSON),
			Pause: &ToolPause{
				Clarification: &ToolPauseClarification{
					ID:       "task-input-1",
					Question: "Which alarm should I investigate?",
				},
			},
		},
		planResult:    &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
	}
	wfCtx.ensureSignals()
	ctrl := interrupt.NewController(wfCtx)
	wfCtx.clarifyCh <- &api.ClarificationAnswer{
		ID:     "task-input-1",
		Answer: "Check alarm 7",
	}

	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			Name:    bookkeeping.Name,
			Payload: rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		"turn-1",
		ctrl,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "done", agentMessageText(out.Final))
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.Empty(t, wfCtx.lastPlannerCall.Input.ToolOutputs, "bookkeeping pauses must not replay tool outputs into the planner")
	require.Len(t, wfCtx.lastPlannerCall.Input.Messages, 3)
	require.Equal(t, model.ConversationRoleAssistant, wfCtx.lastPlannerCall.Input.Messages[0].Role)
	require.Equal(t, model.ConversationRoleUser, wfCtx.lastPlannerCall.Input.Messages[1].Role)
	last := wfCtx.lastPlannerCall.Input.Messages[len(wfCtx.lastPlannerCall.Input.Messages)-1]
	require.Equal(t, model.ConversationRoleUser, last.Role)
	part, ok := last.Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Equal(t, "Check alarm 7", part.Text)
}

func TestRunLoopBudgetedToolPauseRecordsResultBeforeUserAnswer(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	budgeted := newAnyJSONSpec(tools.Ident("tasks.progress.update"), "tasks.progress")
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"phase": "awaiting_input"},
					ToolCallID: call.ToolCallID,
				},
				Pause: &ToolPause{
					Clarification: &ToolPauseClarification{
						ID:       "task-input-1",
						Question: "Which compressor should I investigate?",
					},
				},
			}, nil
		},
		Specs: []tools.ToolSpec{budgeted},
	}))
	resultJSON, err := json.Marshal(map[string]any{"phase": "awaiting_input"})
	require.NoError(t, err)

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Payload: rawjson.Message(resultJSON),
			Pause: &ToolPause{
				Clarification: &ToolPauseClarification{
					ID:       "task-input-1",
					Question: "Which compressor should I investigate?",
				},
			},
		},
		planResult:    &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}},
		hasPlanResult: true,
	}
	wfCtx.ensureSignals()
	ctrl := interrupt.NewController(wfCtx)
	wfCtx.clarifyCh <- &api.ClarificationAnswer{
		ID:     "task-input-1",
		Answer: "Compressor 1",
	}

	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			Name:    budgeted.Name,
			Payload: rawjson.Message(`{}`),
		}},
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		"turn-1",
		ctrl,
	)
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Equal(t, "resume", wfCtx.lastPlannerCall.Name)
	require.NoError(t, transcript.ValidatePlannerTranscript(wfCtx.lastPlannerCall.Input.Messages))
	require.Len(t, wfCtx.lastPlannerCall.Input.Messages, 3)

	assistantMsg := wfCtx.lastPlannerCall.Input.Messages[0]
	require.Equal(t, model.ConversationRoleAssistant, assistantMsg.Role)
	toolUse, ok := assistantMsg.Parts[0].(model.ToolUsePart)
	require.True(t, ok)

	resultMsg := wfCtx.lastPlannerCall.Input.Messages[1]
	require.Equal(t, model.ConversationRoleUser, resultMsg.Role)
	toolResult, ok := resultMsg.Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	require.Equal(t, toolUse.ID, toolResult.ToolUseID)

	answerMsg := wfCtx.lastPlannerCall.Input.Messages[2]
	require.Equal(t, model.ConversationRoleUser, answerMsg.Role)
	answer, ok := answerMsg.Parts[0].(model.TextPart)
	require.True(t, ok)
	require.Equal(t, "Compressor 1", answer.Text)
}

func TestRunLoopBookkeepingToolTerminalRejectsPause(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))

	bookkeeping := newAnyJSONSpec(tools.Ident("tasks.progress.set_step_status"), "tasks.progress")
	bookkeeping.Bookkeeping = true
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "tasks.progress",
		Execute: func(ctx context.Context, call *planner.ToolRequest) (*ToolExecutionResult, error) {
			return &ToolExecutionResult{
				ToolResult: &planner.ToolResult{
					Name:       call.Name,
					Result:     map[string]any{"ok": true},
					ToolCallID: call.ToolCallID,
				},
				Pause: &ToolPause{
					Clarification: &ToolPauseClarification{
						ID:       "task-input-1",
						Question: "Which alarm should I investigate?",
					},
				},
			}, nil
		},
		Specs: []tools.ToolSpec{bookkeeping},
	}))
	resultJSON, err := json.Marshal(map[string]any{"ok": true})
	require.NoError(t, err)

	wfCtx := &testWorkflowContext{
		ctx: context.Background(),
		asyncResult: ToolOutput{
			Payload: rawjson.Message(resultJSON),
			Pause: &ToolPause{
				Clarification: &ToolPauseClarification{
					ID:       "task-input-1",
					Question: "Which alarm should I investigate?",
				},
			},
		},
	}

	base := &planner.PlanInput{
		RunContext: run.Context{
			RunID:     "run-1",
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Attempt:   1,
		},
	}
	input := &RunInput{
		AgentID:   agent.Ident("agent-1"),
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	seedRunMeta(t, rt, input)
	initial := &planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{
			Name:    bookkeeping.Name,
			Payload: rawjson.Message(`{}`),
		}},
		FinalResponse: &planner.FinalResponse{
			Message: &model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "premature terminal text"}},
			},
		},
		Streamed: true,
	}

	out, err := rt.runLoop(
		wfCtx,
		AgentRegistration{ExecuteToolActivity: "execute", ResumeActivityName: "resume"},
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)
	require.Error(t, err)
	require.Nil(t, out)
	require.ErrorContains(t, err, "workflow step terminal payload cannot accompany await work")
	require.Empty(t, wfCtx.lastPlannerCall.Name, "invalid tool-terminal steps must not resume")
}
