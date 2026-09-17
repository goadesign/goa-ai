// Native discovery tests exercise full validated clients and replay their
// returned transcripts. Transport fixtures replace HTTP, not tool translation
// or model request validation.
package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	searchTransport struct {
		responses []*responses.Response
		requests  []responses.ResponseNewParams
		err       error
	}
)

const (
	searchCallJSON = `{"type":"tool_search_call","id":"search-1","call_id":"search-call-1","execution":"client","status":"completed","arguments":{"query":"weather"}}`
	searchToolJSON = `{"type":"function_call","id":"function-1","call_id":"business-1","name":"weather_lookup","status":"completed","arguments":"{\"city\":\"Paris\"}"}`
	searchTextJSON = `{"type":"message","id":"message-1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"Finished.","annotations":[]}]}`
)

func TestNativeSearchLoadsOnlyMatchingPermittedDefinitions(t *testing.T) {
	transport := &searchTransport{responses: []*responses.Response{
		searchResponse(t, searchCallJSON),
		searchResponse(t, searchToolJSON),
	}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	request := searchRequest()
	response, err := client.Complete(t.Context(), request)
	require.NoError(t, err)
	require.Len(t, transport.requests, 2)
	assert.Equal(t, 24, response.Usage.TotalTokens)
	assert.Equal(t, 4, response.Usage.OutputTokens)
	require.Len(t, response.ToolCalls(), 1)
	assert.Equal(t, "weather.lookup", response.ToolCalls()[0].Name.String())
	first, second := transport.requests[0], transport.requests[1]
	assert.Equal(t, int64(30), first.MaxOutputTokens.Value)
	assert.Equal(t, int64(28), second.MaxOutputTokens.Value)
	require.Len(t, first.Tools, 1)
	require.NotNil(t, first.Tools[0].OfToolSearch)
	wire, err := json.Marshal(first)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), "Weather forecasts")
	assert.NotContains(t, string(wire), "Email messages")
	require.Len(t, second.Input.OfInputItemList, 3)
	found := second.Input.OfInputItemList[2].OfToolSearchOutput
	require.NotNil(t, found)
	require.Len(t, found.Tools, 1)
	assert.Equal(t, "weather_lookup", found.Tools[0].OfFunction.Name)
	assert.True(t, found.Tools[0].OfFunction.DeferLoading.Value)

	// JSON persistence and a new provider invocation retain native references.
	clone, err := model.CloneResponse(response)
	require.NoError(t, err)
	for i := range clone.Content {
		request.Messages = append(request.Messages, &clone.Content[i])
	}
	request.Messages = append(request.Messages, &model.Message{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.ToolResultPart{ToolUseID: "business-1", Content: "sunny"}},
	})
	provider, err := newProvider(Options{DefaultModel: "gpt-5.4", transport: transport}, false)
	require.NoError(t, err)
	prepared, err := provider.prepareRequest(request)
	require.NoError(t, err)
	require.Len(t, prepared.request.Input.OfInputItemList, 5)
	assert.NotNil(t, prepared.request.Input.OfInputItemList[1].OfToolSearchCall)
	assert.NotNil(t, prepared.request.Input.OfInputItemList[2].OfToolSearchOutput)
	assert.NotNil(t, prepared.request.Input.OfInputItemList[3].OfFunctionCall)
}

