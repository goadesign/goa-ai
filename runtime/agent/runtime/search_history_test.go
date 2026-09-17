// Native discovery must survive real new-turn preparation and complete-exchange
// trimming. The SDK talks to a synthetic HTTP server; the runtime's public
// reasoning policy and history policy remain in the exercised path.
package runtime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	openaisdk "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/option"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	modelopenai "goa.design/goa-ai/features/model/openai"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestNativeSearchThroughNewTurnPreparationAndHistoryTrimming(t *testing.T) {
	var calls atomic.Int32
	bodies := make(chan []byte, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if !assert.NoError(t, err) {
			return
		}
		bodies <- body
		output := `{"type":"message","id":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Done.","annotations":[]}]}`
		switch calls.Add(1) {
		case 1:
			output = `{"type":"reasoning","id":"rs_1","status":"completed","summary":[],"encrypted_content":"private-search-reasoning"},{"type":"tool_search_call","id":"search","call_id":"discovery","execution":"client","status":"completed","arguments":{"query":"weather"}}`
		case 2:
			output = `{"type":"function_call","id":"function","call_id":"business","name":"weather_lookup","namespace":"weather_lookup","status":"completed","arguments":"{\"city\":\"Paris\"}"}`
		}
		w.Header().Set("Content-Type", "application/json")
		_, err = io.WriteString(w, `{"status":"completed","model":"gpt-5.4","output":[`+output+`],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`)
		assert.NoError(t, err)
	}))
	defer server.Close()
	sdk := openaisdk.NewClient(option.WithAPIKey("test"), option.WithBaseURL(server.URL), option.WithMaxRetries(0))
	client, err := modelopenai.New(modelopenai.Options{Client: &sdk.Responses, DefaultModel: "gpt-5.4"})
	require.NoError(t, err)
	input, err := model.AdvertisedToolInputFromSchema(rawjson.Message(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`))
	require.NoError(t, err)
	request := &model.Request{
		MaxTokens: 30,
		Messages:  []*model.Message{userMsg("Forecast for Paris")},
		Tools: []*model.ToolDefinition{{
			Name: "weather.lookup", Description: "Weather forecasts", Deferred: true,
			Search: tools.NewSearchDocument("weather.lookup Weather forecasts"), Input: input,
		}},
	}
	response, err := client.Complete(t.Context(), request)
	require.NoError(t, err)
	<-bodies
	<-bodies
	require.Len(t, response.Content, 1)
	encoded, err := json.Marshal(response.Content)
	require.NoError(t, err)
	var saved []*model.Message
	require.NoError(t, json.Unmarshal(encoded, &saved))
	history := append([]*model.Message(nil), request.Messages...)
	history = append(history, saved...)
	history = append(history,
		&model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "business", Content: "sunny"},
		}},
		&model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Sunny in Paris."}}},
		userMsg("Now forecast for London"),
	)
	require.NoError(t, transcript.ValidatePlannerTranscript(history))
	start, err := buildOneShotRunStart("svc.agent", history, []RunOption{WithoutPriorReasoning()})
	require.NoError(t, err)
	for _, message := range start.input.Messages {
		require.NotEmpty(t, message.Parts)
	}
	request.Messages = start.input.Messages
	_, err = client.Complete(t.Context(), request)
	require.NoError(t, err)
	body := <-bodies
	assert.NotContains(t, string(body), "private-search-reasoning")
	assert.NotContains(t, string(body), "reasoning_reference")
	assert.Contains(t, string(body), `"type":"tool_search_output"`)
	assert.Contains(t, string(body), `"type":"function_call"`)

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
			assert.NotContains(t, string(body), `"type":"tool_search_output"`)
			assert.NotContains(t, string(body), `"type":"function_call"`)
		} else {
			assert.Contains(t, string(body), `"type":"tool_search_output"`)
			assert.Contains(t, string(body), `"type":"function_call"`)
		}
	}
}
