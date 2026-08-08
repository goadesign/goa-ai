package runtime

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

func TestValidateRecoveryPlan(t *testing.T) {
	t.Parallel()

	rt := New()
	seedTestToolSpecs(
		rt,
		newAnyJSONSpec("catalog.search", "catalog"),
		newAnyJSONSpec("catalog.list", "catalog"),
	)
	correctFailure := &planner.ToolOutput{
		Name:    tools.Ident("catalog.search"),
		Payload: rawjson.Message(`{"query":"bad"}`),
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			planner.RecoveryCorrectCall,
			"query is invalid",
		),
	}
	secondCorrectFailure := &planner.ToolOutput{
		Name:    tools.Ident("catalog.search"),
		Payload: rawjson.Message(`{"query":"also-bad"}`),
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			planner.RecoveryCorrectCall,
			"query is also invalid",
		),
	}
	listCorrectFailure := &planner.ToolOutput{
		Name:    tools.Ident("catalog.list"),
		Payload: rawjson.Message(`{"page":0}`),
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			planner.RecoveryCorrectCall,
			"page is invalid",
		),
	}
	replanFailure := &planner.ToolOutput{
		Name:    tools.Ident("catalog.search"),
		Payload: rawjson.Message(`{"query":"bad"}`),
		Failure: testToolFailure(
			planner.FailureDomainRejection,
			planner.RecoveryReplan,
			"no matching catalog",
		),
	}
	tests := []struct {
		name     string
		failures []*planner.ToolOutput
		result   *planner.PlanResult
		wantErr  string
	}{
		{
			name:   "no failure permits completion",
			result: &planner.PlanResult{},
		},
		{
			name:     "correct call rejects completion",
			failures: []*planner.ToolOutput{correctFailure},
			result:   &planner.PlanResult{},
			wantErr:  "completed without correcting",
		},
		{
			name:     "correct call permits clarification",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{Await: planner.NewAwait(
				planner.AwaitClarificationItem(&planner.AwaitClarification{
					ID:             "clarify-query",
					Question:       "What query should be used?",
					MissingFields:  []string{"query"},
					RestrictToTool: "catalog.search",
				}),
			)},
		},
		{
			name:     "correct call rejects clarification with another transition",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{
				Await: &planner.Await{},
				ToolCalls: []planner.ToolRequest{{
					Name:    tools.Ident("catalog.search"),
					Payload: rawjson.Message(`{"query":"good"}`),
				}},
			},
			wantErr: "combined recovery clarification",
		},
		{
			name:     "correct call rejects unbound clarification",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{Await: planner.NewAwait(
				planner.AwaitClarificationItem(&planner.AwaitClarification{
					ID:            "clarify-query",
					Question:      "What query should be used?",
					MissingFields: []string{"query"},
				}),
			)},
			wantErr: "must restrict",
		},
		{
			name:     "correct call rejects tool-backed clarification",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{Await: planner.NewAwait(
				planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{
					ID:         "clarify-query",
					ToolName:   "chat.ask_clarification",
					ToolCallID: "call-clarify",
					Payload:    rawjson.Message(`{"question":"What query?"}`),
					Question:   "What query should be used?",
				}),
			)},
			wantErr: "only plain clarification",
		},
		{
			name:     "correct call accepts changed payload",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"good"}`),
			}}},
		},
		{
			name:     "correct call rejects unchanged payload",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"bad"}`),
			}}},
			wantErr: "without changing its payload",
		},
		{
			name: "correct call rejects reordered unchanged payload",
			failures: []*planner.ToolOutput{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"limit":10,"query":"bad"}`),
				Failure: testToolFailure(
					planner.FailureInvalidCall,
					planner.RecoveryCorrectCall,
					"query is invalid",
				),
			}},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"bad","limit":10}`),
			}}},
			wantErr: "without changing its payload",
		},
		{
			name:     "correct call rejects another tool",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.list"),
				Payload: rawjson.Message(`{}`),
			}}},
			wantErr: "recovery requires correcting",
		},
		{
			name:     "every correct call obligation must be satisfied",
			failures: []*planner.ToolOutput{correctFailure, secondCorrectFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"good"}`),
			}}},
			wantErr: "did not correct 1 failed call",
		},
		{
			name:     "multiple corrections satisfy multiple failures",
			failures: []*planner.ToolOutput{correctFailure, secondCorrectFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{
				{
					Name:    tools.Ident("catalog.search"),
					Payload: rawjson.Message(`{"query":"good"}`),
				},
				{
					Name:    tools.Ident("catalog.search"),
					Payload: rawjson.Message(`{"query":"better"}`),
				},
			}},
		},
		{
			name:     "correction rejects extra calls",
			failures: []*planner.ToolOutput{correctFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{
				{
					Name:    tools.Ident("catalog.search"),
					Payload: rawjson.Message(`{"query":"good"}`),
				},
				{
					Name:    tools.Ident("catalog.search"),
					Payload: rawjson.Message(`{"query":"extra"}`),
				},
			}},
			wantErr: "added an extra call",
		},
		{
			name:     "corrections satisfy failures from multiple tools",
			failures: []*planner.ToolOutput{correctFailure, listCorrectFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{
				{
					Name:    tools.Ident("catalog.search"),
					Payload: rawjson.Message(`{"query":"good"}`),
				},
				{
					Name:    tools.Ident("catalog.list"),
					Payload: rawjson.Message(`{"page":1}`),
				},
			}},
		},
		{
			name:     "replan permits final answer",
			failures: []*planner.ToolOutput{replanFailure},
			result:   &planner.PlanResult{FinalResponse: &planner.FinalResponse{}},
		},
		{
			name:     "replan rejects clarification",
			failures: []*planner.ToolOutput{replanFailure},
			result:   &planner.PlanResult{Await: &planner.Await{}},
			wantErr:  "recovery requires another capability or a final answer",
		},
		{
			name:     "replan accepts another tool",
			failures: []*planner.ToolOutput{replanFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.list"),
				Payload: rawjson.Message(`{}`),
			}}},
		},
		{
			name: "tool unavailable rejects identical repeated request",
			failures: []*planner.ToolOutput{{
				Name:    tools.ToolUnavailable,
				Payload: rawjson.Message(`{"requested_tool":"catalog.missing","requested_payload":{"q":"status"}}`),
				Failure: testToolFailure(
					planner.FailureInvalidCall,
					planner.RecoveryReplan,
					"tool is unavailable",
				),
			}},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.ToolUnavailable,
				Payload: rawjson.Message(`{"requested_tool":"catalog.missing","requested_payload":{"q":"status"}}`),
			}}},
			wantErr: "without changing its payload",
		},
		{
			name:     "replan validator preserves changed historical call",
			failures: []*planner.ToolOutput{replanFailure},
			result: &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
				Name:    tools.Ident("catalog.search"),
				Payload: rawjson.Message(`{"query":"different"}`),
			}}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := rt.validateRecoveryPlan(tt.failures, tt.result)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestReplanToolIsNotAdvertisedOnRecoveryTurn(t *testing.T) {
	t.Parallel()

	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
	rt := New()
	rt.agentToolSpecs[agent.Ident("catalog.agent")] = []tools.ToolSpec{search, list}
	outputs := []*planner.ToolOutput{
		{
			Name: search.Name,
			Failure: testToolFailure(
				planner.FailureDomainRejection,
				planner.RecoveryReplan,
				"search cannot satisfy this request",
			),
		},
		{
			Name: search.Name,
			Failure: testToolFailure(
				planner.FailureInvalidCall,
				planner.RecoveryCorrectCall,
				"search input is invalid",
			),
		},
	}
	ctx := newAgentContext(agentContextOptions{
		runtime:          rt,
		agentID:          agent.Ident("catalog.agent"),
		unavailableTools: replanUnavailableTools(outputs[:1]),
	})

	definitions := ctx.AdvertisedToolDefinitions()

	require.Len(t, definitions, 1)
	assert.Equal(t, list.Name.String(), definitions[0].Name)

	restored := newAgentContext(agentContextOptions{
		runtime: rt,
		agentID: agent.Ident("catalog.agent"),
	})
	assert.Len(t, restored.AdvertisedToolDefinitions(), 2)

	assert.NotContains(
		t,
		replanUnavailableTools(outputs),
		search.Name,
		"same-tool correction is stronger than a parallel replan failure",
	)
}

func TestRecoveryCatalogRejectsUnadvertisedTool(t *testing.T) {
	t.Parallel()

	result := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    "catalog.search",
		Payload: rawjson.Message(`{"query":"different"}`),
	}}}
	err := validateRecoveryCatalog(
		&RecoveryCatalog{Tools: []tools.Ident{"catalog.list"}},
		result,
	)
	require.ErrorContains(t, err, "outside the advertised recovery catalog")

	require.NoError(t, validateRecoveryCatalog(nil, result))
}

func TestPendingRecoveryOutputsDropsWeakerTransitionsWhenFinishIsPresent(t *testing.T) {
	t.Parallel()

	records := []stepToolRecord{
		{
			call: planner.ToolRequest{Name: tools.Ident("catalog.search")},
			result: &planner.ToolResult{Failure: testToolFailure(
				planner.FailureDomainRejection,
				planner.RecoveryReplan,
				"search rejected",
			)},
		},
		{
			call: planner.ToolRequest{Name: tools.Ident("catalog.load")},
			result: &planner.ToolResult{Failure: testToolFailure(
				planner.FailureInternal,
				planner.RecoveryFinish,
				"load failed",
			)},
		},
	}

	assert.Empty(t, pendingRecoveryOutputs(records))
}

func TestNextRecoveryTurnQueuesDistinctCorrectionTools(t *testing.T) {
	t.Parallel()

	searchOne := recoveryOutput("catalog.search", planner.RecoveryCorrectCall)
	replan := recoveryOutput("catalog.search", planner.RecoveryReplan)
	searchTwo := recoveryOutput("catalog.search", planner.RecoveryCorrectCall)
	list := recoveryOutput("catalog.list", planner.RecoveryCorrectCall)

	current, queued, policy, err := nextRecoveryTurn(
		nil,
		[]*planner.ToolOutput{searchOne, replan, searchTwo, list},
	)

	require.NoError(t, err)
	assert.Equal(t, []*planner.ToolOutput{searchOne, replan, searchTwo}, current)
	assert.Equal(t, []*planner.ToolOutput{list}, queued)
	require.NotNil(t, policy)
	assert.Equal(t, tools.Ident("catalog.search"), policy.RestrictToTool)

	current, queued, policy, err = nextRecoveryTurn(nil, queued)

	require.NoError(t, err)
	assert.Equal(t, []*planner.ToolOutput{list}, current)
	assert.Empty(t, queued)
	assert.Equal(t, tools.Ident("catalog.list"), policy.RestrictToTool)
}

func TestRunLoopCorrectsDistinctToolsInSeparateRestrictedTurns(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	_, err := rt.CreateSession(context.Background(), "sess-1")
	require.NoError(t, err)

	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "catalog",
		Execute: wrapExecute(func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			if string(call.Payload) == `{"invalid":true}` ||
				(call.Name == search.Name && string(call.Payload) == `{"retry":1}`) {
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: testToolFailure(
						planner.FailureInvalidCall,
						planner.RecoveryCorrectCall,
						"invalid call",
					),
				}, nil
			}
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{search, list},
	}))

	agentID := agent.Ident("catalog.agent")
	var advertised [][]string
	registration := AgentRegistration{
		ID: agentID,
		Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			definitions := input.Agent.AdvertisedToolDefinitions()
			names := make([]string, len(definitions))
			for i, definition := range definitions {
				names[i] = definition.Name
			}
			advertised = append(advertised, names)
			switch len(advertised) {
			case 1:
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name: search.Name, Payload: rawjson.Message(`{"retry":1}`),
				}}}, nil
			case 2:
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name: search.Name, Payload: rawjson.Message(`{"retry":2}`),
				}}, SynthesizeAfterTools: true}, nil
			case 3:
				return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
					Name: list.Name, Payload: rawjson.Message(`{"page":1}`),
				}}}, nil
			case 4:
				require.True(t, input.SynthesisOnly)
				return &planner.PlanResult{FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "done"}},
					},
				}}, nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		}},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}
	rt.agents[agentID] = registration
	rt.agentToolSpecs[agentID] = []tools.ToolSpec{search, list}
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       "run-1",
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Attempt:   1,
	}}
	input := &RunInput{
		AgentID:   agentID,
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{
		{Name: search.Name, Payload: rawjson.Message(`{"invalid":true}`)},
		{Name: list.Name, Payload: rawjson.Message(`{"invalid":true}`)},
	}}

	out, err := rt.runLoop(
		wfCtx,
		registration,
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 10, RemainingToolCalls: 10},
		time.Time{},
		time.Time{},
		"turn-1",
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	require.Len(t, advertised, 4)
	assert.Equal(t, []string{search.Name.String()}, advertised[0])
	assert.Equal(t, []string{search.Name.String()}, advertised[1])
	assert.Equal(t, []string{list.Name.String()}, advertised[2])
	assert.ElementsMatch(t, []string{search.Name.String(), list.Name.String()}, advertised[3])
}

func TestRunLoopRemovesReplanToolForExactlyOneTurn(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	_, err := rt.CreateSession(context.Background(), "sess-replan")
	require.NoError(t, err)

	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "catalog",
		Execute: wrapExecute(func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			if call.Name == search.Name {
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: testToolFailure(
						planner.FailureDomainRejection,
						planner.RecoveryReplan,
						"search cannot answer this request",
					),
				}, nil
			}
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{search, list},
	}))

	agentID := agent.Ident("catalog.replan")
	resumes := 0
	registration := AgentRegistration{
		ID: agentID,
		Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			definitions := input.Agent.AdvertisedToolDefinitions()
			switch resumes {
			case 1:
				require.Len(t, definitions, 1)
				assert.Equal(t, list.Name.String(), definitions[0].Name)
				require.Len(t, input.Reminders, 1)
				assert.Contains(t, input.Reminders[0].Text, "failed tool is unavailable for this turn")
				return &planner.PlanResult{
					ToolCalls: []planner.ToolRequest{{
						Name:    list.Name,
						Payload: rawjson.Message(`{"page":1}`),
					}},
					SynthesizeAfterTools: true,
				}, nil
			case 2:
				require.True(t, input.SynthesisOnly)
				require.Len(t, definitions, 2)
				assert.Empty(t, input.Reminders)
				return &planner.PlanResult{FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "done"}},
					},
				}}, nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		}},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}
	rt.agents[agentID] = registration
	rt.agentToolSpecs[agentID] = []tools.ToolSpec{search, list}
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       "run-replan",
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID:     "run-replan",
		SessionID: "sess-replan",
		TurnID:    "turn-replan",
		Attempt:   1,
	}}
	input := &RunInput{
		AgentID:   agentID,
		RunID:     "run-replan",
		SessionID: "sess-replan",
		TurnID:    "turn-replan",
	}
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    search.Name,
		Payload: rawjson.Message(`{"query":"unsupported"}`),
	}}}

	out, err := rt.runLoop(
		wfCtx,
		registration,
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		"turn-replan",
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, 2, resumes)
}

func TestRunLoopPreservesCorrectionAcrossClarification(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	_, err := rt.CreateSession(context.Background(), "sess-correction")
	require.NoError(t, err)

	search := newAnyJSONSpec("catalog.search", "catalog")
	list := newAnyJSONSpec("catalog.list", "catalog")
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "catalog",
		Execute: wrapExecute(func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			if string(call.Payload) == `{"query":"bad"}` {
				return &planner.ToolResult{
					Name:       call.Name,
					ToolCallID: call.ToolCallID,
					Failure: testToolFailure(
						planner.FailureInvalidCall,
						planner.RecoveryCorrectCall,
						"query is required",
					),
				}, nil
			}
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Result:     map[string]any{"ok": true},
			}, nil
		}),
		Specs: []tools.ToolSpec{search, list},
	}))

	agentID := agent.Ident("catalog.agent")
	resumes := 0
	registration := AgentRegistration{
		ID: agentID,
		Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			definitions := input.Agent.AdvertisedToolDefinitions()
			switch resumes {
			case 1:
				require.Len(t, definitions, 1)
				assert.Equal(t, search.Name.String(), definitions[0].Name)
				require.Len(t, input.Reminders, 1)
				return &planner.PlanResult{Await: planner.NewAwait(
					planner.AwaitClarificationItem(&planner.AwaitClarification{
						ID:             "clarify-query",
						Question:       "What should the search query be?",
						MissingFields:  []string{"query"},
						RestrictToTool: search.Name,
					}),
				)}, nil
			case 2:
				require.Len(t, definitions, 1)
				assert.Equal(t, search.Name.String(), definitions[0].Name)
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
				require.Len(t, definitions, 2)
				assert.Empty(t, input.Reminders)
				return &planner.PlanResult{FinalResponse: &planner.FinalResponse{
					Message: &model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "done"}},
					},
				}}, nil
			default:
				require.FailNow(t, "unexpected planner resume")
				return nil, nil
			}
		}},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}
	rt.agents[agentID] = registration
	rt.agentToolSpecs[agentID] = []tools.ToolSpec{search, list}
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       "run-correction",
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}
	wfCtx.ensureSignals()
	ctrl := interrupt.NewController(wfCtx)
	wfCtx.clarifyCh <- &api.ClarificationAnswer{
		ID:     "clarify-query",
		Answer: "Use good.",
	}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID:     "run-correction",
		SessionID: "sess-correction",
		TurnID:    "turn-correction",
		Attempt:   1,
	}}
	input := &RunInput{
		AgentID:   agentID,
		RunID:     "run-correction",
		SessionID: "sess-correction",
		TurnID:    "turn-correction",
	}
	seedRunMeta(t, rt, input)
	initial := &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
		Name:    search.Name,
		Payload: rawjson.Message(`{"query":"bad"}`),
	}}}

	out, err := rt.runLoop(
		wfCtx,
		registration,
		input,
		base,
		initial,
		policy.CapsState{MaxToolCalls: 4, RemainingToolCalls: 4},
		time.Time{},
		time.Time{},
		"turn-correction",
		ctrl,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, 3, resumes)
}

func TestRunLoopCarriesFinishReminderIntoSynthesis(t *testing.T) {
	rt := New(WithLogger(telemetry.NoopLogger{}))
	_, err := rt.CreateSession(context.Background(), "sess-finish")
	require.NoError(t, err)

	search := newAnyJSONSpec("catalog.search", "catalog")
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "catalog",
		Execute: wrapExecute(func(_ context.Context, call *planner.ToolRequest) (*planner.ToolResult, error) {
			return &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Failure: testToolFailure(
					planner.FailureInternal,
					planner.RecoveryFinish,
					"search is unavailable",
				),
			}, nil
		}),
		Specs: []tools.ToolSpec{search},
	}))

	agentID := agent.Ident("catalog.agent")
	registration := AgentRegistration{
		ID: agentID,
		Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			require.True(t, input.SynthesisOnly)
			require.Len(t, input.Reminders, 1)
			assert.Contains(t, input.Reminders[0].Text, "Do not call more tools")
			return &planner.PlanResult{FinalResponse: &planner.FinalResponse{
				Message: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "unable to search"}},
				},
			}}, nil
		}},
		ExecuteToolActivity: "execute",
		ResumeActivityName:  "resume",
	}
	rt.agents[agentID] = registration
	rt.agentToolSpecs[agentID] = []tools.ToolSpec{search}
	wfCtx := &routeWorkflowContext{
		ctx:         context.Background(),
		runID:       "run-finish",
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}
	base := &planner.PlanInput{RunContext: run.Context{
		RunID:     "run-finish",
		SessionID: "sess-finish",
		TurnID:    "turn-finish",
		Attempt:   1,
	}}
	input := &RunInput{
		AgentID:   agentID,
		RunID:     "run-finish",
		SessionID: "sess-finish",
		TurnID:    "turn-finish",
	}
	seedRunMeta(t, rt, input)

	out, err := rt.runLoop(
		wfCtx,
		registration,
		input,
		base,
		&planner.PlanResult{ToolCalls: []planner.ToolRequest{{
			Name:    search.Name,
			Payload: rawjson.Message(`{"query":"status"}`),
		}}},
		policy.CapsState{MaxToolCalls: 2, RemainingToolCalls: 2},
		time.Time{},
		time.Time{},
		"turn-finish",
		nil,
	)

	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "unable to search", agentMessageText(out.Final))
}

// recoveryOutput constructs one pending recovery obligation for queue tests.
func recoveryOutput(name tools.Ident, action planner.RecoveryAction) *planner.ToolOutput {
	return &planner.ToolOutput{
		Name: name,
		Failure: testToolFailure(
			planner.FailureInvalidCall,
			action,
			"failed",
		),
	}
}
