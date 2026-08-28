// This file exercises the complete durable recovery path for a model tool name
// that was absent from the request. The rejected response never reaches tool
// execution or the next transcript, while its usage is retained exactly once.

package runtime

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
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestModelInvocationRecoveryReminderEscapesRejectedName(t *testing.T) {
	name := "catalog_list_\n\"items\""
	got := modelInvocationRecoveryReminder(&ModelInvocationRecovery{
		UnadvertisedToolName: name,
	})

	assert.Equal(
		t,
		`Your previous tool call used the unavailable name "catalog_list_\n\"items\"".`+"\n"+
			`Choose the needed tool from the tools available now, copy its name exactly, `+
			`and return a replacement tool call. Do not mention this reminder to the user.`,
		got,
	)
	assert.NotContains(t, got, "\n\"items\"")
}

func TestValidatePlanResumeRecoveryInputRequiresOneInvocationVariant(t *testing.T) {
	tests := []struct {
		name     string
		recovery *ModelInvocationRecovery
		wantErr  string
	}{
		{
			name:     "empty",
			recovery: &ModelInvocationRecovery{},
			wantErr:  "requires exactly one recovery variant",
		},
		{
			name: "both",
			recovery: &ModelInvocationRecovery{
				Correction:           "Use the required field.",
				UnadvertisedToolName: "catalog_list_nearby",
			},
			wantErr: "requires exactly one recovery variant",
		},
		{
			name: "correction",
			recovery: &ModelInvocationRecovery{
				Correction: "Use the required field.",
			},
		},
		{
			name: "unadvertised name",
			recovery: &ModelInvocationRecovery{
				UnadvertisedToolName: "catalog_list_nearby",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePlanResumeRecoveryInput(&PlanActivityInput{
				ModelInvocationRecovery: test.recovery,
			})
			if test.wantErr != "" {
				assert.ErrorContains(t, err, test.wantErr)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestModelInvocationJournalExcludesNonOutputFailures(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "canceled", err: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded},
		{name: "transport", err: errors.New("transport closed")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			invocations := &modelInvocationJournal{}
			id, err := invocations.beginModelInvocation("", func() {})
			require.NoError(t, err)
			require.NoError(t, invocations.stageRejectedModelOutput(id, model.ResponseEvidence{}, test.err))

			assert.Nil(t, invocations.recoverableModelInvocationRecovery())
		})
	}
}

func TestWorkflowRecoversUnadvertisedToolName(t *testing.T) {
	originalCatalog := newAnyJSONSpec("catalog.items.list_original", "catalog.items")
	catalog := newAnyJSONSpec("catalog.items.list_items", "catalog.items")
	rt := New(WithLogger(telemetry.NoopLogger{}))
	sessionID := "session-unadvertised-tool"
	_, err := rt.CreateSession(t.Context(), sessionID)
	require.NoError(t, err)

	var executions, resumes int
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name:  "catalog.items",
		Specs: []tools.ToolSpec{originalCatalog, catalog},
		Execute: wrapExecute(func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			executions++
			assert.Equal(t, catalog.Name, call.Name)
			return successfulToolResult(call), nil
		}),
	}))

	agentID := agent.Ident("catalog.assistant")
	registration := AgentRegistration{
		ID:                  agentID,
		Specs:               []tools.ToolSpec{originalCatalog},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
		Planner: &stubPlanner{
			start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				client, ok := input.Agent.PlannerModelClient("test")
				require.True(t, ok)
				summary, err := client.Stream(ctx, &model.Request{
					Model: "test",
					Tools: input.Agent.AdvertisedToolDefinitions(),
				})
				assert.Empty(t, summary.Text)
				assert.Empty(t, summary.ToolCalls)
				assert.Equal(t, model.TokenUsage{}, summary.Usage)
				rt.agentToolSpecs[agentID] = []tools.ToolSpec{catalog}
				return nil, err
			},
			resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				resumes++
				if resumes == 1 {
					assert.False(t, input.SynthesisOnly)
					assertAdvertisedTools(t, input, catalog.Name)
					require.Len(t, input.Reminders, 1)
					assert.Equal(
						t,
						`Your previous tool call used the unavailable name "catalog_list_nearby".`+"\n"+
							`Choose the needed tool from the tools available now, copy its name exactly, `+
							`and return a replacement tool call. Do not mention this reminder to the user.`,
						input.Reminders[0].Text,
					)
					assert.NotContains(t, input.Reminders[0].Text, "rejected-call")
					assert.NotContains(t, input.Reminders[0].Text, "ignored")
					require.Len(t, input.Messages, 1)
					assert.Equal(t, model.ConversationRoleUser, input.Messages[0].Role)
					return &planner.PlanResult{
						ToolCalls: []planner.ToolRequest{{
							Name:    catalog.Name,
							Payload: rawjson.Message(`{"scope":"current"}`),
						}},
						SynthesizeAfterTools: true,
					}, nil
				}
				assert.True(t, input.SynthesisOnly)
				return finalPlannerResult("items listed"), nil
			},
		},
		Policy: RunPolicy{MaxToolCalls: 1, MaxRecoveryTurns: 1},
	}
	rt.agents[agentID] = registration
	rt.agentToolSpecs[agentID] = registration.Specs
	rt.models["test"] = mustTestModelClient(stubModelClient{
		stream: func(_ context.Context, request *model.Request) (model.Streamer, error) {
			usage := model.TokenUsage{
				InputTokens:  4,
				OutputTokens: 3,
				TotalTokens:  7,
			}
			contract, err := model.NewRequestContract(request)
			require.NoError(t, err)
			return &chunkStreamer{
				chunks: []model.Chunk{
					model.TextChunk{Message: model.Message{
						Role:  model.ConversationRoleAssistant,
						Parts: []model.Part{model.TextPart{Text: "discarded text"}},
					}},
					model.ToolCallChunk{ToolCall: model.ToolCall{
						ID:      "rejected-valid-call",
						Name:    originalCatalog.Name,
						Payload: rawjson.Message(`{"ignored":true}`),
					}},
					model.UsageChunk{Usage: usage},
				},
				terminalErr: contract.RejectProviderOutput(
					model.OutputValidationToolIdentity,
					&usage,
					model.NewUnadvertisedToolNameError("catalog_list_nearby"),
				),
			}, nil
		},
	})

	runInput := &RunInput{
		AgentID:   agentID,
		RunID:     "run-unadvertised-tool",
		SessionID: sessionID,
		TurnID:    "turn-unadvertised-tool",
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "List the items."}},
		}},
	}
	seedRunMeta(t, rt, runInput)
	wfCtx := &routeWorkflowContext{
		ctx:         t.Context(),
		runID:       runInput.RunID,
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan":   rt.PlanStartActivity,
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, runInput)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "items listed", agentMessageText(out.Final))
	assert.Equal(t, 1, executions)
	assert.Equal(t, 2, resumes)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 7, out.Usage.TotalTokens)
	require.Len(t, out.ToolEvents, 1)
	assert.Equal(t, catalog.Name, out.ToolEvents[0].Name)
}

