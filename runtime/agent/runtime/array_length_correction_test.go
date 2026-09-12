// This test sends two invalid arrays through the runtime's real model
// validation and recovery path. The next planner receives both exact bounds,
// while rejected arguments never become executable conversation history.
package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestArrayLengthCorrectionReachesNextPlannerWithoutExecutingRejectedBatch(t *testing.T) {
	kickoff, batch := newAnyJSONSpec("catalog.kickoff"), newAnyJSONSpec("catalog.batch")
	batch.Payload = tools.TypeSpec{
		Name:   "Batch",
		Schema: rawjson.Message(`{"type":"object","properties":{"items":{"type":"array","items":{"type":"integer"},"minItems":1,"maxItems":2}},"required":["items"],"additionalProperties":false}`),
		Fields: []tools.FieldMetadata{{Path: []tools.FieldPathSegment{tools.FixedField("items")}, JSONType: "array"}},
		Codec:  tools.AnyJSONCodec,
	}
	const invalid = `{"items":[1,2,3]}`
	const empty = `{"items":[]}`
	const accepted = `{"items":[1,2]}`
	const guidance = "Tool call 1, input contract \"catalog.batch\" (diagnostic identifier, not a callable tool name):\nField \"items\" must contain at most 2 items.\n\nTool call 2, input contract \"catalog.batch\" (diagnostic identifier, not a callable tool name):\nField \"items\" must contain at least 1 items."
	var providerCalls, executions, resumes int
	h := newRecoveryHarness(t, "array-correction", []tools.ToolSpec{kickoff, batch},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == batch.Name {
				executions++
				if string(call.Payload) != accepted {
					t.Fatalf("executor received changed argument bytes: %s", call.Payload)
				}
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			resumes++
			if len(input.ToolOutputs) == 3 {
				return finalPlannerResult("accepted array completed"), nil
			}
			messages := input.Messages
			if resumes == 2 {
				require.Len(t, input.Reminders, 1)
				assert.Contains(t, input.Reminders[0].Text, guidance)
			}
			for _, message := range messages {
				for _, part := range message.Parts {
					if call, ok := part.(model.ToolUsePart); ok {
						assert.NotEqual(t, invalid, string(call.Input))
						assert.NotEqual(t, empty, string(call.Input))
					}
				}
			}
			// Planners compose the runtime-supplied reminders into model input.
			messages, err := reminder.InjectMessages(messages, input.Reminders)
			if err != nil {
				return nil, err
			}
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{Model: "test", Messages: messages, Tools: input.Agent.AdvertisedToolDefinitions()})
			if err != nil {
				var rejected *model.OutputValidationError
				require.ErrorAs(t, err, &rejected)
				assert.Equal(t, guidance, rejected.RecoveryCorrection())
				return nil, err
			}
			calls := response.ToolCalls()
			require.Len(t, calls, 2)
			var requests []planner.ToolRequest
			for _, call := range calls {
				request, err := planner.ToolRequestFromModelCall(call)
				require.NoError(t, err)
				requests = append(requests, request)
			}
			return &planner.PlanResult{ToolCalls: requests}, nil
		})
	h.runtime.models["test"] = mustTestModelClient(stubModelClient{complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
		providerCalls++
		payload, second, id := invalid, empty, "oversized"
		if providerCalls == 2 {
			payload, second, id = accepted, accepted, "accepted"
			found := false
			for _, message := range request.Messages {
				for _, part := range message.Parts {
					if text, ok := part.(model.TextPart); ok && message.Role == model.ConversationRoleSystem {
						if strings.Contains(text.Text, guidance) {
							found = true
						}
					}
				}
			}
			assert.True(t, found, "runtime must deliver the same correction to the next model request")
		}
		return testModelResponseWithUsage(nil, model.TokenUsage{InputTokens: 6, OutputTokens: 4, TotalTokens: 10}, model.ToolCall{ID: id, Name: batch.Name, Payload: rawjson.Message(payload)}, model.ToolCall{ID: id + "-second", Name: batch.Name, Payload: rawjson.Message(second)}), nil
	}})
	out, err := h.run(streamRecoveryKickoff(kickoff), policy.CapsState{MaxToolCalls: 3, RemainingToolCalls: 3, MaxRecoveryTurns: 1, RemainingRecoveryTurns: 1})
	require.NoError(t, err)
	assert.Equal(t, "accepted array completed", out.Final.Text())
	assert.Equal(t, 2, providerCalls)
	assert.Equal(t, 2, executions)
	assert.Equal(t, 3, resumes)
	require.NotNil(t, out.Usage)
	assert.Equal(t, 20, out.Usage.TotalTokens)
}
