package runtime

// These tests run mixed provider and planner-created calls through real planner
// activities, tool execution, saved history, and suspension restoration. All
// provider messages and tool results are synthetic and stay inside the process.

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestWorkflowMixedCallsPreserveCompleteTranscript(t *testing.T) {
	for _, test := range []struct {
		name        string
		modelCalls  int
		codeCalls   int
		bookkeeping bool
		suspend     bool
	}{
		{name: "mixed", modelCalls: 1, codeCalls: 1},
		{name: "model only", modelCalls: 1},
		{name: "planner only", codeCalls: 2},
		{name: "multiple model calls and bookkeeping", modelCalls: 2, codeCalls: 1, bookkeeping: true},
		{name: "mixed restored checkpoint", modelCalls: 1, codeCalls: 1, suspend: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			const agentID = agent.Ident("records.agent")
			lookup := newAnyJSONSpec("records.lookup")
			bookkeeping := newAnyJSONSpec("records.note")
			bookkeeping.Bookkeeping = true
			specs := []tools.ToolSpec{lookup, bookkeeping}
			providerMessages := make([]model.Message, 2)
			providerMessages[0] = model.Message{
				Role: model.ConversationRoleAssistant,
				Meta: map[string]any{"provider": "synthetic-original"},
				Parts: []model.Part{
					model.ThinkingPart{Text: "Read the requested records.", Signature: "synthetic-thinking-signature", Final: true},
					model.ThinkingPart{Redacted: []byte("synthetic-redacted-reasoning"), Final: true},
					model.TextPart{Text: "Looking up the records."},
				},
			}
			for i := range test.modelCalls {
				providerMessages[0].Parts = append(providerMessages[0].Parts, model.ToolUsePart{
					ID: fmt.Sprintf("provider-%d", i), Name: lookup.Name.String(),
					Input:            rawjson.Message(fmt.Sprintf(`{"query":"requested-%d"}`, i)),
					ThoughtSignature: "synthetic-call-signature",
				})
			}
			// A separate text fragment verifies that adding declarations keeps
			// every original provider message in its original position.
			providerMessages[1] = model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "The selected calls are ready."}},
			}
			originalProvider := make([]*model.Message, len(providerMessages))
			for i := range providerMessages {
				originalProvider[i] = &providerMessages[i]
			}
			before := canonicalHistory(t, originalProvider)
			var executed []ToolCall
			resumes := 0
			pl := &stubPlanner{
				start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
					result := &planner.PlanResult{}
					if test.modelCalls > 0 {
						client, ok := input.Agent.PlannerModelClient("synthetic")
						require.True(t, ok)
						response, err := client.Complete(ctx, &model.Request{
							Model: "synthetic", Messages: input.Messages, Tools: input.Agent.AdvertisedToolDefinitions(),
						})
						require.NoError(t, err)
						for _, call := range response.ToolCalls() {
							request, err := planner.ToolRequestFromModelCall(call)
							require.NoError(t, err)
							// Planner code may compile the selected call into a
							// different executable payload while its original input
							// remains in the transcript presented to the next call.
							request.Payload = rawjson.Message(`{"query":"compiled"}`)
							result.ToolCalls = append(result.ToolCalls, request)
						}
					}
					for i := range test.codeCalls {
						result.ToolCalls = append(result.ToolCalls, planner.ToolRequest{
							Name: lookup.Name, Payload: rawjson.Message(fmt.Sprintf(`{"query":"additional-%d"}`, i)),
						})
					}
					if test.bookkeeping {
						result.ToolCalls = append(result.ToolCalls, planner.ToolRequest{
							Name: bookkeeping.Name, Payload: rawjson.Message(`{"record":"checked"}`),
						})
					}
					return result, nil
				},
				resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumes++
					require.NoError(t, transcript.ValidatePlannerTranscript(input.Messages))
					assert.Len(t, input.ToolOutputs, test.modelCalls+test.codeCalls)
					if test.modelCalls > 0 {
						assert.Equal(t, before, canonicalHistory(t, input.Messages[1:1+len(originalProvider)]))
					}
					uses := make(map[string]model.ToolUsePart)
					results := make(map[string]model.ToolResultPart)
					for _, message := range input.Messages {
						for _, part := range message.Parts {
							switch part := part.(type) {
							case model.ToolUsePart:
								_, duplicate := uses[part.ID]
								assert.False(t, duplicate)
								uses[part.ID] = part
							case model.ToolResultPart:
								_, duplicate := results[part.ToolUseID]
								assert.False(t, duplicate)
								results[part.ToolUseID] = part
							}
						}
					}
					assert.Len(t, uses, len(executed))
					assert.Len(t, results, len(executed))
					for i, call := range executed {
						id := call.ToolCallID
						if i < test.modelCalls {
							id = fmt.Sprintf("provider-%d", i)
							var modelID string
							for _, output := range input.ToolOutputs {
								if output.ToolCallID == call.ToolCallID {
									modelID = output.ModelToolCallID
								}
							}
							assert.Equal(t, id, modelID)
							assert.NotEqual(t, id, call.ToolCallID)
							assert.JSONEq(t, `{"query":"compiled"}`, string(call.Payload))
							assert.JSONEq(t, fmt.Sprintf(`{"query":"requested-%d"}`, i), string(uses[id].Input))
						} else {
							assert.Empty(t, call.ModelToolCallID)
							assert.Equal(t, call.Payload, uses[id].Input)
							assert.Empty(t, uses[id].ThoughtSignature)
						}
						assert.Equal(t, call.TranscriptName().String(), uses[id].Name)
						assert.Equal(t, map[string]any{"execution": call.ToolCallID}, results[id].Content)
						assert.False(t, results[id].IsError)
					}
					if test.modelCalls > 0 {
						client, ok := input.Agent.PlannerModelClient("synthetic")
						require.True(t, ok)
						response, err := client.Complete(ctx, &model.Request{Model: "synthetic", Messages: input.Messages})
						require.NoError(t, err)
						return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
					}
					return finalPlannerResult("done"), nil
				},
			}
			rt := New(newTestStore(), WithLogger(telemetry.NoopLogger{}))
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
				Name: "records", Specs: specs,
				Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
					executed = append(executed, *call)
					result := &ToolExecutionResult{ToolResult: &planner.ToolResult{
						Name: call.Name, ToolCallID: call.ToolCallID,
						Result: map[string]any{"execution": call.ToolCallID},
					}}
					if test.suspend && len(executed) == test.modelCalls+test.codeCalls {
						result.Clarification = &ToolClarification{ID: "select-group", Question: "Which record group?"}
					}
					return result, nil
				},
			}))
			rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{agentID: specs}
			providerCalls := 0
			rt.models["synthetic"] = mustTestModelClient(stubModelClient{
				complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
					providerCalls++
					if providerCalls == 1 {
						return &model.Response{Content: providerMessages, StopReason: "tool_use"}, nil
					}
					require.NoError(t, transcript.ValidatePlannerTranscript(request.Messages))
					return testModelResponse([]model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}}}), nil
				},
			})
			reg := AgentRegistration{
				Definition: NewAgentDefinition(
					AgentRoute{ID: agentID, WorkflowName: "mixed.workflow", DefaultTaskQueue: "mixed.queue"},
					specs, nil, nil, []tools.Ident{lookup.Name, bookkeeping.Name}, nil, nil,
				), Planner: pl,
				WorkflowHandler:  rt.ExecuteWorkflow,
				PlanActivityName: "plan", ResumeActivityName: "resume", ExecuteToolActivity: "execute",
				Policy: RunPolicy{MaxToolCalls: 8},
			}
			require.NoError(t, rt.RegisterAgent(t.Context(), reg))
			_, err := createSessionForTest(t.Context(), rt.Store, "mixed-session")
			require.NoError(t, err)
			input := &RunInput{
				Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))},
				RunID:   "mixed-run", SessionID: "mixed-session", TurnID: "mixed-turn",
			}
			wf := &routeWorkflowContext{
				ctx: t.Context(), runID: input.RunID, hookRuntime: rt,
				plannerRoutes: map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error){
					"plan": rt.PlanStartActivity, "resume": rt.PlanResumeActivity,
				},
				toolRoutes: map[string]func(context.Context, *ToolInput) (*ToolOutput, error){"execute": rt.ExecuteToolActivity},
			}
			publishTestRunInput(t, rt, input, []*model.Message{userMsg("Look up the requested records.")})
			out, err := rt.ExecuteWorkflow(wf, input)
			require.NoError(t, err)
			if test.suspend {
				require.NotNil(t, out.Suspension)
				assert.Zero(t, resumes)
				checkpoint, err := decodeWorkflowCheckpoint(out.Suspension, reg.Definition)
				require.NoError(t, err)
				saved, err := transcript.BuildMessagesFromRunLogPrefix(t.Context(), rt.Store, input.RunID, checkpoint.HistoryEndID)
				require.NoError(t, err)
				require.NoError(t, transcript.ValidatePlannerTranscript(saved))
				assert.Equal(t, before, canonicalHistory(t, saved[1:1+len(originalProvider)]))
				out, err = rt.MustClient(agentID).Continue(t.Context(), input.SessionID, input.RunID,
					"restored-run", "restored-turn", &api.PendingInputResponse{
						Clarification: &api.ClarificationAnswer{ID: "select-group", Answer: "Group A"},
					}, WorkflowOptions{})
				require.NoError(t, err)
			}
			require.NotNil(t, out.Final)
			assert.Equal(t, "done", out.Final.Text())
			assert.Equal(t, 1, resumes)
			assert.Equal(t, before, canonicalHistory(t, originalProvider))
			assert.Len(t, executed, test.modelCalls+test.codeCalls+map[bool]int{true: 1}[test.bookkeeping])
			if test.modelCalls > 0 {
				assert.Equal(t, 2, providerCalls)
			}
		})
	}
}
