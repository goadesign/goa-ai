package tooloutput

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/completion"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	testOutput struct {
		Value string `json:"value"`
	}

	recordingProvider struct {
		mu        sync.Mutex
		requests  []*model.Request
		responses []*model.Response
		errors    []error
	}
)

const outputSchema = `{
  "type": "object",
  "additionalProperties": false,
  "required": ["value"],
  "properties": {
    "value": {"type": "string", "minLength": 1}
  }
}`

func (p *recordingProvider) Complete(_ context.Context, request *model.Request) (*model.Response, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	index := len(p.requests)
	p.requests = append(p.requests, request)
	var response *model.Response
	if index < len(p.responses) {
		response = p.responses[index]
	}
	var err error
	if index < len(p.errors) {
		err = p.errors[index]
	}
	return response, err
}

func (p *recordingProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, errors.New("unexpected stream")
}

func TestRunReturnsTypedPayloadFromExactForcedTool(t *testing.T) {
	provider := &recordingProvider{responses: []*model.Response{
		toolResponse(`{"value":"accepted"}`),
	}}
	request := &model.Request{
		ModelClass:  model.ModelClassSmall,
		Messages:    userMessages("Return the result."),
		Temperature: 0.25,
		MaxTokens:   128,
	}
	spec := outputSpec()

	result, err := Run[testOutput](t.Context(), testClient(t, provider), request, spec)

	require.NoError(t, err)
	assert.Equal(t, testOutput{Value: "accepted"}, result)
	require.Len(t, provider.requests, 1)
	actual := provider.requests[0]
	assert.Equal(t, model.ModelClassSmall, actual.ModelClass)
	assert.InDelta(t, 0.25, actual.Temperature, 0.001)
	assert.Equal(t, 128, actual.MaxTokens)
	require.Len(t, actual.Tools, 1)
	assert.Equal(t, "results.submit", actual.Tools[0].Name)
	assert.True(t, bytes.Equal(spec.Schema, actual.Tools[0].Input.Contract().Schema))
	require.NotNil(t, actual.ToolChoice)
	assert.Equal(t, model.ToolChoiceModeTool, actual.ToolChoice.Mode)
	assert.Equal(t, "results.submit", actual.ToolChoice.Name)
	assert.Nil(t, actual.StructuredOutput)
	assert.False(t, actual.Stream)
	assert.Empty(t, request.Tools)
	assert.Nil(t, request.ToolChoice)
}

