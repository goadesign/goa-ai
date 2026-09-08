// These tests use the official SDK with in-memory HTTP responses to prove that
// stateless requests ask for replay metadata independently of reasoning effort.
// No provider is contacted and no claim is made about model-generated content.
package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/credentials"
	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestResponsesSDKStatelessReplay(t *testing.T) {
	for _, bedrock := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			for _, thinking := range []string{"nil", "on", "off"} {
				t.Run(fmt.Sprintf("bedrock_%t/stream_%t/%s", bedrock, streaming, thinking), func(t *testing.T) {
					var bodies [][]byte
					client := newReplayTestClient(t, bedrock, Options{ThinkingEffort: "medium"}, bedrockRoundTripFunc(func(req *http.Request) (*http.Response, error) {
						body, err := io.ReadAll(req.Body)
						if err != nil {
							return nil, err
						}
						bodies = append(bodies, body)
						return replayTestResponse(bedrockReasoning, streaming), nil
					}))
					request := replayTestRequest(thinking)
					response, err := completeReplayTest(t, client, request, streaming)
					require.NoError(t, err)
					require.NotEmpty(t, response.Content)
					metadata := response.Content[0].Meta[openAIReasoningItemsMetaKey].([]string)
					require.Equal(t, []string{bedrockReasoning}, metadata)
					part := response.Content[0].Parts[0].(model.ThinkingPart)
					assert.Equal(t, []byte(`opaque+/=\unchanged`), part.Redacted)
					for i := range response.Content {
						request.Messages = append(request.Messages, &response.Content[i])
					}
					request.Messages = append(request.Messages, &model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Continue."}}})
					_, err = completeReplayTest(t, client, request, streaming)
					require.NoError(t, err)
					require.Len(t, bodies, 2)
					for i, body := range bodies {
						var wire struct {
							Store     *bool             `json:"store"`
							Include   []string          `json:"include"`
							Reasoning json.RawMessage   `json:"reasoning"`
							Input     []json.RawMessage `json:"input"`
						}
						require.NoError(t, json.Unmarshal(body, &wire))
						require.NotNil(t, wire.Store)
						assert.False(t, *wire.Store)
						assert.Equal(t, []string{"reasoning.encrypted_content"}, wire.Include)
						if thinking == "on" {
							assert.JSONEq(t, `{"effort":"medium","summary":"auto"}`, string(wire.Reasoning))
						} else {
							assert.Empty(t, wire.Reasoning)
						}
						if i == 1 {
							require.Len(t, wire.Input, 4)
							assert.JSONEq(t, bedrockReasoning, string(wire.Input[1]))
							var reasoning struct {
								EncryptedContent string `json:"encrypted_content"`
							}
							require.NoError(t, json.Unmarshal(wire.Input[1], &reasoning))
							assert.Equal(t, []byte(`opaque+/=\unchanged`), []byte(reasoning.EncryptedContent))
						}
					}
				})
			}
		}
	}
}

func TestResponsesSDKRejectsEmptyReasoning(t *testing.T) {
	for _, bedrock := range []bool{false, true} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("bedrock_%t/stream_%t", bedrock, streaming), func(t *testing.T) {
				client := newReplayTestClient(t, bedrock, Options{}, bedrockRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return replayTestResponse(`{"type":"reasoning","id":"rs_empty","summary":[]}`, streaming), nil
				}))
				_, err := completeReplayTest(t, client, replayTestRequest("nil"), streaming)
				var invalid *model.OutputValidationError
				require.ErrorAs(t, err, &invalid)
				assert.Equal(t, model.OutputValidationResponseShape, invalid.Kind())
				assert.ErrorContains(t, invalid.Unwrap(), "reasoning item has no summary or encrypted content")
			})
		}
	}
}

// newReplayTestClient runs each public constructor against the same synthetic
// transport, leaving the official SDK responsible for request serialization.
func newReplayTestClient(t *testing.T, bedrock bool, opts Options, transport http.RoundTripper) model.Client {
	t.Helper()
	opts.DefaultModel = bedrockTestModel
	requestOptions := make([]option.RequestOption, 0, 4)
	requestOptions = append(requestOptions, option.WithMaxRetries(0), option.WithHTTPClient(&http.Client{Transport: transport}))
	if bedrock {
		t.Setenv("AWS_BEDROCK_BASE_URL", "")
		client, err := NewBedrock(t.Context(), "us-west-2", credentials.NewStaticCredentialsProvider("test-access", "test-secret", "test-session"), opts, requestOptions...)
		require.NoError(t, err)
		return client
	}
	requestOptions = append(requestOptions, option.WithAPIKey("test-key"), option.WithBaseURL("https://api.openai.invalid/v1/"))
	sdk := openaisdk.NewClient(requestOptions...)
	opts.Client = &sdk.Responses
	client, err := New(opts)
	require.NoError(t, err)
	return client
}

func replayTestRequest(thinking string) *model.Request {
	req := &model.Request{Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Answer briefly."}}}}}
	switch thinking {
	case "on":
		req.Thinking = &model.ThinkingOptions{Enable: true}
	case "off":
		req.Thinking = &model.ThinkingOptions{Enable: false}
	}
	return req
}

// completeReplayTest consumes a streaming response through the real validated
// client, so final reasoning validation and metadata retention are exercised.
func completeReplayTest(t *testing.T, client model.Client, request *model.Request, streaming bool) (*model.Response, error) {
	t.Helper()
	if !streaming {
		return client.Complete(t.Context(), request)
	}
	stream, err := client.Stream(t.Context(), request)
	if err != nil {
		return nil, err
	}
	defer func() { assert.NoError(t, stream.Close()) }()
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			return stream.Response(), nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func replayTestResponse(reasoning string, streaming bool) *http.Response {
	body := `{"id":"resp_1","model":"` + bedrockTestModel + `","status":"completed","output":[` + reasoning + `,{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Answer.","annotations":[]}]}],"usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}`
	if !streaming {
		return bedrockJSONResponse(body)
	}
	sse := "data: {\"type\":\"response.completed\",\"sequence_number\":1,\"response\":" + body + "}\n\ndata: [DONE]\n\n"
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(sse))}
}