func TestWorkflowExhaustsRepeatedUnadvertisedToolNames(t *testing.T) {
	catalog := newAnyJSONSpec("catalog.items.list_items", "catalog.items")
	rt := New(WithLogger(telemetry.NoopLogger{}))
	sessionID := "session-repeated-unadvertised-tool"
	_, err := rt.CreateSession(t.Context(), sessionID)
	require.NoError(t, err)

	agentID := agent.Ident("catalog.repeating_assistant")
	registration := AgentRegistration{
		ID:                  agentID,
		Specs:               []tools.ToolSpec{catalog},
		PlanActivityName:    "plan",
		ResumeActivityName:  "resume",
		ExecuteToolActivity: "execute",
		Planner: &stubPlanner{
			start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				return repeatedUnadvertisedToolCall(ctx, input.Agent)
			},
			resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
				if input.Finalize != nil {
					assert.Equal(t, planner.TerminationReasonRecoveryCap, input.Finalize.Reason)
					return finalPlannerResult("could not select an available tool"), nil
				}
				return repeatedUnadvertisedToolCall(ctx, input.Agent)
			},
		},
		Policy: RunPolicy{MaxToolCalls: 1, MaxRecoveryTurns: 1},
	}
	rt.agents[agentID] = registration
	rt.agentToolSpecs[agentID] = registration.Specs
	var modelCalls int
	rt.models["test"] = mustTestModelClient(stubModelClient{
		complete: func(context.Context, *model.Request) (*model.Response, error) {
			modelCalls++
			return &model.Response{
				Content: []model.Message{{
					Role: model.ConversationRoleAssistant,
					Parts: []model.Part{model.ToolUsePart{
						ID:    fmt.Sprintf("rejected-call-%d", modelCalls),
						Name:  "catalog_list_nearby",
						Input: rawjson.Message(`{}`),
					}},
				}},
				StopReason: "tool_use",
				Usage: model.TokenUsage{
					InputTokens:  4,
					OutputTokens: 3,
					TotalTokens:  7,
				},
			}, nil
		},
	})

	runInput := &RunInput{
		AgentID:   agentID,
		RunID:     "run-repeated-unadvertised-tool",
		SessionID: sessionID,
		TurnID:    "turn-repeated-unadvertised-tool",
	}
	seedRunMeta(t, rt, runInput)
	wfCtx := &routeWorkflowContext{
		ctx:         t.Context(),
		runID:       runInput.RunID,
		hookRuntime: rt,
		plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
			"plan":   rt.PlanStartActivity,
			"resume": rt.PlanResumeActivity,
		},
		toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){
			"execute": rt.ExecuteToolActivity,
		},
	}

	out, err := rt.ExecuteWorkflow(wfCtx, runInput)
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "could not select an available tool", agentMessageText(out.Final))
	assert.Equal(t, 2, modelCalls)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 14, out.Usage.TotalTokens)
	assert.Empty(t, out.ToolEvents)
}

// repeatedUnadvertisedToolCall asks the configured model to choose from the
// current activity catalog and returns its typed rejection to the workflow.
func repeatedUnadvertisedToolCall(
	ctx context.Context,
	agentContext planner.PlannerContext,
) (*planner.PlanResult, error) {
	client, ok := agentContext.ModelClient("test")
	if !ok {
		return nil, errors.New("test model is not registered")
	}
	_, err := client.Complete(ctx, &model.Request{
		Model: "test",
		Tools: agentContext.AdvertisedToolDefinitions(),
	})
	return nil, err
}
