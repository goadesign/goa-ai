// Replacement activities receive a complete authored example after each
// independent schema failure. Rejected attempts remain unexecuted and outside
// accepted history; the planner may choose another authorized tool.
package runtime

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestRunLoopToolExampleSurvivesSuccessiveSchemaFailures(t *testing.T) {
	kickoff := newAnyJSONSpec("catalog.kickoff")
	alternative := newAnyJSONSpec("catalog.alternative")
	lookup := newAnyJSONSpec("catalog.lookup")
	const schema = `{"type":"object","properties":{"request":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]},"window":{"type":"object","properties":{"hours":{"type":"integer"}},"required":["hours"]}},"required":["request","window"],"additionalProperties":false}`
	const example = `{"request":{"query":"example quantity"},"window":{"hours":7}}`
	lookup.Payload.Schema = rawjson.Message(schema)
	lookup.Payload.SchemaWithoutRootExample = rawjson.Message(schema)
	lookup.Payload.ExampleJSON = rawjson.Message(example)
	lookup.Payload.Fields = []tools.FieldMetadata{
		{Path: []tools.FieldPathSegment{tools.FixedField("request")}, JSONType: "object"},
		{Path: []tools.FieldPathSegment{tools.FixedField("window")}, JSONType: "object"},
	}
	var providerCalls, lookupCalls, alternativeCalls int
	h := newRecoveryHarness(t, "example-correction", []tools.ToolSpec{kickoff, lookup, alternative},
		func(_ context.Context, call *ToolCall) (*planner.ToolResult, error) {
			if call.Name == lookup.Name {
				lookupCalls++
			}
			if call.Name == alternative.Name {
				alternativeCalls++
			}
			return successfulToolResult(call), nil
		},
		func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
			messages := input.Messages
			require.NoError(t, transcript.ValidatePlannerTranscript(messages))
			for _, message := range messages {
				for _, part := range message.Parts {
					if call, ok := part.(model.ToolUsePart); ok {
						assert.NotEqual(t, lookup.Name.String(), call.Name, "rejected calls never enter accepted history")
					}
				}
			}
			if len(input.ToolOutputs) == 2 {
				return finalPlannerResult("alternative completed"), nil
			}
			assert.False(t, input.SynthesisOnly)
			assertAdvertisedTools(t, input, kickoff.Name, lookup.Name, alternative.Name)
			messages, err := reminder.InjectMessages(messages, input.Reminders)
			require.NoError(t, err)
			client, ok := input.Agent.PlannerModelClient("test")
			require.True(t, ok)
			response, err := client.Complete(ctx, &model.Request{Model: "test", Messages: messages, Tools: input.Agent.AdvertisedToolDefinitions()})
			if err != nil {
				return nil, err
			}
			call, err := planner.ToolRequestFromModelCall(response.ToolCalls()[0])
			require.NoError(t, err)
			return &planner.PlanResult{ToolCalls: []planner.ToolRequest{call}}, nil
		})
	h.runtime.models["test"] = mustTestModelClient(stubModelClient{complete: func(_ context.Context, request *model.Request) (*model.Response, error) {
		providerCalls++
		require.NoError(t, transcript.ValidatePlannerTranscript(request.Messages))
		var text string
		for _, message := range request.Messages {
			for _, part := range message.Parts {
				if value, ok := part.(model.TextPart); ok {
					text += value.Text
				}
			}
		}
		name := lookup.Name
		payload := rawjson.Message(`{"window":{"hours":12}}`)
		if providerCalls > 1 {
			assert.Contains(t, text, example)
			assert.Contains(t, text, "use values and a valid variant appropriate to the request")
			assert.NotContains(t, text, "submitted quantity")
		}
		switch providerCalls {
		case 2:
			assert.Contains(t, text, `Field "request" is required.`)
			payload = rawjson.Message(`{"request":{"query":"submitted quantity"}}`)
		case 3:
			assert.Contains(t, text, `Field "window" is required.`)
			name, payload = alternative.Name, rawjson.Message(`{}`)
		}
		return testModelResponseWithUsage(nil, model.TokenUsage{InputTokens: 6, OutputTokens: 4, TotalTokens: 10}, model.ToolCall{
			ID: fmt.Sprintf("attempt-%d", providerCalls), Name: name, Payload: payload,
		}), nil
	}})
	out, err := h.run(&PlanResult{ToolCalls: []ToolCall{{ToolCallID: "kickoff", Name: kickoff.Name, Payload: rawjson.Message(`{}`)}}}, policy.CapsState{
		MaxToolCalls: 3, RemainingToolCalls: 3, MaxRecoveryTurns: 2, RemainingRecoveryTurns: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, "alternative completed", out.Final.Text())
	assert.Equal(t, 3, providerCalls)
	assert.Zero(t, lookupCalls)
	assert.Equal(t, 1, alternativeCalls)
	assert.Equal(t, 30, out.Usage.TotalTokens)
}
