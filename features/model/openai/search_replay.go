// Package openai retains only native discovery items beside canonical assistant
// messages. Search calls and application results keep their original positions;
// reasoning and business calls retain their existing replay representations.
package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/openai/openai-go/v3/packages/param"
	"github.com/openai/openai-go/v3/responses"

	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	searchQuery struct {
		Query string `json:"query"`
	}

	// searchCallRecord accepts only the native fields supported by this
	// adapter. It cannot carry a business call or private reasoning item.
	searchCallRecord struct {
		Type      string      `json:"type"`
		ID        string      `json:"id"`
		CallID    string      `json:"call_id"`
		Execution string      `json:"execution"`
		Status    string      `json:"status"`
		Arguments searchQuery `json:"arguments"`
		CreatedBy string      `json:"created_by,omitempty"`
	}

	searchOutputRecord struct {
		Type      string           `json:"type"`
		CallID    string           `json:"call_id"`
		Execution string           `json:"execution"`
		Status    string           `json:"status"`
		Tools     []searchFunction `json:"tools"`
	}

	// searchFunction is the closed function declaration this adapter produces.
	// Its schema is the already prepared provider projection, not a new schema
	// inferred from search text.
	searchFunction struct {
		Type         string          `json:"type"`
		Name         string          `json:"name"`
		Description  string          `json:"description"`
		Parameters   rawjson.Message `json:"parameters"`
		Strict       bool            `json:"strict"`
		DeferLoading bool            `json:"defer_loading"`
	}
)

const (
	nativeSearchCallType = "tool_search_call"
	searchBeforeMetaKey  = modelmetadata.OpenAISearchBefore
	searchAfterMetaKey   = modelmetadata.OpenAISearchAfter
	// searchResultLimit bounds newly returned definitions in one search result.
	// It does not limit the number of distinct tools discoverable in a run.
	searchResultLimit = 5
)

// decodeSearchJSON rejects unknown fields and trailing documents in provider
// calls and persisted discovery records. The model boundary already bounds
// their bytes before this decoder allocates their typed representation.
func decodeSearchJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("openai: invalid tool search record: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("openai: tool search record must contain one JSON value")
	}
	return nil
}

// searchFunctionFromTool reads this adapter's eager function or single-function
// namespace. Both contain the same schema used to compare saved discovery with
// the current permitted catalog.
func searchFunctionFromTool(tool responses.ToolUnionParam) (searchFunction, error) {
	var name, description string
	var parametersValue any
	var strict bool
	if namespace := tool.OfNamespace; namespace != nil {
		function := namespace.Tools[0].OfFunction
		name, description, parametersValue, strict = function.Name, function.Description.Value, function.Parameters, function.Strict.Value
	} else {
		function := tool.OfFunction
		name, description, parametersValue, strict = function.Name, function.Description.Value, function.Parameters, function.Strict.Value
	}
	parameters, err := json.Marshal(parametersValue)
	if err != nil {
		return searchFunction{}, fmt.Errorf("openai: encode search tool schema: %w", err)
	}
	return searchFunction{
		Type:         "function",
		Name:         name,
		Description:  description,
		Parameters:   parameters,
		Strict:       strict,
		DeferLoading: true,
	}, nil
}

