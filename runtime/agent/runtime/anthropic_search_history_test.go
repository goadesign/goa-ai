// Claude discovery follows the same runtime history policies as ordinary tool
// exchanges. Retained search blocks stay intact while prior thinking is removed.
package runtime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	modelanthropic "goa.design/goa-ai/features/model/anthropic"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestClaudeSearchThroughNewTurnAndHistoryTrimming(t *testing.T) {
	var calls atomic.Int32
	bodies := make(chan []byte, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		bodies <- body
		output, reason := `{"type":"text","text":"Done."}`, "end_turn"
		if calls.Add(1) == 1 {
			reason = "tool_use"
			output = `{"type":"thinking","thinking":"private-search-reasoning","signature":"sig_before"},
				{"type":"server_tool_use","id":"srvtoolu_search","name":"tool_search_tool_regex","input":{"pattern":"weather"}},
				{"type":"tool_search_tool_result","tool_use_id":"srvtoolu_search","content":{"type":"tool_search_tool_search_result","tool_references":[{"type":"tool_reference","tool_name":"weather_lookup"}]}},
				{"type":"thinking","thinking":"private-tool-reasoning","signature":"sig_after"},
				{"type":"tool_use","id":"toolu_lookup","name":"weather_lookup","input":{"city":"Paris"}}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{"id":"msg","type":"message","role":"assistant","model":"claude-opus-5","stop_reason":"`+reason+`","content":[`+output+`],"usage":{"input_tokens":10,"output_tokens":2}}`)
		assert.NoError(t, err)
	}))
	defer server.Close()
	sdkClient := sdk.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	client, err := modelanthropic.New(&sdkClient.Messages, modelanthropic.Options{DefaultModel: "claude-opus-5"})
	require.NoError(t, err)
	input, err := model.AdvertisedToolInputFromSchema(rawjson.Message(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`))
	require.NoError(t, err)
	request := &model.Request{
		MaxTokens: 128, Messages: []*model.Message{userMsg("Forecast for Paris")},
		Tools: []*model.ToolDefinition{{
			Name: "weather.lookup", Description: "Weather forecasts.", Deferred: true,
			Search: tools.NewSearchDocument("weather lookup forecasts"), Input: input,
		}},
	}
	response, err := client.Complete(t.Context(), request)
	require.NoError(t, err)
	<-bodies
	savedJSON, err := json.Marshal(response.Content)
	require.NoError(t, err)
	var saved []*model.Message
	require.NoError(t, json.Unmarshal(savedJSON, &saved))
	history := append([]*model.Message(nil), request.Messages...)
	history = append(history, saved...)
	history = append(history,
		&model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "toolu_lookup", Content: "sunny"},
		}},
		&model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Sunny in Paris."}}},
		userMsg("Now London"),
	)
	require.NoError(t, transcript.ValidatePlannerTranscript(history))
	start, err := buildOneShotRunStart("svc.agent", history, []RunOption{WithoutPriorReasoning()})
	require.NoError(t, err)
	request.Messages = start.messages
	for _, message := range request.Messages {
		require.NotEmpty(t, message.Parts)
	}
	_, err = client.Complete(t.Context(), request)
	require.NoError(t, err)
	body := <-bodies
	assert.NotContains(t, string(body), "private-search-reasoning")
	assert.NotContains(t, string(body), "private-tool-reasoning")
	assert.Contains(t, string(body), `"type":"tool_search_tool_result"`)
	assert.Contains(t, string(body), `"name":"weather_lookup"`)
	for _, keep := range []int{2, 3} {
		result, err := KeepRecentTurns(keep)(t.Context(), request, nil, nil)
		require.NoError(t, err)
		require.NoError(t, transcript.ValidatePlannerTranscript(result.Messages))
		trimmed := *request
		trimmed.Messages = result.Messages
		_, err = client.Complete(t.Context(), &trimmed)
		require.NoError(t, err)
		body := <-bodies
		if keep == 2 {
			assert.NotContains(t, string(body), `"type":"tool_search_tool_result"`)
		} else {
			assert.Contains(t, string(body), `"type":"tool_search_tool_result"`)
		}
	}
}
