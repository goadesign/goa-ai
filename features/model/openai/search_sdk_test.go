// These tests exercise native discovery through the official SDK and both
// OpenAI transports. HTTP responses are synthetic; they validate local wire
// contracts and history behavior, not live model availability.
package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestNativeSearchSDKPreservesReasoningOrderAndNewTurnReplay(t *testing.T) {
	for _, bedrock := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("bedrock_%t/stream_%t", bedrock, streaming), func(t *testing.T) {
				var bodies [][]byte
				client := newReplayTestClient(t, bedrock, Options{}, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					bodies = append(bodies, body)
					output := searchTextJSON
					switch len(bodies) {
					case 1:
						output = bedrockReasoning + "," + searchCallJSON
					case 2:
						output = strings.ReplaceAll(bedrockReasoning, `"rs_1"`, `"rs_2"`) + "," + searchToolJSON
					}
					response := searchResponse(t, output).RawJSON()
					if !streaming {
						return bedrockJSONResponse(response), nil
					}
					sse := "data: {\"type\":\"response.completed\",\"response\":" + response + "}\n\ndata: [DONE]\n\n"
					return &http.Response{
						StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}},
						Body: io.NopCloser(strings.NewReader(sse)),
					}, nil
				}))
				request := searchRequest()
				response, err := completeReplayTest(t, client, request, streaming)
				require.NoError(t, err)
				require.Len(t, response.Content, 1)
				require.Len(t, response.Content[0].Parts, 3)
				assert.IsType(t, model.ThinkingPart{}, response.Content[0].Parts[0])
				assert.IsType(t, model.ThinkingPart{}, response.Content[0].Parts[1])
				assert.IsType(t, model.ToolUsePart{}, response.Content[0].Parts[2])
				encoded, err := json.Marshal(response.Content)
				require.NoError(t, err)
				var restored []*model.Message
				require.NoError(t, json.Unmarshal(encoded, &restored))
				request.Messages = append(request.Messages, restored...)
				request.Messages = append(request.Messages, &model.Message{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.ToolResultPart{ToolUseID: "business-1", Content: "sunny"}},
				})
				_, err = completeReplayTest(t, client, request, streaming)
				require.NoError(t, err)
				var replay struct {
					Input []struct {
						Type string `json:"type"`
						Role string `json:"role"`
					} `json:"input"`
				}
				require.NoError(t, json.Unmarshal(bodies[2], &replay))
				types := make([]string, 0, len(replay.Input))
				for _, item := range replay.Input {
					types = append(types, item.Type)
				}
				require.Len(t, types, 7)
				assert.Equal(t, "user", replay.Input[0].Role)
				assert.Equal(t, []string{"reasoning", "tool_search_call", "tool_search_output", "reasoning", "function_call", "function_call_output"}, types[1:])

				// Apply the same owned metadata operation used by completed turns.
				// Semantic content survives, so every message remains valid.
				for _, message := range restored {
					require.NoError(t, modelmetadata.WithoutOpenAIReasoning(message.Meta))
					var parts []model.Part
					for _, part := range message.Parts {
						if _, thinking := part.(model.ThinkingPart); !thinking {
							parts = append(parts, part)
						}
					}
					message.Parts = parts
				}
				_, err = completeReplayTest(t, client, request, streaming)
				require.NoError(t, err)
				assert.NotContains(t, string(bodies[3]), `"encrypted_content":`)
				assert.NotContains(t, string(bodies[3]), "reasoning_reference")
				assert.Contains(t, string(bodies[3]), `"type":"tool_search_call"`)
				assert.Contains(t, string(bodies[3]), `"type":"tool_search_output"`)
			})
		}
	}
}
