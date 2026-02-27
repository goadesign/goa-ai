//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRunPlanActivityUsesOptions(t *testing.T) {
	rt := &Runtime{
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		Bus:     noopHooks{},
	}
	wf := &testWorkflowContext{
		ctx:           context.Background(),
		hasPlanResult: true,
		planResult: &planner.PlanResult{
			FinalResponse: &planner.FinalResponse{
				Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}},
			},
		},
	}

	opts := engine.ActivityOptions{
		Queue:       "custom_queue",
		Timeout:     30 * time.Second,
		RetryPolicy: engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Second, BackoffCoefficient: 2},
	}
	_, err := rt.runPlanActivity(wf, "calc.agent.plan", opts, PlanActivityInput{}, time.Time{})
	require.NoError(t, err)
	require.Equal(t, opts.Queue, wf.lastPlannerCall.Options.Queue)
	require.Equal(t, opts.Timeout, wf.lastPlannerCall.Options.Timeout)
	require.Equal(t, opts.RetryPolicy, wf.lastPlannerCall.Options.RetryPolicy)
}

func TestPlanStartActivityInvokesPlanner(t *testing.T) {
	called := false
	pl := &stubPlanner{start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		called = true
		require.NotNil(t, input)
		require.Equal(t, run.Context{RunID: "run-123"}, input.RunContext)
		require.Len(t, input.Messages, 1)
		require.Equal(t, "hello", agentMessageText(input.Messages[0]))
		require.NotNil(t, input.Agent)
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{AgentID: "service.agent", RunID: "run-123", Messages: []*model.Message{{Role: "user", Parts: []model.Part{model.TextPart{Text: "hello"}}}}, RunContext: run.Context{RunID: "run-123"}}
	out, err := rt.PlanStartActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
	require.NotNil(t, out.Result.FinalResponse)
}

func TestPlanResumeActivityPassesToolResults(t *testing.T) {
	called := false
	toolResults := []*api.ToolEvent{{Name: "svc.ts.tool"}}
	pl := &stubPlanner{resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		called = true
		require.NotNil(t, input)
		require.Len(t, input.ToolResults, 1)
		require.Equal(t, tools.Ident("svc.ts.tool"), input.ToolResults[0].Name)
		require.Equal(t, 3, input.RunContext.Attempt)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "svc.other.tool"}}}, nil
	}}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{AgentID: "service.agent", RunID: "run-123", RunContext: run.Context{RunID: "run-123", Attempt: 3}, ToolResults: toolResults}
	out, err := rt.PlanResumeActivity(context.Background(), &input)
	require.NoError(t, err)
	require.True(t, called)
	require.Len(t, out.Result.ToolCalls, 1)
}

func TestPlanResumeActivityPreservesEmptyRawJSONPayloads(t *testing.T) {
	pl := &stubPlanner{
		resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			return &planner.PlanResult{
				ToolCalls: []planner.ToolRequest{
					{
						Name:    "svc.other.tool",
						Payload: rawjson.RawJSON([]byte{}),
					},
				},
				Await: planner.NewAwait(
					planner.AwaitQuestionsItem(&planner.AwaitQuestions{
						ID:         "await-q",
						ToolName:   "chat.ask_question.ask_question",
						ToolCallID: "call-q",
						Payload:    rawjson.RawJSON([]byte{}),
					}),
					planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{
						ID: "await-ext",
						Items: []planner.AwaitToolItem{
							{
								Name:       "external.one",
								ToolCallID: "call-ext",
								Payload:    rawjson.RawJSON([]byte{}),
							},
						},
					}),
				),
			}, nil
		},
	}
	rt := newTestRuntimeWithPlanner("service.agent", pl)
	input := PlanActivityInput{
		AgentID:    "service.agent",
		RunID:      "run-123",
		RunContext: run.Context{RunID: "run-123"},
	}
	out, err := rt.PlanResumeActivity(context.Background(), &input)
	require.NoError(t, err)
	require.Len(t, out.Result.ToolCalls, 1)
	require.NotNil(t, out.Result.ToolCalls[0].Payload)
	require.Len(t, out.Result.ToolCalls[0].Payload, 0)
	require.NotNil(t, out.Result.Await)
	require.Len(t, out.Result.Await.Items, 2)
	require.NotNil(t, out.Result.Await.Items[0].Questions)
	require.NotNil(t, out.Result.Await.Items[0].Questions.Payload)
	require.Len(t, out.Result.Await.Items[0].Questions.Payload, 0)
	require.NotNil(t, out.Result.Await.Items[1].ExternalTools)
	require.Len(t, out.Result.Await.Items[1].ExternalTools.Items, 1)
	require.NotNil(t, out.Result.Await.Items[1].ExternalTools.Items[0].Payload)
	require.Len(t, out.Result.Await.Items[1].ExternalTools.Items[0].Payload, 0)
}

func TestNormalizeTranscriptRawJSONNormalizesEmptyRawMessageValues(t *testing.T) {
	messages := []*model.Message{
		{
			Role: "assistant",
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "call-1",
					Name:  "tool.one",
					Input: json.RawMessage{},
				},
				model.ToolResultPart{
					ToolUseID: "call-1",
					Content: map[string]any{
						"payload": json.RawMessage{},
					},
				},
			},
			Meta: map[string]any{
				"raw": json.RawMessage{},
			},
		},
	}

	normalizeTranscriptRawJSON(messages)

	toolUse, ok := messages[0].Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	require.Nil(t, toolUse.Input)
	toolResult, ok := messages[0].Parts[1].(model.ToolResultPart)
	require.True(t, ok)
	content, ok := toolResult.Content.(map[string]any)
	require.True(t, ok)
	require.Nil(t, content["payload"])
	require.Nil(t, messages[0].Meta["raw"])
}
