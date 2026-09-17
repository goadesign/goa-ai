// Package openai executes native client tool searches within one model call.
// The current permitted catalog stays private. Only retrieved definitions enter
// native history, and all physical requests share the original output budget.
package openai

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"goa.design/goa-ai/features/model/internal/outputvalidation"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// toolSearch exists only for one invocation and is never persisted.
	toolSearch struct {
		index     *searchIndex
		functions map[string]responses.ToolUnionParam
		seen      map[string]struct{}
		pending   []string
		outputs   []string
		content   []model.Message
		usage     model.TokenUsage
		thinking  int
		searches  int
	}
)

// prepareToolSearch withholds deferred definitions, validates native history,
// and installs the query tool only when the request permits discovery.
func prepareToolSearch(req *model.Request, native *responses.ResponseNewParams) (*toolSearch, error) {
	current := make(map[string]searchFunction, len(req.Tools))
	search := &toolSearch{
		functions: make(map[string]responses.ToolUnionParam),
		seen:      make(map[string]struct{}),
	}
	var eager []responses.ToolUnionParam
	var documents []searchDocument
	for i, definition := range req.Tools {
		encoded := native.Tools[i]
		snapshot, err := searchFunctionFromTool(encoded)
		if err != nil {
			return nil, err
		}
		current[snapshot.Name] = snapshot
		forced := req.ToolChoice != nil && req.ToolChoice.Mode == model.ToolChoiceModeTool &&
			req.ToolChoice.Name == definition.Name
		if !definition.Deferred || forced {
			eager = append(eager, encoded)
			continue
		}
		encoded.OfFunction.DeferLoading = param.NewOpt(true)
		search.functions[definition.Name] = encoded
		documents = append(documents, searchDocument{
			name: definition.Name, document: definition.Search,
		})
	}
	if err := filterSearchHistory(native.Input.OfInputItemList, current); err != nil {
		return nil, err
	}
	native.Tools = eager
	disabled := req.ToolChoice != nil &&
		(req.ToolChoice.Mode == model.ToolChoiceModeNone || req.ToolChoice.Mode == model.ToolChoiceModeTool)
	if len(documents) == 0 || disabled {
		return nil, nil
	}
	if native.MaxOutputTokens.Value <= 0 {
		return nil, errors.New("openai: deferred tools require MaxTokens or MaxCompletionTokens")
	}
	search.index = newSearchIndex(documents)
	for _, item := range native.Input.OfInputItemList {
		if call := item.OfToolSearchCall; call != nil {
			search.seen[call.CallID.Value] = struct{}{}
		}
	}
	native.Tools = append(native.Tools, responses.ToolUnionParam{OfToolSearch: &responses.ToolSearchToolParam{
		Execution: "client",
		Description: param.NewOpt(
			"Search available tool names and descriptions using capability keywords. Returns matching complete tool definitions.",
		),
		Parameters: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"query": map[string]any{"type": "string", "minLength": 1},
			},
			"required": []string{"query"}, "additionalProperties": false,
		},
	}})
	return search, nil
}

// record accepts a completed native search and produces its exact application
// result. Search results do not become business calls or executor work.
func (s *toolSearch) record(native responses.ResponseToolSearchCall) error {
	var call searchCallRecord
	if err := decodeSearchJSON([]byte(native.RawJSON()), &call); err != nil {
		return err
	}
	if err := call.validate(); err != nil {
		return err
	}
	if _, exists := s.seen[call.CallID]; exists {
		return errors.New("openai: repeated native tool search call ID")
	}
	s.seen[call.CallID] = struct{}{}
	callJSON, err := json.Marshal(call)
	if err != nil {
		return err
	}
	output := searchOutputRecord{
		Type: "tool_search_output", CallID: call.CallID, Execution: "client", Status: "completed",
		Tools: make([]searchFunction, 0, searchResultLimit),
	}
	for _, hit := range s.index.search(call.Arguments.Query, searchResultLimit) {
		function, err := searchFunctionFromTool(s.functions[hit.name])
		if err != nil {
			return err
		}
		output.Tools = append(output.Tools, function)
	}
	outputJSON, err := json.Marshal(output)
	if err != nil {
		return err
	}
	s.pending = append(s.pending, string(callJSON))
	s.outputs = append(s.outputs, string(outputJSON))
	s.searches++
	return nil
}