func TestRunCorrectsMalformedAndSchemaInvalidToolArguments(t *testing.T) {
	tests := []struct {
		name    string
		invalid rawjson.Message
	}{
		{name: "malformed JSON", invalid: rawjson.Message(`{`)},
		{name: "schema invalid", invalid: rawjson.Message(`{"value":7}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &recordingProvider{responses: []*model.Response{
				toolResponseBytes(test.invalid),
				toolResponse(`{"value":"corrected"}`),
			}}

			result, err := Run[testOutput](
				t.Context(),
				testClient(t, provider),
				outputRequest(),
				outputSpec(),
			)

			require.NoError(t, err)
			assert.Equal(t, testOutput{Value: "corrected"}, result)
			require.Len(t, provider.requests, 2)
			assertCorrectionRequest(t, provider.requests[1])
		})
	}
}

func TestRunCorrectsPayloadCodecFailure(t *testing.T) {
	spec := outputSpec()
	decode := spec.Codec.FromJSON
	spec.Codec.FromJSON = func(data []byte) (testOutput, error) {
		value, err := decode(data)
		if err != nil {
			return testOutput{}, err
		}
		if value.Value == "reject" {
			return testOutput{}, tools.NewValidationError(
				"value is not accepted by the generated contract",
				[]*tools.FieldIssue{{Field: "value", Constraint: "invalid_enum_value"}},
				nil,
			)
		}
		return value, nil
	}
	provider := &recordingProvider{responses: []*model.Response{
		toolResponse(`{"value":"reject"}`),
		toolResponse(`{"value":"corrected"}`),
	}}

	result, err := Run[testOutput](
		t.Context(),
		testClient(t, provider),
		outputRequest(),
		spec,
	)

	require.NoError(t, err)
	assert.Equal(t, testOutput{Value: "corrected"}, result)
	require.Len(t, provider.requests, 2)
	assertCorrectionRequest(t, provider.requests[1])
}

func TestRunStopsAtCorrectionCap(t *testing.T) {
	provider := &recordingProvider{responses: []*model.Response{
		toolResponse(`{"value":7}`),
		toolResponse(`{"value":7}`),
		toolResponse(`{"value":7}`),
		toolResponse(`{"value":7}`),
	}}

	_, err := Run[testOutput](
		t.Context(),
		testClient(t, provider),
		outputRequest(),
		outputSpec(),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), `completion tool "results.submit" did not succeed: recovery_cap`)
	assert.Len(t, provider.requests, 4)
}

func TestRunDoesNotRetryProviderError(t *testing.T) {
	want := errors.New("provider unavailable")
	provider := &recordingProvider{errors: []error{want}}

	_, err := Run[testOutput](
		t.Context(),
		testClient(t, provider),
		outputRequest(),
		outputSpec(),
	)

	require.ErrorIs(t, err, want)
	assert.Len(t, provider.requests, 1)
}

func TestRunRejectsConflictingRequestModesBeforeInference(t *testing.T) {
	tests := []struct {
		name    string
		request *model.Request
		want    string
	}{
		{name: "nil request", want: "request is required"},
		{
			name:    "tools",
			request: &model.Request{Tools: []*model.ToolDefinition{{}}},
			want:    "does not allow tool definitions",
		},
		{
			name:    "tool choice",
			request: &model.Request{ToolChoice: &model.ToolChoice{Mode: model.ToolChoiceModeAny}},
			want:    "does not allow tool choice",
		},
		{
			name:    "structured output",
			request: &model.Request{StructuredOutput: &model.StructuredOutput{}},
			want:    "does not allow structured output",
		},
		{
			name:    "stream",
			request: &model.Request{Stream: true},
			want:    "does not support streaming",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &recordingProvider{}

			_, err := Run[testOutput](t.Context(), testClient(t, provider), test.request, outputSpec())

			require.ErrorContains(t, err, test.want)
			assert.Empty(t, provider.requests)
		})
	}
}

func TestRunRejectsMultipleForcedCallsWithoutCorrection(t *testing.T) {
	response := toolResponse(`{"value":"one"}`)
	response.Content[0].Parts = append(response.Content[0].Parts, model.ToolUsePart{
		ID:    "call-2",
		Name:  "results.submit",
		Input: rawjson.Message(`{"value":"two"}`),
	})
	provider := &recordingProvider{responses: []*model.Response{response}}

	_, err := Run[testOutput](
		t.Context(),
		testClient(t, provider),
		outputRequest(),
		outputSpec(),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "completed output does not meet its contract")
	assert.Len(t, provider.requests, 1)
}

func TestRunRejectsMissingForcedCallWithoutCorrection(t *testing.T) {
	provider := &recordingProvider{responses: []*model.Response{{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "I cannot submit the result."}},
		}},
		StopReason: "end_turn",
	}}}

	_, err := Run[testOutput](
		t.Context(),
		testClient(t, provider),
		outputRequest(),
		outputSpec(),
	)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "completed output does not meet its contract")
	assert.Len(t, provider.requests, 1)
}

func TestPrivateToolSpecRejectsInternalGoTypeMismatch(t *testing.T) {
	spec := privateToolSpec(outputSpec())

	_, err := spec.Result.Codec.ToJSON(map[string]string{"value": "accepted"})

	require.ErrorContains(t, err, `internal result has Go type map[string]string, want tooloutput.testOutput`)
}

func outputSpec() completion.Spec[testOutput] {
	return completion.Spec[testOutput]{
		Name:        "results.submit",
		Description: "Submit the requested result.",
		Schema:      rawjson.Message(outputSchema),
		Codec: tools.JSONCodec[testOutput]{
			ToJSON: func(value testOutput) ([]byte, error) {
				return json.Marshal(value)
			},
			FromJSON: func(data []byte) (testOutput, error) {
				var output testOutput
				if err := json.Unmarshal(data, &output); err != nil {
					return testOutput{}, err
				}
				return output, nil
			},
		},
	}
}

func testClient(t *testing.T, provider model.Provider) model.Client {
	t.Helper()
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	return client
}

func toolResponse(payload string) *model.Response {
	return toolResponseBytes(rawjson.Message(payload))
}

func toolResponseBytes(payload rawjson.Message) *model.Response {
	return &model.Response{
		Content: []model.Message{{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{model.ToolUsePart{
				ID:    "call-1",
				Name:  "results.submit",
				Input: payload,
			}},
		}},
		StopReason: "tool_use",
	}
}

func userMessages(text string) []*model.Message {
	return []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: text}},
	}}
}

func outputRequest() *model.Request {
	return &model.Request{
		ModelClass: model.ModelClassDefault,
		Messages:   userMessages("Return the result."),
	}
}

func assertCorrectionRequest(t *testing.T, request *model.Request) {
	t.Helper()
	require.Len(t, request.Tools, 1)
	assert.Equal(t, "results.submit", request.Tools[0].Name)
	require.NotNil(t, request.ToolChoice)
	assert.Equal(t, model.ToolChoiceModeTool, request.ToolChoice.Mode)
	assert.Equal(t, "results.submit", request.ToolChoice.Name)
	assert.Contains(t, systemText(request), "system-reminder")
	assert.Contains(t, systemText(request), "replacement tool call")
}

func systemText(request *model.Request) string {
	var text string
	for _, message := range request.Messages {
		if message.Role != model.ConversationRoleSystem {
			continue
		}
		for _, part := range message.Parts {
			if value, ok := part.(model.TextPart); ok {
				text += value.Text
			}
		}
	}
	return text
}
