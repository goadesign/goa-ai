// These tests keep the caller's thinking choice intact when generated typed
// completions add their output contract, for both unary and streaming calls.
package completion

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

// TestCompletionPreservesThinkingChoice verifies that requesting typed JSON
// leaves provider defaults unspecified and preserves explicit thinking options.
func TestCompletionPreservesThinkingChoice(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "unary"
		if streaming {
			name = "streaming"
		}
		t.Run(name, func(t *testing.T) {
			for _, tc := range []struct {
				name     string
				thinking *model.ThinkingOptions
			}{
				{name: "provider defaults"},
				{name: "disabled", thinking: &model.ThinkingOptions{}},
				{
					name: "enabled",
					thinking: &model.ThinkingOptions{
						Enable:       true,
						BudgetTokens: 4096,
						Interleaved:  true,
					},
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body := `{"assistant_text":"created a draft"}`
					response := completionResponse(body)
					providerStream := &scriptedStreamer{
						results: []recvResult{
							{chunk: model.CompletionChunk{Completion: model.Completion{
								Name:    "draft_from_transcript",
								Payload: []byte(body),
							}}},
							{chunk: model.StopChunk{Reason: "stop"}},
						},
						response: response,
					}
					provider := &recordingCompletionClient{
						response: response,
						streamer: providerStream,
					}
					client := mustCompletionClient(t, provider)
					request := &model.Request{Thinking: tc.thinking, MaxTokens: 8192}
					if streaming {
						stream, err := Stream(t.Context(), client, request, testCompletionSpec())
						require.NoError(t, err)
						chunk, err := stream.Recv()
						require.NoError(t, err)
						assert.IsType(t, model.CompletionChunk{}, chunk)
						value, ok := stream.Value()
						require.True(t, ok)
						assert.Equal(t, "created a draft", value.AssistantText)
						require.NoError(t, stream.Close())
						assert.True(t, providerStream.closed)
					} else {
						result, err := Complete(t.Context(), client, request, testCompletionSpec())
						require.NoError(t, err)
						assert.Equal(t, "created a draft", result.Value.AssistantText)
						assert.Len(t, provider.requests, 1)
					}
					require.NotNil(t, provider.request)
					assert.Equal(t, tc.thinking, provider.request.Thinking)
					assert.Equal(t, tc.thinking, request.Thinking)
					assert.Equal(t, 8192, provider.request.MaxTokens)
					assert.Nil(t, request.StructuredOutput)
					assert.False(t, request.Stream)
				})
			}
		})
	}
}
