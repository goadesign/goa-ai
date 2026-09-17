// Package anthropic retains Claude's native discovery blocks beside semantic
// message parts. Only the supported search protocol can enter this metadata;
// ordinary text, reasoning, and application calls remain canonical parts.
package anthropic

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"

	sdk "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/packages/param"

	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	searchDefinition struct {
		Canonical     string          `json:"canonical"`
		Name          string          `json:"name"`
		Description   string          `json:"description"`
		InputSchema   rawjson.Message `json:"input_schema"`
		InputExamples rawjson.Message `json:"input_examples,omitempty"`
	}

	searchCall struct {
		Type  string `json:"type"`
		ID    string `json:"id"`
		Name  string `json:"name"`
		Input struct {
			Pattern string `json:"pattern"`
			Limit   *int   `json:"limit,omitempty"`
		} `json:"input"`
		Caller *struct {
			Type string `json:"type"`
		} `json:"caller,omitempty"`
	}

	searchResult struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
		Content   struct {
			Type           string `json:"type"`
			ErrorCode      string `json:"error_code,omitempty"`
			ErrorMessage   string `json:"error_message,omitempty"`
			ToolReferences []struct {
				Type     string `json:"type"`
				ToolName string `json:"tool_name"`
			} `json:"tool_references,omitempty"`
		} `json:"content"`
	}

	searchChange struct {
		Type string `json:"type"`
		Tool struct {
			Type string `json:"type"`
			Name string `json:"name"`
		} `json:"tool"`
	}

	searchPartReference struct {
		Type  string `json:"type"`
		Index int    `json:"index"`
	}
)

const (
	nativeSearchCallType   = "server_tool_use"
	nativeSearchResultType = "tool_search_tool_result"
	toolAdditionType       = "tool_addition"
	toolRemovalType        = "tool_removal"
	toolReferenceType      = "tool_reference"
	claudeSearchName       = "tool_search_tool_regex"
	searchDefsKey          = "anthropic_tool_search_definitions_v1"
	searchChangesKey       = "anthropic_tool_search_changes_v1"
	searchPartsKey         = modelmetadata.AnthropicSearchParts
	semanticReference      = "part_reference"
	thinkingReference      = modelmetadata.AnthropicThinkingReferenceType
)

// decodeSearchRecord rejects fields outside this adapter's closed replay
// format, including extra JSON documents. Request preflight bounds the bytes.
func decodeSearchRecord(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("anthropic: invalid search record: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("anthropic: search record must contain one JSON value")
	}
	return nil
}

func searchRecordStrings(meta map[string]any, key string) ([]string, error) {
	value, exists := meta[key]
	if !exists {
		return nil, nil
	}
	switch actual := value.(type) {
	case []string:
		return actual, nil
	case []any:
		records := make([]string, len(actual))
		for i, value := range actual {
			record, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("anthropic: %s must contain strings", key)
			}
			records[i] = record
		}
		return records, nil
	default:
		return nil, fmt.Errorf("anthropic: %s must contain a string array", key)
	}
}

// searchRecordType reads only the discriminator. The selected decoder then
// validates every accepted field before the record is replayed.
func searchRecordType(record string) (string, error) {
	var kind struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(record), &kind); err != nil {
		return "", fmt.Errorf("anthropic: decode search record type: %w", err)
	}
	return kind.Type, nil
}

func decodeSearchCall(record string) (searchCall, error) {
	var call searchCall
	if err := decodeSearchRecord([]byte(record), &call); err != nil {
		return call, err
	}
	if call.Type != nativeSearchCallType || call.Name != claudeSearchName ||
		call.ID == "" || call.Input.Pattern == "" ||
		(call.Input.Limit != nil && *call.Input.Limit <= 0) ||
		(call.Caller != nil && call.Caller.Type != "direct") {
		return call, errors.New("anthropic: invalid native search call")
	}
	return call, nil
}

func decodeSearchResult(record string) (searchResult, error) {
	var result searchResult
	if err := decodeSearchRecord([]byte(record), &result); err != nil {
		return result, err
	}
	if result.Type != nativeSearchResultType || result.ToolUseID == "" {
		return result, errors.New("anthropic: invalid native search result")
	}
	var shape struct {
		Content struct {
			ToolReferences json.RawMessage `json:"tool_references"`
		} `json:"content"`
	}
	if err := json.Unmarshal([]byte(record), &shape); err != nil {
		return result, err
	}
	references := bytes.TrimSpace(shape.Content.ToolReferences)
	switch result.Content.Type {
	case "tool_search_tool_search_result":
		if result.Content.ErrorCode != "" || result.Content.ErrorMessage != "" {
			return result, errors.New("anthropic: successful search result contains an error")
		}
		if len(references) == 0 || references[0] != '[' {
			return result, errors.New("anthropic: successful search requires a tool_references array")
		}
		names := make(map[string]struct{}, len(result.Content.ToolReferences))
		for _, reference := range result.Content.ToolReferences {
			if reference.Type != toolReferenceType || reference.ToolName == "" {
				return result, errors.New("anthropic: invalid search tool reference")
			}
			if _, exists := names[reference.ToolName]; exists {
				return result, errors.New("anthropic: duplicate search tool reference")
			}
			names[reference.ToolName] = struct{}{}
		}
	case "tool_search_tool_result_error":
		codes := []string{"invalid_tool_input", "unavailable", "too_many_requests", "execution_time_exceeded"}
		if !slices.Contains(codes, result.Content.ErrorCode) || len(references) > 0 {
			return result, errors.New("anthropic: invalid search error result")
		}
	default:
		return result, fmt.Errorf("anthropic: unsupported search result %q", result.Content.Type)
	}
	return result, nil
}

func decodeSearchChange(record string) (searchChange, error) {
	var change searchChange
	if err := decodeSearchRecord([]byte(record), &change); err != nil {
		return change, err
	}
	if (change.Type != toolAdditionType && change.Type != toolRemovalType) ||
		change.Tool.Type != toolReferenceType || change.Tool.Name == "" {
		return change, errors.New("anthropic: invalid search availability change")
	}
	return change, nil
}

// searchChangeMessage uses the SDK's typed beta content blocks without changing
// the public MessagesClient interface. Each accepted directive refers to one
// definition in the request's top-level tools.
func searchChangeMessage(records []string) (sdk.MessageParam, error) {
	blocks := make([]sdk.ContentBlockParamUnion, len(records))
	for i, record := range records {
		change, err := decodeSearchChange(record)
		if err != nil {
			return sdk.MessageParam{}, err
		}
		reference := &sdk.BetaToolChangeToolReferenceParam{Name: change.Tool.Name}
		switch change.Type {
		case toolAdditionType:
			blocks[i] = param.Override[sdk.ContentBlockParamUnion](sdk.BetaRequestToolAdditionBlockParam{
				Tool: sdk.BetaRequestToolAdditionBlockToolUnionParam{OfToolReference: reference},
			})
		case toolRemovalType:
			blocks[i] = param.Override[sdk.ContentBlockParamUnion](sdk.BetaRequestToolRemovalBlockParam{
				Tool: sdk.BetaRequestToolRemovalBlockToolUnionParam{OfToolReference: reference},
			})
		}
	}
	return sdk.MessageParam{Role: sdk.MessageParamRoleSystem, Content: blocks}, nil
}