// encodeSearchReplay restores native discovery before or after an ordinary
// assistant message. Only this adapter's closed call/output forms are accepted.
func encodeSearchReplay(meta map[string]any, key string) (responses.ResponseInputParam, error) {
	records, err := metaStrings(meta, key)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	items := make(responses.ResponseInputParam, 0, len(records))
	reasoning, err := decodeReasoningItemsMeta(meta)
	if err != nil {
		return nil, err
	}
	reasoningByID := make(map[string]responses.ResponseReasoningItemParam, len(reasoning))
	for _, item := range reasoning {
		reasoningByID[item.ID] = item
	}
	for _, record := range records {
		var discriminator struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(record), &discriminator); err != nil {
			return nil, fmt.Errorf("openai: decode tool search replay type: %w", err)
		}
		switch discriminator.Type {
		case modelmetadata.OpenAIReasoningReferenceType:
			reference, err := modelmetadata.DecodeOpenAIReasoningReference(record)
			if err != nil {
				return nil, err
			}
			item, exists := reasoningByID[reference.ID]
			if !exists {
				return nil, errors.New("openai: search references a missing reasoning item")
			}
			items = append(items, responses.ResponseInputItemUnionParam{OfReasoning: &item})
		case nativeSearchCallType:
			var call searchCallRecord
			if err := decodeSearchJSON([]byte(record), &call); err != nil {
				return nil, err
			}
			if err := call.validate(); err != nil {
				return nil, err
			}
			native := &responses.ResponseInputItemToolSearchCallParam{
				ID:        param.NewOpt(call.ID),
				CallID:    param.NewOpt(call.CallID),
				Execution: call.Execution,
				Status:    call.Status,
				Arguments: map[string]any{"query": call.Arguments.Query},
			}
			items = append(items, responses.ResponseInputItemUnionParam{OfToolSearchCall: native})
		case "tool_search_output":
			var output searchOutputRecord
			if err := decodeSearchJSON([]byte(record), &output); err != nil {
				return nil, err
			}
			if output.CallID == "" || output.Execution != "client" || output.Status != "completed" {
				return nil, errors.New("openai: tool search output requires a completed client call")
			}
			if output.Tools == nil {
				return nil, errors.New("openai: tool search output requires a tools array")
			}
			if len(output.Tools) > searchResultLimit {
				return nil, errors.New("openai: tool search output exceeds its definition limit")
			}
			native := &responses.ResponseToolSearchOutputItemParam{
				CallID:    param.NewOpt(output.CallID),
				Execution: responses.ResponseToolSearchOutputItemParamExecutionClient,
				Status:    responses.ResponseToolSearchOutputItemParamStatusCompleted,
				Tools:     make([]responses.ToolUnionParam, 0, len(output.Tools)),
			}
			seen := make(map[string]struct{}, len(output.Tools))
			for _, function := range output.Tools {
				if function.Type != "function" || function.Name == "" ||
					function.Description == "" || !function.DeferLoading ||
					len(function.Parameters) == 0 || function.Parameters[0] != '{' {
					return nil, errors.New("openai: invalid function in tool search output")
				}
				if _, exists := seen[function.Name]; exists {
					return nil, errors.New("openai: duplicate function in tool search output")
				}
				seen[function.Name] = struct{}{}
				parameters, err := sdkSchema(function.Parameters)
				if err != nil {
					return nil, fmt.Errorf("openai: replay search schema: %w", err)
				}
				// Bedrock omits the namespace of a bare dynamically loaded
				// function, then rejects that same call during replay. An
				// explicit namespace makes the returned identity complete.
				native.Tools = append(native.Tools, responses.ToolUnionParam{OfNamespace: &responses.NamespaceToolParam{
					Name:        function.Name,
					Description: function.Description,
					Tools: []responses.NamespaceToolToolUnionParam{{OfFunction: &responses.NamespaceToolToolFunctionParam{
						Name:         function.Name,
						Description:  param.NewOpt(function.Description),
						Parameters:   parameters,
						Strict:       param.NewOpt(function.Strict),
						DeferLoading: param.NewOpt(true),
					}}},
				}})
			}
			items = append(items, responses.ResponseInputItemUnionParam{OfToolSearchOutput: native})
		default:
			return nil, fmt.Errorf("openai: unsupported tool search replay type %q", discriminator.Type)
		}
	}
	return items, nil
}

// filterSearchHistory validates call/result correlation and removes historical
// definitions that are no longer in the current permitted contract. Stored
// messages are untouched; only the newly encoded provider request is changed.
func filterSearchHistory(input responses.ResponseInputParam, current map[string]searchFunction) error {
	pending := make(map[string]struct{})
	seen := make(map[string]struct{})
	for _, item := range input {
		if call := item.OfToolSearchCall; call != nil {
			id := call.CallID.Value
			if _, exists := seen[id]; exists {
				return errors.New("openai: duplicate tool search call ID in history")
			}
			pending[id] = struct{}{}
			seen[id] = struct{}{}
		}
		output := item.OfToolSearchOutput
		if output == nil {
			continue
		}
		id := output.CallID.Value
		if _, ok := pending[id]; !ok {
			return errors.New("openai: tool search output has no preceding unresolved call")
		}
		delete(pending, id)
		retained := make([]responses.ToolUnionParam, 0, len(output.Tools))
		for _, tool := range output.Tools {
			historical, err := searchFunctionFromTool(tool)
			if err != nil {
				return err
			}
			allowed, ok := current[historical.Name]
			if ok && historical.equal(allowed) {
				retained = append(retained, tool)
			}
		}
		output.Tools = retained
	}
	if len(pending) > 0 {
		return errors.New("openai: history contains an unresolved tool search call")
	}
	return nil
}

// appendSearchRecords attaches owned discovery items at one precise position
// around a canonical message. These records never contain reasoning.
func appendSearchRecords(message *model.Message, key string, records []string) {
	if len(records) == 0 {
		return
	}
	if message.Meta == nil {
		message.Meta = make(map[string]any)
	}
	if prior, ok := message.Meta[key]; ok {
		records = append(prior.([]string), records...)
	}
	message.Meta[key] = records
}

func (call searchCallRecord) validate() error {
	if call.Type != nativeSearchCallType || call.ID == "" || call.CallID == "" ||
		call.Execution != "client" || call.Status != "completed" {
		return errors.New("openai: tool search requires a completed client call with an ID")
	}
	if call.Arguments.Query == "" {
		return errors.New("openai: tool search query is required")
	}
	return nil
}

func (function searchFunction) equal(other searchFunction) bool {
	return function.Name == other.Name && function.Description == other.Description &&
		function.Strict == other.Strict && bytes.Equal(function.Parameters, other.Parameters)
}
