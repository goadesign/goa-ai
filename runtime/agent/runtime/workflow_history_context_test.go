package runtime

// Exercise ordinary and finalization recovery through the workflow loop and
// serialized activity records. Historical outputs omit derived history; replay
// keeps that omission and never reconstructs a summary inside workflow code.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestWorkflowHistoryContextSurvivesSerializedRecovery(t *testing.T) {
	for _, finalizing := range []bool{false, true} {
		for _, historical := range []bool{false, true} {
			name := map[bool]string{false: "ordinary", true: "finalization"}[finalizing] + "/" + map[bool]string{false: "current records", true: "historical records"}[historical]
			t.Run(name, func(t *testing.T) {
				load := newAnyJSONSpec("catalog.load")
				resumes, fresh, reused := 0, 0, 0
				h := newRecoveryHarness(t, "history-context", []tools.ToolSpec{load},
					func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
						if finalizing {
							return &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID,
								Failure: testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "source unavailable")}, nil
						}
						return successfulToolResult(call), nil
					},
					func(ctx context.Context, in *planner.PlanResumeInput) (*planner.PlanResult, error) {
						resumes++
						assert.Equal(t, finalizing, in.Finalize != nil)
						client, ok := in.Agent.PlannerModelClient("test")
						require.True(t, ok)
						response, err := client.Complete(ctx, &model.Request{Model: "test", Messages: in.Messages})
						if err != nil {
							return nil, err
						}
						if resumes == 1 {
							return nil, planner.NewRecoverableModelAnswerError(errors.New("unsupported conclusion"), &planner.FinalResponse{Message: &response.Content[0]}, "Use the supplied evidence.")
						}
						return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &response.Content[0]}}, nil
					})
				h.base.Messages = requestHistoryMessages()
				original := canonicalHistory(t, h.base.Messages)
				h.runtime.models["test"] = mustTestModelClient(stubModelClient{complete: func(context.Context, *model.Request) (*model.Response, error) {
					return testModelResponse([]model.Message{*assistantTextMsg("supported answer")}), nil
				}})
				h.registration.Policy.History = func(_ context.Context, req *model.Request, _ model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
					if prior == nil {
						fresh++
					} else {
						reused++
					}
					return requestHistoryResult(req.Messages, "saved original evidence", prior)
				}
				h.runtime.agents[h.input.AgentID] = h.registration
				h.workflow.plannerRoutes["resume"] = func(ctx context.Context, input *PlanActivityInput) (*PlanActivityOutput, error) {
					encoded, err := json.Marshal(input)
					if err != nil {
						return nil, err
					}
					var decoded PlanActivityInput
					if err := json.Unmarshal(encoded, &decoded); err != nil {
						return nil, err
					}
					out, err := h.runtime.PlanResumeActivity(ctx, &decoded)
					if err != nil {
						return nil, err
					}
					if historical {
						out.HistoryContext = nil
					}
					encoded, err = json.Marshal(out)
					if err != nil {
						return nil, err
					}
					var restored PlanActivityOutput
					if err := json.Unmarshal(encoded, &restored); err != nil {
						return nil, err
					}
					return &restored, nil
				}
				out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{Name: load.Name, ToolCallID: "load-call", Payload: rawjson.Message(`{}`)}}, SynthesizeAfterTools: true}, initialCaps(RunPolicy{MaxToolCalls: 4, MaxRecoveryTurns: 3}))
				require.NoError(t, err)
				assert.Equal(t, "supported answer", out.Final.Text())
				assert.Equal(t, 2, resumes)
				assert.Equal(t, original, canonicalHistory(t, h.base.Messages[:4]))
				if historical {
					assert.Equal(t, 2, fresh)
					assert.Zero(t, reused)
					assert.Nil(t, h.base.HistoryContext)
				} else {
					assert.Equal(t, 1, fresh)
					assert.Equal(t, 1, reused)
					require.NotNil(t, h.base.HistoryContext)
				}
			})
		}
	}
}

func TestWorkflowHistoryContextResumeRequestOwnsCopy(t *testing.T) {
	messages := requestHistoryMessages()
	result, err := requestHistoryResult(messages, "summary", nil)
	require.NoError(t, err)
	source, positions, _, err := historySourceHashes(messages, 2)
	require.NoError(t, err)
	saved := &api.HistoryContext{Summary: *result.Summary, SourceSHA256: source, SourcePositionsSHA256: positions}
	base := &workflowConversation{Messages: messages, HistoryContext: saved}
	attempt := 1
	rt := newTestRuntimeWithPlanner("service.agent", &stubPlanner{})
	input, err := rt.buildNextResumeRequest("service.agent", base, nil, nil, nil, false, nil, nil, &attempt)
	require.NoError(t, err)
	require.NotNil(t, input.HistoryContext)
	input.HistoryContext.Summary.Message.Parts[0] = model.TextPart{Text: "request-owned edit"}
	assert.Equal(t, "summary", saved.Summary.Message.Parts[0].(model.TextPart).Text)
}

func TestWorkflowHistoryContextCodeOnlyStepRetainsPrior(t *testing.T) {
	load := newAnyJSONSpec("catalog.load")
	h := newRecoveryHarness(t, "history-code-only", []tools.ToolSpec{load},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			return successfulToolResult(call), nil
		},
		func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return finalPlannerResult("finished without a model"), nil
		})
	h.base.Messages = requestHistoryMessages()
	result, err := requestHistoryResult(h.base.Messages, "existing summary", nil)
	require.NoError(t, err)
	source, positions, _, err := historySourceHashes(h.base.Messages, 2)
	require.NoError(t, err)
	saved := &api.HistoryContext{Summary: *result.Summary, SourceSHA256: source, SourcePositionsSHA256: positions}
	h.base.HistoryContext = saved
	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{Name: load.Name, ToolCallID: "load-call", Payload: rawjson.Message(`{}`)}}, SynthesizeAfterTools: true}, initialCaps(RunPolicy{MaxToolCalls: 4}))
	require.NoError(t, err)
	assert.Equal(t, "finished without a model", out.Final.Text())
	assert.Same(t, saved, h.base.HistoryContext)
}
