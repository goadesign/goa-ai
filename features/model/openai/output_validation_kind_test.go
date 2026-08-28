// OpenAI output-classification tests drive malformed SDK responses through
// both the unary adapter and its stream pump to prove callers receive typed
// validation errors instead of internal classification panics.
package openai

import (
	"context"
	"errors"
	"testing"

	"github.com/openai/openai-go/responses"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestCompleteClassifiesMalformedReasoningOutput(t *testing.T) {
	tests := []struct {
		name     string
		response *responses.Response
	}{
		{name: "empty reasoning item", response: malformedReasoningResponse(t)},
		{
			name: "unsupported output item",
			response: &responses.Response{
				Status: responses.ResponseStatusCompleted,
				Output: []responses.ResponseOutputItemUnion{{}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := New(Options{
				DefaultModel: "gpt-4o",
				transport:    &mockTransport{completeResponse: test.response},
			})
			require.NoError(t, err)

			_, err = client.Complete(context.Background(), testOutputValidationRequest())

			requireOutputValidationKind(t, err, model.OutputValidationResponseShape)
		})
	}
}

func TestCompleteClassifiesNilResponseBeforeReadingUsage(t *testing.T) {
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{},
	})
	require.NoError(t, err)

	_, err = client.Complete(context.Background(), testOutputValidationRequest())

	requireOutputValidationKind(t, err, model.OutputValidationResponseShape)
}

func TestStreamPumpClassifiesMalformedReasoningOutput(t *testing.T) {
	stream := &mockStream{events: []responses.ResponseStreamEventUnion{
		mustStreamEvent(t, `{
			"type":"response.completed",
			"sequence_number":1,
			"response":{
				"model":"gpt-4o",
				"status":"completed",
				"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3},
				"output":[{
					"id":"reasoning-1",
					"type":"reasoning",
					"status":"completed",
					"summary":[]
				}]
			}
		}`),
	}}
	client, err := New(Options{
		DefaultModel: "gpt-4o",
		transport:    &mockTransport{stream: stream},
	})
	require.NoError(t, err)
	streamer, err := client.Stream(context.Background(), testOutputValidationRequest())
	require.NoError(t, err)
	defer func() {
		require.NoError(t, streamer.Close())
	}()

	_, err = streamer.Recv()

	requireOutputValidationKind(t, err, model.OutputValidationResponseShape)
}

func malformedReasoningResponse(t *testing.T) *responses.Response {
	t.Helper()
	return mustResponse(t, `{
		"model":"gpt-4o",
		"status":"completed",
		"usage":{"input_tokens":2,"output_tokens":1,"total_tokens":3},
		"output":[{
			"id":"reasoning-1",
			"type":"reasoning",
			"status":"completed",
			"summary":[]
		}]
	}`)
}

func testOutputValidationRequest() *model.Request {
	return &model.Request{Messages: []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "Summarize the record."}},
	}}}
}

func requireOutputValidationKind(t *testing.T, err error, want model.OutputValidationKind) {
	t.Helper()
	var validationErr *model.OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, want, validationErr.Kind())
	require.Error(t, errors.Unwrap(validationErr))
}
