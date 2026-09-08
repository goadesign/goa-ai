// These tests prove that disabled reasoning is explicit provider configuration,
// while absent thinking and unconfigured clients preserve their old requests.
package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestResponsesSDKDisabledThinkingEffort(t *testing.T) {
	for _, bedrock := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, configured := range []string{"", "none"} {
				for _, thinking := range []string{"nil", "on", "off"} {
					t.Run(fmt.Sprintf("bedrock_%t/stream_%t/config_%s/%s", bedrock, streaming, configured, thinking), func(t *testing.T) {
						var body []byte
						client := newReplayTestClient(t, bedrock, Options{ThinkingEffort: "medium", DisabledThinkingEffort: configured}, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
							var err error
							body, err = io.ReadAll(req.Body)
							if err != nil {
								return nil, err
							}
							return replayTestResponse(bedrockReasoning, streaming), nil
						}))
						request := replayTestRequest(thinking)
						request.MaxTokens = 100
						if thinking != "on" {
							request.Temperature = 0.2
						}
						if !bedrock && configured == "" {
							request.Model = "gpt-4o"
						}
						_, err := completeReplayTest(t, client, request, streaming)
						require.NoError(t, err)
						var wire struct {
							Model           string          `json:"model"`
							Reasoning       json.RawMessage `json:"reasoning"`
							Temperature     *float64        `json:"temperature"`
							MaxOutputTokens int             `json:"max_output_tokens"`
							Include         []string        `json:"include"`
						}
						require.NoError(t, json.Unmarshal(body, &wire))
						switch {
						case thinking == "on":
							assert.JSONEq(t, `{"effort":"medium","summary":"auto"}`, string(wire.Reasoning))
						case thinking == "off" && configured == "none":
							assert.JSONEq(t, `{"effort":"none"}`, string(wire.Reasoning))
						default:
							assert.Empty(t, wire.Reasoning)
						}
						assert.Equal(t, 100, wire.MaxOutputTokens)
						assert.Equal(t, []string{"reasoning.encrypted_content"}, wire.Include)
						if thinking == "on" {
							assert.Nil(t, wire.Temperature)
						} else {
							require.NotNil(t, wire.Temperature)
							assert.Equal(t, float64(request.Temperature), *wire.Temperature) //nolint:testifylint // The exact float32-to-float64 conversion must be preserved.
						}
						if request.Model != "" {
							assert.Equal(t, request.Model, wire.Model)
						}
					})
				}
			}
		}
	}
}

func TestNewRejectsUnknownDisabledThinkingEffort(t *testing.T) {
	for _, effort := range []string{"low", "medium", "high", "NONE", " none"} {
		t.Run(effort, func(t *testing.T) {
			_, err := New(Options{DefaultModel: "gpt-4o", DisabledThinkingEffort: effort, transport: &mockTransport{}})
			require.EqualError(t, err, fmt.Sprintf("openai: unsupported disabled thinking effort %q", effort))
		})
	}
}

func TestResponsesSDKDisabledThinkingPreservesProviderError(t *testing.T) {
	for _, bedrock := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("bedrock_%t/stream_%t", bedrock, streaming), func(t *testing.T) {
				calls := 0
				client := newReplayTestClient(t, bedrock, Options{DisabledThinkingEffort: "none"}, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					body, err := io.ReadAll(req.Body)
					if err != nil {
						return nil, err
					}
					assert.Contains(t, string(body), `"model":"unsupported-model"`)
					assert.Contains(t, string(body), `"effort":"none"`)
					response := bedrockJSONResponse(`{"error":{"code":"unsupported_value","message":"Model unsupported-model does not support reasoning effort none."}}`)
					response.StatusCode = http.StatusBadRequest
					return response, nil
				}))
				request := replayTestRequest("off")
				request.Model = "unsupported-model"
				_, err := completeReplayTest(t, client, request, streaming)
				require.Error(t, err)
				providerError, ok := model.AsProviderError(err)
				require.True(t, ok)
				assert.Contains(t, providerError.Error(), "Model unsupported-model does not support reasoning effort none.")
				assert.Equal(t, 1, calls)
			})
		}
	}
}
