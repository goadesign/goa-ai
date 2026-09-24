// Native search tests exercise the real SDK's JSON and SSE encoding. They prove
// local protocol handling, not acceptance by a live Anthropic or Bedrock model.
package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

const claudeSearchResponseJSON = `{
	"id":"msg_search","type":"message","role":"assistant","model":"claude-opus-5",
	"content":[
		{"type":"thinking","thinking":"Find the weather tool.","signature":"sig-before"},
		{"type":"server_tool_use","id":"srvtoolu_search","name":"tool_search_tool_regex","input":{"pattern":"weather","limit":5}},
		{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_search","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"weather_lookup"}]}},
		{"type":"thinking","thinking":"Use the matching tool.","signature":"sig-after"},
		{"type":"tool_use","id":"toolu_lookup","name":"weather_lookup","input":{"city":"Paris"}}
	],
	"stop_reason":"tool_use","usage":{"input_tokens":20,"output_tokens":12}
}`

func TestClaudeSearchSDKUnaryAndStreamingReplay(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			var requests []map[string]json.RawMessage
			transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
				body, err := io.ReadAll(request.Body)
				if err != nil {
					return nil, err
				}
				var fields map[string]json.RawMessage
				if err := json.Unmarshal(body, &fields); err != nil {
					return nil, err
				}
				requests = append(requests, fields)
				response := claudeSearchResponseJSON
				contentType := "application/json"
				if strings.HasSuffix(request.URL.Path, "/count_tokens") {
					response = `{"input_tokens":42}`
				} else if string(fields["stream"]) == "true" {
					response = claudeSearchSSE(t, claudeSearchResponseJSON)
					contentType = "text/event-stream"
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": {contentType}},
					Body:       io.NopCloser(strings.NewReader(response)),
					Request:    request,
				}, nil
			})
			sdkClient := sdk.NewClient(option.WithAPIKey("test"), option.WithHTTPClient(&http.Client{Transport: transport}))
			client, err := New(&sdkClient.Messages, Options{DefaultModel: "claude-opus-5"})
			require.NoError(t, err)
			req := claudeSearchRequest(t)
			var response *model.Response
			if streaming {
				stream, err := client.Stream(t.Context(), req)
				require.NoError(t, err)
				toolCalls, stops := 0, 0
				for {
					chunk, err := stream.Recv()
					if errors.Is(err, io.EOF) {
						break
					}
					require.NoError(t, err)
					switch chunk.(type) {
					case model.ToolCallChunk:
						toolCalls++
					case model.StopChunk:
						stops++
					}
				}
				response = stream.Response()
				require.NoError(t, stream.Close())
				assert.Equal(t, 1, toolCalls)
				assert.Equal(t, 1, stops)
			} else {
				response, err = client.Complete(t.Context(), req)
				require.NoError(t, err)
			}
			require.NotNil(t, response)
			assert.Equal(t, 32, response.Usage.TotalTokens)
			require.Len(t, response.Content, 1)
			assert.Len(t, response.Content[0].Parts, 3)
			assert.Contains(t, response.Content[0].Meta, searchPartsKey)
			initial := requests[0]
			var declarations []map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(initial["tools"], &declarations))
			require.Len(t, declarations, 2)
			assert.Equal(t, "true", string(declarations[0]["defer_loading"]))
			assert.NotContains(t, declarations[0], "cache_control")
			assert.Equal(t, `"tool_search_tool_regex_20251119"`, string(declarations[1]["type"]))
			assert.Contains(t, declarations[1], "cache_control")

			// JSON transport and owned cloning both preserve the ordering record.
			saved, err := json.Marshal(response.Content[0])
			require.NoError(t, err)
			var persisted model.Message
			require.NoError(t, json.Unmarshal(saved, &persisted))
			req.Messages = append(req.Messages, &persisted, &model.Message{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.ToolResultPart{ToolUseID: "toolu_lookup", Content: "sunny"}},
			})
			count, err := client.CountTokens(t.Context(), req)
			require.NoError(t, err)
			assert.Equal(t, 42, count.InputTokens)
			replay := requests[len(requests)-1]
			assert.JSONEq(t, string(initial["tools"]), string(replay["tools"]))
			var replayMessages []struct {
				Content []json.RawMessage `json:"content"`
			}
			require.NoError(t, json.Unmarshal(replay["messages"], &replayMessages))
			require.Len(t, replayMessages[1].Content, 5)
			for i, kind := range []string{"thinking", "server_tool_use", "tool_search_tool_result", "thinking", "tool_use"} {
				assert.Contains(t, string(replayMessages[1].Content[i]), `"type":"`+kind+`"`)
			}
		})
	}
}

