// These tests run ordinary questions and data answers through the workflow engine.
// The client sends messages and receives completed runs without external input.
package runtime

import (
	"context"
	"goa.design/goa-ai/runtime/agent/run"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestTextOnlyConversationCompletesWithoutInputProtocol(t *testing.T) {
	rt := New(newTestStore(), WithEngine(engineinmem.New()), WithLogger(telemetry.NoopLogger{}))
	executions := 0
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "records", Specs: genrecords.Specs(),
		Execute: func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			executions++
			assert.True(t, run.IsTextOnly(ctx))
			assert.True(t, call.TextOnly)
			return Executed(&planner.ToolResult{Name: call.Name, Result: &genrecords.ReadResult{Count: 2}}), nil
		},
	}))
	impl := &stubPlanner{
		start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			assert.True(t, input.RunContext.TextOnly)
			definitions := input.Agent.AdvertisedToolDefinitions()
			require.Len(t, definitions, 1)
			assert.NotContains(t, string(definitions[0].Input.Contract().Schema), "render_ui")
			if input.RunContext.RunID == "question" {
				return finalPlannerResult("Which records should I inspect?"), nil
			}
			assert.Contains(t, input.Messages[len(input.Messages)-1].Text(), "active records")
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: genrecords.Read, Payload: rawjson.Message(`{"query":"active"}`)}}}, nil
		},
		resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			assert.True(t, input.RunContext.TextOnly)
			require.Len(t, input.ToolOutputs, 1)
			for _, message := range input.Messages {
				assert.NotContains(t, message.Text(), "card is displayed")
			}
			return finalPlannerResult("There are 2 active records. [Download](https://example.com/records.csv)"), nil
		},
	}
	require.NoError(t, rt.RegisterAgent(t.Context(), correctionTestRegistration(rt, impl, genrecords.Specs())))
	_, err := createSessionForTest(t.Context(), rt.Store, "conversation")
	require.NoError(t, err)
	client := rt.MustClient("catalog.agent")
	first, err := client.Run(t.Context(), "conversation", []*model.Message{userMsg("Inspect records.")}, WithRunID("question"), WithTurnID("first"), WithTextOnly(true))
	require.NoError(t, err)
	require.Nil(t, first.Suspension)
	require.NotNil(t, first.Final)
	assert.Equal(t, "Which records should I inspect?", first.Final.Text())
	second, err := client.Run(t.Context(), "conversation", []*model.Message{userMsg("Inspect records."), first.Final, userMsg("Inspect the active records.")}, WithRunID("answer"), WithTurnID("second"), WithTextOnly(true))
	require.NoError(t, err)
	require.Nil(t, second.Suspension)
	require.NotNil(t, second.Final)
	assert.Contains(t, second.Final.Text(), "2 active records")
	assert.Contains(t, second.Final.Text(), "https://example.com/records.csv")
	assert.Equal(t, 1, executions)
	for _, id := range []string{"question", "answer"} {
		page, err := rt.Store.ListRunRecords(t.Context(), id, "", 100)
		require.NoError(t, err)
		require.Empty(t, page.NextCursor)
		started, completed := false, false
		for _, record := range page.Events {
			switch record.Type {
			case hooks.RunStarted, hooks.ToolResultReceived, hooks.RunCompleted:
			default:
				continue
			}
			event, err := hooks.DecodeRunlogEvent(record)
			require.NoError(t, err)
			switch value := event.(type) {
			case *hooks.RunStartedEvent:
				started = true
				assert.Equal(t, "true", value.Labels["runtime.text_only"])
			case *hooks.ToolResultReceivedEvent:
				assert.Empty(t, value.ServerData)
			case *hooks.RunCompletedEvent:
				completed = true
				assert.Equal(t, "success", value.Status)
			}
		}
		assert.True(t, started)
		assert.True(t, completed)
	}
	saved, err := transcript.BuildMessagesFromRunLog(t.Context(), rt.Store, "answer")
	require.NoError(t, err)
	assert.Contains(t, saved[len(saved)-1].Text(), "https://example.com/records.csv")
}

func TestTextOnlyMissingFieldRecoveryAsksThroughOrdinaryMessage(t *testing.T) {
	for _, textOnly := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary structured question", true: "text question"}[textOnly], func(t *testing.T) {
			rt := New(newTestStore(), WithEngine(engineinmem.New()), WithLogger(telemetry.NoopLogger{}))
			spec := genrecords.SpecRead()
			executions, resumes := 0, 0
			require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{spec}, Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
				executions++
				return Executed(&planner.ToolResult{Name: call.Name, Failure: &planner.ToolFailure{
					Error: planner.NewToolError("record selection needs more information"), Kind: planner.FailureInvalidCall,
					Recovery: planner.RecoveryDirective{Action: planner.RecoveryCorrectCall, Issues: []*tools.FieldIssue{{Field: "query", Constraint: "missing_field"}}},
				}}), nil
			}}))
			rt.models["test"] = mustTestModelClient(stubModelClient{complete: func(context.Context, *model.Request) (*model.Response, error) {
				return &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{
					model.ToolUsePart{ID: "model-read", Name: genrecords.Read.String(), Input: rawjson.Message(`{"query":"requested"}`)},
				}}}, StopReason: "tool_use"}, nil
			}})
			impl := &stubPlanner{
				start: func(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
					client, ok := input.Agent.PlannerModelClient("test")
					require.True(t, ok)
					summary, err := client.Complete(ctx, &model.Request{Model: "test", Messages: input.Messages, Tools: input.Agent.AdvertisedToolDefinitions()})
					if err != nil {
						return nil, err
					}
					calls := summary.ToolCalls()
					require.Len(t, calls, 1)
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: calls[0].Name, ModelToolCallID: calls[0].ID, Payload: calls[0].Payload}}}, nil
				},
				resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					resumes++
					assert.True(t, input.RunContext.TextOnly)
					require.Len(t, input.ToolOutputs, 1)
					encoded := input.ToolOutputs[0].Failure.Recovery.ExampleJSON
					assert.NotContains(t, string(encoded), "render_ui")
					for _, message := range input.Messages {
						assert.NotContains(t, message.Text(), "render_ui")
					}
					return finalPlannerResult("Which records do you mean?"), nil
				},
			}
			require.NoError(t, rt.RegisterAgent(t.Context(), correctionTestRegistration(rt, impl, []tools.ToolSpec{spec})))
			_, err := createSessionForTest(t.Context(), rt.Store, "recovery")
			require.NoError(t, err)
			output, err := rt.MustClient("catalog.agent").Run(t.Context(), "recovery", []*model.Message{userMsg("Read the records.")}, WithRunID("read"), WithTurnID("turn"), WithTextOnly(textOnly))
			require.NoError(t, err)
			assert.Equal(t, 1, executions)
			if textOnly {
				assert.Equal(t, 1, resumes)
				require.Nil(t, output.Suspension)
				require.NotNil(t, output.Final)
				assert.Equal(t, "Which records do you mean?", output.Final.Text())
			} else {
				assert.Equal(t, 0, resumes)
				require.NotNil(t, output.Suspension)
				assert.Nil(t, output.Final)
			}
		})
	}
}