// account keeps every completed provider request's usage, including a response
// whose output will subsequently fail validation.
func (s *toolSearch) account(usage model.TokenUsage) error {
	total, err := model.AddTokenUsage(s.usage, usage)
	if err != nil {
		return outputvalidation.New(model.OutputValidationUsage, err)
	}
	total.Model, total.ModelClass = usage.Model, usage.ModelClass
	if s.usage != (model.TokenUsage{}) && s.usage.Model != usage.Model {
		total.Model = ""
	}
	s.usage = total
	return nil
}

// appendMessage attaches queued native items immediately before their next
// canonical message; ordinary text, tool calls, and reasoning keep their forms.
func (s *toolSearch) appendMessage(content *[]model.Message, message model.Message) {
	appendSearchRecords(&message, searchBeforeMetaKey, s.pending)
	s.pending = nil
	*content = append(*content, message)
}

// finishRound preserves native ordering and decides whether another model
// request is needed. Business calls always return to the runtime immediately.
func (s *toolSearch) finishRound(response *model.Response) bool {
	s.content = append(s.content, response.Content...)
	s.pending = append(s.pending, s.outputs...)
	if len(s.content) > 0 {
		appendSearchRecords(&s.content[len(s.content)-1], searchAfterMetaKey, s.pending)
		s.pending = nil
	}
	return s.searches > 0 && len(response.ToolCalls()) == 0
}

// continueRequest appends provider-native output and search results to this
// invocation's owned input. It never uses server-stored history or resets caps.
func (s *toolSearch) continueRequest(request *responses.ResponseNewParams, response *responses.Response) error {
	if response.Usage.OutputTokens <= 0 {
		return outputvalidation.New(model.OutputValidationUsage,
			errors.New("openai: completed tool search requires positive output token usage"))
	}
	remaining := request.MaxOutputTokens.Value - response.Usage.OutputTokens
	if remaining <= 0 || openAIOutputLimited(response) {
		return outputvalidation.New(model.OutputValidationResponseShape,
			errors.New("openai: tool search exhausted the invocation output budget"))
	}
	for _, item := range response.Output {
		if item.Type == nativeSearchCallType {
			items, err := encodeSearchReplay(map[string]any{
				searchBeforeMetaKey: []string{item.RawJSON()},
			}, searchBeforeMetaKey)
			if err != nil {
				return outputvalidation.New(model.OutputValidationResponseShape, err)
			}
			request.Input.OfInputItemList = append(request.Input.OfInputItemList, items...)
			continue
		}
		var input responses.ResponseInputItemUnionParam
		if err := json.Unmarshal([]byte(item.RawJSON()), &input); err != nil {
			return outputvalidation.New(model.OutputValidationResponseShape,
				fmt.Errorf("openai: encode search continuation: %w", err))
		}
		request.Input.OfInputItemList = append(request.Input.OfInputItemList, input)
	}
	items, err := encodeSearchReplay(map[string]any{searchAfterMetaKey: s.outputs}, searchAfterMetaKey)
	if err != nil {
		return outputvalidation.New(model.OutputValidationResponseShape, err)
	}
	for _, item := range items {
		if item.OfToolSearchOutput != nil {
			request.Input.OfInputItemList = append(request.Input.OfInputItemList, item)
		}
	}
	request.MaxOutputTokens = param.NewOpt(remaining)
	encoded, err := json.Marshal(request)
	if err != nil {
		return outputvalidation.New(model.OutputValidationResponseShape, err)
	}
	if len(encoded) > 16<<20 {
		return outputvalidation.New(model.OutputValidationOutputBounds,
			errors.New("openai: tool search continuation exceeds 16777216 bytes"))
	}
	s.searches = 0
	s.outputs = nil
	return nil
}

// complete returns only this invocation's accumulated assistant messages and
// usage; the caller's earlier transcript is never copied into the response.
func (s *toolSearch) complete(response *model.Response) error {
	content, err := groupSearchMessages(s.content)
	if err != nil {
		return outputvalidation.New(model.OutputValidationResponseShape, err)
	}
	response.Content = content
	response.Usage = s.usage
	return nil
}