func TestClaudeSearchAvailabilityAndReasoningRemoval(t *testing.T) {
	req := claudeSearchRequest(t)
	raw, err := NewProvider(&stubMessagesClient{}, Options{DefaultModel: "claude-opus-5"})
	require.NoError(t, err)
	provider := raw.(*provider)
	enc, err := provider.encodeRequest(t.Context(), req)
	require.NoError(t, err)
	var native sdk.Message
	require.NoError(t, json.Unmarshal([]byte(claudeSearchResponseJSON), &native))
	response, err := translateSearchResponse(&native, enc)
	require.NoError(t, err)
	stored, err := decodeSearchDefinition(response.Content[0].Meta[searchDefsKey].([]string)[0])
	require.NoError(t, err)
	assert.Equal(t, enc.search.definitions["weather_lookup"], stored)
	assistant := response.Content[0]
	require.NoError(t, modelmetadata.WithoutAnthropicReasoning(assistant.Meta))
	assistant.Parts = []model.Part{assistant.Parts[2]}
	req.Messages = append(req.Messages, &assistant, &model.Message{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.ToolResultPart{ToolUseID: "toolu_lookup", Content: "sunny"}},
	})
	tools := req.Tools
	req.Tools = nil
	removed, err := provider.encodeRequest(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, removed.search.changes, 1)
	assert.Contains(t, removed.search.changes[0], `"type":"tool_removal"`)
	assert.Empty(t, removed.provToCanon)
	require.Len(t, removed.tools, 2)
	assert.Equal(t, sdk.MessageParamRoleSystem, removed.messages[len(removed.messages)-1].Role)
	assert.Len(t, removed.messages[1].Content, 3)
	assert.Contains(t, assistant.Meta, searchPartsKey)

	// Keep the injected removal with the response that observed it. Restoring
	// the tool emits an addition after the historical removal.
	next := &sdk.Message{
		StopReason: sdk.StopReasonEndTurn,
		Content:    []sdk.ContentBlockUnion{{Type: "text", Text: "Done."}},
	}
	result, err := translateSearchResponse(next, removed)
	require.NoError(t, err)
	req.Messages = append(req.Messages, &result.Content[0], &model.Message{
		Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Check again"}},
	})
	stillRemoved, err := provider.encodeRequest(t.Context(), req)
	require.NoError(t, err)
	assert.Empty(t, stillRemoved.search.changes)
	req.Tools = tools
	restored, err := provider.encodeRequest(t.Context(), req)
	require.NoError(t, err)
	require.Len(t, restored.search.changes, 1)
	assert.Contains(t, restored.search.changes[0], `"type":"tool_addition"`)
	assert.Equal(t, sdk.MessageParamRoleSystem, restored.messages[len(restored.messages)-1].Role)

	req.Tools[0].Description = "Changed contract."
	_, err = provider.encodeRequest(t.Context(), req)
	require.ErrorIs(t, err, model.ErrToolSearchUnsupported)
	req.Tools = nil
	req.Model = "claude-sonnet-4-5"
	_, err = provider.encodeRequest(t.Context(), req)
	require.ErrorIs(t, err, model.ErrToolSearchUnsupported)
}

func TestClaudeSearchRejectsMalformedNativeHistory(t *testing.T) {
	tests := []struct {
		name        string
		old         string
		replacement string
	}{
		{"missing reference", `"tool_name":"weather_lookup"`, `"tool_name":"other_tool"`},
		{"wrong server tool", `"name":"tool_search_tool_regex"`, `"name":"web_search"`},
		{"unknown native field", `"pattern":"weather"`, `"pattern":"weather","sql":"drop"`},
		{"unpaired result", `"tool_use_id":"srvtoolu_search"`, `"tool_use_id":"other"`},
		{"unknown result", `"type":"tool_search_tool_search_result"`, `"type":"other"`},
		{"empty query", `"pattern":"weather"`, `"pattern":""`},
		{"missing reference array", `"tool_references":[{"type":"tool_reference","tool_name":"weather_lookup"}]`, `"error_message":""`},
		{"null reference array", `"tool_references":[{"type":"tool_reference","tool_name":"weather_lookup"}]`, `"tool_references":null`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			req := claudeSearchRequest(t)
			raw, err := NewProvider(&stubMessagesClient{}, Options{DefaultModel: "claude-opus-5"})
			require.NoError(t, err)
			enc, err := raw.(*provider).encodeRequest(t.Context(), req)
			require.NoError(t, err)
			var native sdk.Message
			require.NoError(t, json.Unmarshal([]byte(strings.ReplaceAll(claudeSearchResponseJSON, test.old, test.replacement)), &native))
			_, err = translateSearchResponse(&native, enc)
			require.Error(t, err)
		})
	}
}