func TestNativeSearchMixedBusinessCallReturnsWithoutContinuation(t *testing.T) {
	transport := &searchTransport{responses: []*responses.Response{
		searchResponse(t, searchCallJSON+","+searchToolJSON),
	}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	response, err := client.Complete(t.Context(), searchRequest())
	require.NoError(t, err)
	require.Len(t, transport.requests, 1)
	require.Len(t, response.ToolCalls(), 1)
	items, err := encodeMessages([]*model.Message{&response.Content[0]}, map[string]string{"weather.lookup": "weather_lookup"})
	require.NoError(t, err)
	require.Len(t, items, 3)
	assert.NotNil(t, items[0].OfToolSearchCall)
	assert.NotNil(t, items[1].OfFunctionCall)
	assert.NotNil(t, items[2].OfToolSearchOutput)
}

func TestNativeSearchRetainsResponseAttributionWithoutSendingItAsInput(t *testing.T) {
	output := strings.Replace(searchCallJSON, `"id":"search-1"`, `"id":"search-1","created_by":"provider-actor"`, 1)
	transport := &searchTransport{responses: []*responses.Response{
		searchResponse(t, output), searchResponse(t, searchTextJSON),
	}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	response, err := client.Complete(t.Context(), searchRequest())
	require.NoError(t, err)
	wire, err := json.Marshal(transport.requests[1])
	require.NoError(t, err)
	assert.NotContains(t, string(wire), "created_by")
	saved, err := json.Marshal(response.Content)
	require.NoError(t, err)
	assert.Contains(t, string(saved), "provider-actor")
	replay, err := encodeMessages([]*model.Message{&response.Content[0]}, nil)
	require.NoError(t, err)
	wire, err = json.Marshal(replay)
	require.NoError(t, err)
	assert.NotContains(t, string(wire), "created_by")
}

func TestNativeSearchFailuresRetainUsage(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("connection failed")} {
		transport := &searchTransport{responses: []*responses.Response{
			searchResponse(t, searchCallJSON),
		}, err: cause}
		client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
		require.NoError(t, err)
		_, err = client.Complete(t.Context(), searchRequest())
		require.ErrorIs(t, err, cause)
		require.NotNil(t, model.UsageFromError(err))
		assert.Equal(t, 12, model.UsageFromError(err).TotalTokens)
		var rejected *model.OutputValidationError
		assert.NotErrorAs(t, err, &rejected)
	}
	transport := &searchTransport{responses: []*responses.Response{searchResponse(t, searchCallJSON)}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	request := searchRequest()
	request.MaxTokens = 2
	_, err = client.Complete(t.Context(), request)
	var rejected *model.OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, 12, rejected.Usage().TotalTokens)
	assert.Len(t, transport.requests, 1)
}

func TestNativeSearchCatalogChangesRemoveLoadedDefinitions(t *testing.T) {
	transport := &searchTransport{responses: []*responses.Response{
		searchResponse(t, searchCallJSON), searchResponse(t, searchTextJSON),
	}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	request := searchRequest()
	response, err := client.Complete(t.Context(), request)
	require.NoError(t, err)
	for i := range response.Content {
		request.Messages = append(request.Messages, &response.Content[i])
	}
	for _, removed := range []bool{false, true} {
		changed := searchRequest()
		changed.Messages = request.Messages
		if removed {
			changed.Tools = changed.Tools[1:]
		} else {
			changed.Tools[0].Input = mustOpenAIToolInput(rawjson.Message(`{"type":"object","properties":{"zip":{"type":"string"}},"required":["zip"],"additionalProperties":false}`))
		}
		provider, err := newProvider(Options{DefaultModel: "gpt-5.4", transport: transport}, false)
		require.NoError(t, err)
		prepared, err := provider.prepareRequest(changed)
		require.NoError(t, err)
		assert.Empty(t, prepared.request.Input.OfInputItemList[2].OfToolSearchOutput.Tools)
	}
	// Filtering an encoded request must not edit the saved transcript.
	items, err := encodeMessages(request.Messages, nil)
	require.NoError(t, err)
	assert.Len(t, items[2].OfToolSearchOutput.Tools, 1)
}

func TestNativeSearchForcedToolIsImmediate(t *testing.T) {
	request := searchRequest()
	request.MaxTokens = 0
	request.ToolChoice = &model.ToolChoice{Mode: model.ToolChoiceModeTool, Name: "weather.lookup"}
	provider, err := newProvider(Options{DefaultModel: "gpt-5.4", transport: &searchTransport{}}, false)
	require.NoError(t, err)
	prepared, err := provider.prepareRequest(request)
	require.NoError(t, err)
	assert.Nil(t, prepared.search)
	require.Len(t, prepared.request.Tools, 1)
	assert.Equal(t, "weather_lookup", prepared.request.Tools[0].OfFunction.Name)
	assert.False(t, prepared.request.Tools[0].OfFunction.DeferLoading.Value)
	request.ToolChoice = nil
	_, err = provider.prepareRequest(request)
	require.ErrorContains(t, err, "require MaxTokens")
}

func searchRequest() *model.Request {
	return &model.Request{
		MaxTokens: 30,
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Forecast for Paris"}},
		}},
		Tools: []*model.ToolDefinition{
			{
				Name: "weather.lookup", Description: "Weather forecasts for a city.",
				Search:   tools.NewSearchDocument("weather.lookup Weather lookup Weather forecasts for a city."),
				Deferred: true,
				Input:    mustOpenAIToolInput(rawjson.Message(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`)),
			},
			{
				Name: "email.read", Description: "Email messages in the inbox.",
				Search:   tools.NewSearchDocument("email.read Read email Email messages in the inbox."),
				Deferred: true, Input: mustOpenAIToolInput(rawjson.Message(`{"type":"object","properties":{},"additionalProperties":false}`)),
			},
		},
	}
}

func searchResponse(t *testing.T, output string) *responses.Response {
	t.Helper()
	return mustResponse(t, fmt.Sprintf(`{"status":"completed","model":"gpt-5.4","output":[%s],"usage":{"input_tokens":10,"output_tokens":2,"total_tokens":12}}`, output))
}

func (s *searchTransport) Complete(_ context.Context, request responses.ResponseNewParams) (*responses.Response, error) {
	s.requests = append(s.requests, request)
	if len(s.responses) == 0 {
		return nil, s.err
	}
	response := s.responses[0]
	s.responses = s.responses[1:]
	return response, nil
}

func (*searchTransport) Stream(context.Context, responses.ResponseNewParams) responseStream {
	panic("stream not configured")
}
