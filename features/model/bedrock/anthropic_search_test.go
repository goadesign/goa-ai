// These tests drive signed InvokeModel requests and AWS EventStream decoding
// against local responses. No AWS credentials or live endpoint are used.
package bedrock

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream"
	"github.com/aws/aws-sdk-go-v2/aws/protocol/eventstream/eventstreamapi"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	searchHTTPTransport func(*http.Request) (*http.Response, error)
)

const bedrockSearchResponse = `{
	"id":"msg_search","type":"message","role":"assistant","model":"anthropic.claude-opus-5",
	"content":[
		{"type":"server_tool_use","id":"srvtoolu_search","name":"tool_search_tool_regex","input":{"pattern":"weather"}},
		{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_search","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"weather_lookup"}]}},
		{"type":"tool_use","id":"toolu_lookup","name":"weather_lookup","input":{"city":"Paris"}}
	],
	"stop_reason":"tool_use","usage":{"input_tokens":20,"output_tokens":12}
}`

func TestClaudeSearchBedrockSDK(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var body map[string]json.RawMessage
			var path, authorization string
			transport := searchHTTPTransport(func(request *http.Request) (*http.Response, error) {
				payload, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				if err := json.Unmarshal(payload, &body); err != nil {
					return nil, err
				}
				path, authorization = request.URL.Path, request.Header.Get("Authorization")
				content := []byte(bedrockSearchResponse)
				contentType := "application/json"
				if streaming {
					content = bedrockSearchEvents(t)
					contentType = "application/vnd.amazon.eventstream"
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {contentType}},
					Body:       io.NopCloser(bytes.NewReader(content)), Request: request,
				}, nil
			})
			cfg := aws.Config{Region: "us-west-2", Credentials: testCredentialsProvider{}}
			counter := &recordingAnthropicCounter{}
			client, err := NewAnthropic(cfg, counter,
				AnthropicOptions{DefaultModel: "us.anthropic.claude-opus-5", MaxTokens: 128},
				option.WithHTTPClient(&http.Client{Transport: transport}),
			)
			require.NoError(t, err)
			input, err := model.AdvertisedToolInputFromSchema(rawjson.Message(`{
				"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false
			}`))
			require.NoError(t, err)
			request := &model.Request{
				Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Weather in Paris"}}}},
				Tools: []*model.ToolDefinition{{
					Name: "weather.lookup", Description: "Get the weather.", Deferred: true,
					Search: tools.NewSearchDocument("weather lookup"), Input: input,
				}},
				Cache: &model.CacheOptions{AfterTools: true},
			}
			var response *model.Response
			if streaming {
				stream, err := client.Stream(t.Context(), request)
				require.NoError(t, err)
				calls, stops := 0, 0
				for {
					chunk, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					require.NoError(t, err)
					switch chunk.(type) {
					case model.ToolCallChunk:
						calls++
					case model.StopChunk:
						stops++
					}
				}
				response = stream.Response()
				require.NoError(t, stream.Close())
				assert.Equal(t, 1, calls)
				assert.Equal(t, 1, stops)
				assert.True(t, strings.HasSuffix(path, "/invoke-with-response-stream"))
			} else {
				response, err = client.Complete(t.Context(), request)
				require.NoError(t, err)
				assert.True(t, strings.HasSuffix(path, "/invoke"))
			}
			assert.Contains(t, authorization, "AWS4-HMAC-SHA256")
			var betas []string
			require.NoError(t, json.Unmarshal(body["anthropic_beta"], &betas))
			assert.Contains(t, betas, bedrockSearchBeta)
			var declarations []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body["tools"], &declarations))
			require.Len(t, declarations, 2)
			assert.Equal(t, "true", string(declarations[0]["defer_loading"]))
			assert.NotContains(t, declarations[0], "cache_control")
			assert.Equal(t, `"tool_search_tool_regex"`, string(declarations[1]["type"]))
			assert.Contains(t, declarations[1], "cache_control")
			require.NotNil(t, response)
			require.Len(t, response.Content, 1)
			assert.Contains(t, response.Content[0].Meta, "anthropic_tool_search_parts_v1")
			assert.Equal(t, 32, response.Usage.TotalTokens)

			request.Messages = append(request.Messages, &response.Content[0], &model.Message{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.ToolResultPart{ToolUseID: "toolu_lookup", Content: "sunny"}},
			})
			count, err := client.CountTokens(t.Context(), request)
			require.NoError(t, err)
			assert.Equal(t, 42, count.InputTokens)
			assert.Equal(t, "anthropic.claude-opus-5", counter.request.Model)
			assert.True(t, counter.request.Tools[0].Deferred)
			assert.Equal(t, response.Content[0].Meta, counter.request.Messages[1].Meta)
		})
	}
}

func TestClaudeSearchBedrockRejectsUnsupportedAvailabilityChanges(t *testing.T) {
	client := &anthropicMessages{}
	body := sdk.MessageNewParams{
		Model:    "us.anthropic.claude-sonnet-4-5",
		Messages: []sdk.MessageParam{{Role: sdk.MessageParamRoleSystem}},
	}
	_, err := client.New(t.Context(), body)
	require.ErrorIs(t, err, model.ErrToolSearchUnsupported)
	stream := client.NewStreaming(t.Context(), body)
	require.ErrorIs(t, stream.Err(), model.ErrToolSearchUnsupported)
	require.NoError(t, stream.Close())
}

func (f searchHTTPTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func bedrockSearchEvents(t *testing.T) []byte {
	t.Helper()
	events := []string{
		`{"type":"message_start","message":{"id":"msg_search","type":"message","role":"assistant","content":[],"model":"anthropic.claude-opus-5","stop_reason":null,"usage":{"input_tokens":20,"output_tokens":0}}}`,
		`{"type":"content_block_start","index":0,"content_block":{"type":"server_tool_use","id":"srvtoolu_search","name":"tool_search_tool_regex","input":{}}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"pattern\":\"weather\"}"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_search","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"weather_lookup"}]}}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_lookup","name":"weather_lookup","input":{}}}`,
		`{"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Paris\"}"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":12}}`,
		`{"type":"message_stop"}`,
	}
	var wire bytes.Buffer
	for _, event := range events {
		payload, err := json.Marshal(map[string]string{"bytes": base64.StdEncoding.EncodeToString([]byte(event))})
		require.NoError(t, err)
		message := eventstream.Message{Payload: payload}
		message.Headers.Set(eventstreamapi.MessageTypeHeader, eventstream.StringValue(eventstreamapi.EventMessageType))
		message.Headers.Set(eventstreamapi.EventTypeHeader, eventstream.StringValue("chunk"))
		require.NoError(t, eventstream.NewEncoder().Encode(&wire, message))
	}
	return wire.Bytes()
}