func TestClaudeSearchReplaysEmptyResultsAndErrors(t *testing.T) {
	for _, content := range []string{
		`{"type":"tool_search_tool_search_result","tool_references":[]}`,
		`{"type":"tool_search_tool_result_error","error_code":"too_many_requests","error_message":"Try later."}`,
	} {
		t.Run(content, func(t *testing.T) {
			req := claudeSearchRequest(t)
			raw, err := NewProvider(&stubMessagesClient{}, Options{DefaultModel: "claude-opus-5"})
			require.NoError(t, err)
			provider := raw.(*provider)
			enc, err := provider.encodeRequest(t.Context(), req)
			require.NoError(t, err)
			var native sdk.Message
			source := strings.Replace(claudeSearchResponseJSON,
				`{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"weather_lookup"}]}`, content, 1)
			require.NoError(t, json.Unmarshal([]byte(source), &native))
			response, err := translateSearchResponse(&native, enc)
			require.NoError(t, err)
			req.Messages = append(req.Messages, &response.Content[0], &model.Message{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.ToolResultPart{ToolUseID: "toolu_lookup", Content: "sunny"}},
			})
			replayed, err := provider.encodeRequest(t.Context(), req)
			require.NoError(t, err)
			wire, err := json.Marshal(replayed.messages[1].Content[2])
			require.NoError(t, err)
			assert.JSONEq(t, `{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_search","content":`+content+`}`, string(wire))
		})
	}
}

func TestClaudeSearchRequiresStoredDefinitionsAndConsistentPartReferences(t *testing.T) {
	for _, change := range []string{"missing definitions", "duplicate part", "unknown field"} {
		t.Run(change, func(t *testing.T) {
			req := claudeSearchRequest(t)
			raw, err := NewProvider(&stubMessagesClient{}, Options{DefaultModel: "claude-opus-5"})
			require.NoError(t, err)
			provider := raw.(*provider)
			enc, err := provider.encodeRequest(t.Context(), req)
			require.NoError(t, err)
			var native sdk.Message
			require.NoError(t, json.Unmarshal([]byte(claudeSearchResponseJSON), &native))
			response, err := translateSearchResponse(&native, enc)
			require.NoError(t, err)
			message := &response.Content[0]
			switch change {
			case "missing definitions":
				delete(message.Meta, searchDefsKey)
			case "duplicate part":
				records := message.Meta[searchPartsKey].([]string)
				records = append(records, records[len(records)-1])
				message.Meta[searchPartsKey] = records
			case "unknown field":
				records := message.Meta[searchPartsKey].([]string)
				records[0] = `{"type":"thinking_reference","index":0,"text":"injected"}`
			}
			req.Messages = append(req.Messages, message)
			_, err = provider.encodeRequest(t.Context(), req)
			require.Error(t, err)
		})
	}
}

func claudeSearchRequest(t *testing.T) *model.Request {
	t.Helper()
	return &model.Request{
		MaxTokens: 128,
		Messages:  []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Weather in Paris"}}}},
		Tools: []*model.ToolDefinition{{
			Name: "weather.lookup", Description: "Look up the weather.",
			Deferred: true, Search: tools.NewSearchDocument("weather lookup temperature"),
			Input: mustAnthropicToolInput(t, rawjson.Message(`{"type":"object","properties":{"city":{"type":"string","description":"Use <city> & country; population > 0."}},"required":["city"],"additionalProperties":false}`)),
		}},
		Cache: &model.CacheOptions{AfterTools: true},
	}
}

// claudeSearchSSE emits native search arguments as input_json_delta fragments,
// matching the real streaming protocol rather than putting them in start events.
func claudeSearchSSE(t *testing.T, response string) string {
	t.Helper()
	var message map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(response), &message))
	var content []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(message["content"], &content))
	message["content"] = json.RawMessage(`[]`)
	message["stop_reason"] = json.RawMessage(`null`)
	start, err := json.Marshal(message)
	require.NoError(t, err)
	var stream strings.Builder
	write := func(kind string, value any) {
		body, err := json.Marshal(value)
		require.NoError(t, err)
		_, err = fmt.Fprintf(&stream, "event: %s\ndata: %s\n\n", kind, body)
		require.NoError(t, err)
	}
	write("message_start", map[string]any{"type": "message_start", "message": json.RawMessage(start)})
	for i, block := range content {
		input := block["input"]
		if len(input) > 0 {
			block["input"] = json.RawMessage(`{}`)
		}
		write("content_block_start", map[string]any{"type": "content_block_start", "index": i, "content_block": block})
		if len(input) > 0 {
			write("content_block_delta", map[string]any{"type": "content_block_delta", "index": i,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)}})
		}
		write("content_block_stop", map[string]any{"type": "content_block_stop", "index": i})
	}
	write("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": "tool_use"},
		"usage": map[string]any{"output_tokens": 12}})
	write("message_stop", map[string]any{"type": "message_stop"})
	return stream.String()
}
