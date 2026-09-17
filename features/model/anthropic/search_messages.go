// Package anthropic preserves provider block order without copying
// semantic content into metadata. Thinking uses a separate index sequence so
// the completed-turn policy can remove it without moving tool or text indices.
package anthropic

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	sdk "github.com/anthropics/anthropic-sdk-go"

	"goa.design/goa-ai/features/model/internal/outputvalidation"
	"goa.design/goa-ai/runtime/agent/model"
)

// translateSearchResponse separates native discovery from semantic blocks,
// translates the latter through the ordinary response decoder, and retains only
// this invocation's discovery and newly injected availability changes.
func translateSearchResponse(msg *sdk.Message, enc *encodedRequest) (*model.Response, error) {
	if msg == nil {
		return translateResponse(msg, enc.provToCanon)
	}
	semantic := *msg
	semantic.Content = make([]sdk.ContentBlockUnion, 0, len(msg.Content))
	records := make([]string, 0, len(msg.Content))
	referenced := make(map[string]struct{})
	semanticIndex, thinkingIndex := 0, 0
	hasSearch := false
	for _, block := range msg.Content {
		switch block.Type {
		case nativeSearchCallType, nativeSearchResultType:
			if !enc.search.enabled {
				return nil, outputvalidation.New(model.OutputValidationResponseShape,
					errors.New("anthropic: native search was not enabled for this response"))
			}
			hasSearch = true
			record := block.RawJSON()
			records = append(records, record)
			if block.Type == nativeSearchResultType {
				result, err := decodeSearchResult(record)
				if err != nil {
					return nil, outputvalidation.New(model.OutputValidationResponseShape, err)
				}
				for _, reference := range result.Content.ToolReferences {
					if _, current := enc.provToCanon[reference.ToolName]; !current {
						return nil, outputvalidation.New(model.OutputValidationResponseShape,
							fmt.Errorf("anthropic: search returned an unavailable tool %q", reference.ToolName))
					}
					referenced[reference.ToolName] = struct{}{}
				}
			}
		default:
			reference := searchPartReference{Type: semanticReference, Index: semanticIndex}
			if block.Type == "thinking" || block.Type == "redacted_thinking" {
				reference.Type = thinkingReference
				reference.Index = thinkingIndex
				thinkingIndex++
			} else {
				semanticIndex++
			}
			record, err := json.Marshal(reference)
			if err != nil {
				return nil, outputvalidation.New(model.OutputValidationResponseShape, err)
			}
			records = append(records, string(record))
			semantic.Content = append(semantic.Content, block)
		}
	}
	response, err := translateResponse(&semantic, enc.provToCanon)
	if err != nil {
		return nil, err
	}
	if !hasSearch && len(enc.search.changes) == 0 {
		return response, nil
	}
	if len(response.Content) == 0 || semanticIndex == 0 {
		return nil, outputvalidation.New(model.OutputValidationResponseShape,
			errors.New("anthropic: native search response has no semantic content"))
	}
	if err := validateSearchSequence(records, enc.search.definitions, enc.search.seenCalls); err != nil {
		return nil, outputvalidation.New(model.OutputValidationResponseShape, err)
	}
	for _, record := range enc.search.changes {
		change, err := decodeSearchChange(record)
		if err != nil {
			return nil, outputvalidation.New(model.OutputValidationResponseShape, err)
		}
		referenced[change.Tool.Name] = struct{}{}
	}
	names := make([]string, 0, len(referenced))
	for name := range referenced {
		names = append(names, name)
	}
	slices.Sort(names)
	definitions := make([]string, 0, len(names))
	for _, name := range names {
		definition, exists := enc.search.definitions[name]
		if !exists {
			return nil, outputvalidation.New(model.OutputValidationResponseShape,
				fmt.Errorf("anthropic: search returned an undeclared tool %q", name))
		}
		record, err := json.Marshal(definition)
		if err != nil {
			return nil, outputvalidation.New(model.OutputValidationResponseShape, err)
		}
		definitions = append(definitions, string(record))
	}
	meta := map[string]any{searchPartsKey: records}
	if len(definitions) > 0 {
		meta[searchDefsKey] = definitions
	}
	if len(enc.search.changes) > 0 {
		meta[searchChangesKey] = slices.Clone(enc.search.changes)
	}
	response.Content[0].Meta = meta
	return response, nil
}

// orderSearchBlocks expands validated references to canonical parts and native
// search records. Each semantic or thinking part must appear exactly once and
// retain its order among parts of the same kind.
func orderSearchBlocks(message *model.Message, blocks []sdk.ContentBlockParamUnion) ([]sdk.ContentBlockParamUnion, error) {
	records, err := searchRecordStrings(message.Meta, searchPartsKey)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return blocks, nil
	}
	if len(blocks) != len(message.Parts) {
		return nil, errors.New("anthropic: search replay requires one encoded block per canonical part")
	}
	var semantic, thinking []sdk.ContentBlockParamUnion
	for i, part := range message.Parts {
		if _, ok := part.(model.ThinkingPart); ok {
			thinking = append(thinking, blocks[i])
		} else {
			semantic = append(semantic, blocks[i])
		}
	}
	ordered := make([]sdk.ContentBlockParamUnion, 0, len(records))
	semanticIndex, thinkingIndex := 0, 0
	for _, record := range records {
		kind, err := searchRecordType(record)
		if err != nil {
			return nil, err
		}
		switch kind {
		case semanticReference, thinkingReference:
			var reference searchPartReference
			if err := decodeSearchRecord([]byte(record), &reference); err != nil {
				return nil, err
			}
			switch kind {
			case semanticReference:
				if reference.Index != semanticIndex || semanticIndex >= len(semantic) {
					return nil, errors.New("anthropic: inconsistent semantic part reference")
				}
				ordered = append(ordered, semantic[semanticIndex])
				semanticIndex++
			case thinkingReference:
				if reference.Index != thinkingIndex || thinkingIndex >= len(thinking) {
					return nil, errors.New("anthropic: inconsistent thinking part reference")
				}
				ordered = append(ordered, thinking[thinkingIndex])
				thinkingIndex++
			}
		case nativeSearchCallType:
			if _, err := decodeSearchCall(record); err != nil {
				return nil, err
			}
			var native sdk.ServerToolUseBlockParam
			if err := json.Unmarshal([]byte(record), &native); err != nil {
				return nil, err
			}
			ordered = append(ordered, sdk.ContentBlockParamUnion{OfServerToolUse: &native})
		case nativeSearchResultType:
			if _, err := decodeSearchResult(record); err != nil {
				return nil, err
			}
			var native sdk.ToolSearchToolResultBlockParam
			if err := json.Unmarshal([]byte(record), &native); err != nil {
				return nil, err
			}
			ordered = append(ordered, sdk.ContentBlockParamUnion{OfToolSearchToolResult: &native})
		default:
			return nil, fmt.Errorf("anthropic: unsupported search record %q", kind)
		}
	}
	if semanticIndex != len(semantic) || thinkingIndex != len(thinking) {
		return nil, errors.New("anthropic: search replay omits canonical parts")
	}
	return ordered, nil
}
