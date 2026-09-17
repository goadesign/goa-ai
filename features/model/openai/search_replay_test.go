// Persisted search records are a closed protocol. Invalid records fail before
// provider dispatch; no-match searches remain valid empty results.
package openai

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestNativeSearchRejectsIncompleteOrUnadvertisedNamespace(t *testing.T) {
	for _, namespace := range []string{"", "another_namespace"} {
		call := strings.Replace(searchToolJSON, `"namespace":"weather_lookup"`, `"namespace":"`+namespace+`"`, 1)
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", namespace, streaming), func(t *testing.T) {
				var client model.Client
				var err error
				if streaming {
					client, err = New(Options{DefaultModel: "gpt-5.4", transport: &searchStreamTransport{
						streams: []responseStream{
							searchResponseStream(t, searchCallJSON),
							searchResponseStream(t, call),
						},
					}})
				} else {
					client, err = New(Options{DefaultModel: "gpt-5.4", transport: &searchTransport{
						responses: []*responses.Response{
							searchResponse(t, searchCallJSON),
							searchResponse(t, call),
						},
					}})
				}
				require.NoError(t, err)
				if streaming {
					stream, streamErr := client.Stream(t.Context(), searchRequest())
					require.NoError(t, streamErr)
					for {
						chunk, recvErr := stream.Recv()
						_, isCall := chunk.(model.ToolCallChunk)
						assert.False(t, isCall, "invalid identity must not become an executable call")
						if recvErr != nil {
							err = recvErr
							break
						}
					}
					require.NotErrorIs(t, err, io.EOF)
					require.NoError(t, stream.Close())
				} else {
					_, err = client.Complete(t.Context(), searchRequest())
				}
				var rejected *model.OutputValidationError
				require.ErrorAs(t, err, &rejected)
				assert.Equal(t, model.OutputValidationToolIdentity, rejected.Kind())
				assert.Contains(t, rejected.Unwrap().Error(), "namespace")
				require.NotNil(t, model.UsageFromError(err))
				assert.Equal(t, 24, model.UsageFromError(err).TotalTokens)
			})
		}
	}
}

func TestNativeSearchRejectsMalformedReplay(t *testing.T) {
	for _, record := range []string{
		`{"type":"tool_search_call","id":"s","call_id":"c","execution":"client","status":"completed","arguments":{"query":1}}`,
		`{"type":"tool_search_call","id":"s","call_id":"c","execution":"client","status":"completed","arguments":{"query":"weather","name":"hidden"}}`,
		`{"type":"tool_search_call","id":"s","call_id":"c","execution":"server","status":"completed","arguments":{"query":"weather"}}`,
		`{"type":"tool_search_call","id":"s","call_id":"c","execution":"client","status":"in_progress","arguments":{"query":"weather"}}`,
		`{"type":"tool_search_call","id":"s","call_id":"c","execution":"client","status":"completed","arguments":{"query":""}}`,
		`{"type":"tool_search_output","call_id":"c","execution":"client","status":"completed"}`,
		`{"type":"tool_search_output","call_id":"c","execution":"client","status":"completed","tools":null}`,
		`{"type":"function_call","name":"weather_lookup","arguments":"{}"}`,
		`{"type":"reasoning_reference","id":"missing"}`,
		`{"type":"reasoning_reference","id":"rs_1","unexpected":true}`,
	} {
		_, err := encodeSearchReplay(map[string]any{searchBeforeMetaKey: []string{record}}, searchBeforeMetaKey)
		require.Error(t, err, record)
	}
	call, err := encodeSearchReplay(map[string]any{searchBeforeMetaKey: []string{searchCallJSON}}, searchBeforeMetaKey)
	require.NoError(t, err)
	require.ErrorContains(t, filterSearchHistory(call, nil), "unresolved")
	call = append(call, call...)
	require.ErrorContains(t, filterSearchHistory(call, nil), "duplicate")
}

func TestNativeSearchNoMatchAndMultipleSearchesReplayOnce(t *testing.T) {
	first := strings.ReplaceAll(searchCallJSON, `"weather"`, `"unknown-capability"`)
	second := strings.ReplaceAll(strings.ReplaceAll(searchCallJSON, `"search-1"`, `"search-2"`), `"search-call-1"`, `"search-call-2"`)
	transport := &searchTransport{responses: []*responses.Response{
		searchResponse(t, first), searchResponse(t, second), searchResponse(t, searchTextJSON),
	}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	response, err := client.Complete(t.Context(), searchRequest())
	require.NoError(t, err)
	assert.Equal(t, 36, response.Usage.TotalTokens)
	require.Len(t, transport.requests, 3)
	assert.Empty(t, transport.requests[1].Input.OfInputItemList[2].OfToolSearchOutput.Tools)
	require.Len(t, transport.requests[2].Input.OfInputItemList, 5)
	require.Len(t, response.Content, 1)
	items, err := encodeMessages([]*model.Message{&response.Content[0]}, nil)
	require.NoError(t, err)
	require.Len(t, items, 5)
	require.NoError(t, filterSearchHistory(items, nil))
	assert.NotNil(t, items[0].OfToolSearchCall)
	assert.NotNil(t, items[1].OfToolSearchOutput)
	assert.NotNil(t, items[2].OfToolSearchCall)
	assert.NotNil(t, items[3].OfToolSearchOutput)
	assert.NotNil(t, items[4].OfOutputMessage)
}

func TestNativeSearchIndexesOnlyPreparedDocuments(t *testing.T) {
	index := newSearchIndex([]searchDocument{
		{name: "a", document: tools.NewSearchDocument("weather")},
		{name: "b", document: tools.NewSearchDocument("weather")},
		{name: "c", document: tools.NewSearchDocument("email inbox")},
	})
	assert.Empty(t, index.search("unknown", 5))
	hits := index.search("WEATHER, weather!", 5)
	require.Len(t, hits, 2)
	assert.Equal(t, "a", hits[0].name)
	assert.Equal(t, "b", hits[1].name)
	assert.InDelta(t, hits[0].score, hits[1].score, 0)
	assert.Len(t, index.search("weather", 1), 1)
}

func TestNativeSearchCountUsesPreparedPrefix(t *testing.T) {
	raw, err := newProvider(Options{DefaultModel: "gpt-5.4", transport: &searchTransport{}}, true)
	require.NoError(t, err)
	counter := &bedrockProvider{provider: raw}
	request := searchRequest()
	request.Tools[0].Description = strings.Repeat("Weather forecasts and precipitation. ", 100)
	request.Tools[0].Search = tools.NewSearchDocument(request.Tools[0].Name + " " + request.Tools[0].Description)
	deferred, err := counter.CountTokens(t.Context(), request)
	require.NoError(t, err)
	for _, definition := range request.Tools {
		definition.Deferred = false
	}
	immediate, err := counter.CountTokens(t.Context(), request)
	require.NoError(t, err)
	assert.False(t, deferred.Exact)
	assert.Less(t, deferred.InputTokens, immediate.InputTokens)
}

func TestNativeSearchReasoningReferencesAreNotDuplicated(t *testing.T) {
	reference, err := json.Marshal(modelmetadata.OpenAIReasoningReference{
		Type: modelmetadata.OpenAIReasoningReferenceType, ID: "rs_1",
	})
	require.NoError(t, err)
	meta := map[string]any{
		openAIReasoningItemsMetaKey: []string{bedrockReasoning},
		searchBeforeMetaKey:         []string{string(reference), string(reference)},
	}
	before, err := encodeSearchReplay(meta, searchBeforeMetaKey)
	require.NoError(t, err)
	require.ErrorContains(t, validateSearchReasoning(meta, before, nil), "repeated")
	meta[searchBeforeMetaKey] = []string{searchCallJSON}
	before, err = encodeSearchReplay(meta, searchBeforeMetaKey)
	require.NoError(t, err)
	require.ErrorContains(t, validateSearchReasoning(meta, before, nil), "unreferenced")
}

func TestNativeSearchMixedModelUsageKeepsCountsWithoutFalseAttribution(t *testing.T) {
	search := &toolSearch{}
	for _, modelID := range []string{"revision-1", "revision-2", "revision-2"} {
		require.NoError(t, search.account(model.TokenUsage{
			Model: modelID, InputTokens: 10, OutputTokens: 2, TotalTokens: 12,
		}))
	}
	assert.Equal(t, model.TokenUsage{InputTokens: 30, OutputTokens: 6, TotalTokens: 36}, search.usage)
}
