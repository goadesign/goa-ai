// Package openai groups native search replay with semantic assistant content.
// This keeps messages valid after completed-turn reasoning is removed, while
// references retain the exact native order during an unfinished tool exchange.
package openai

import (
	"encoding/json"
	"errors"
	"maps"

	"github.com/openai/openai-go/v3/responses"

	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/model"
)

// groupSearchMessages combines reasoning-only rows with adjacent semantic
// content. Each reasoning item is stored once and referenced at its native
// position among search calls and results.
func groupSearchMessages(content []model.Message) ([]model.Message, error) {
	var grouped []model.Message
	var pending []string
	var reasoning []string
	var thinking []model.Part
	for _, message := range content {
		before, err := metaStrings(message.Meta, searchBeforeMetaKey)
		if err != nil {
			return nil, err
		}
		pending = append(pending, before...)
		nativeReasoning, err := decodeReasoningItemsMeta(message.Meta)
		if err != nil {
			return nil, err
		}
		rawReasoning, err := metaStrings(message.Meta, openAIReasoningItemsMetaKey)
		if err != nil {
			return nil, err
		}
		for _, item := range nativeReasoning {
			if item.ID == "" {
				return nil, errors.New("openai: search reasoning requires a native item ID")
			}
			reference, err := json.Marshal(modelmetadata.OpenAIReasoningReference{
				Type: modelmetadata.OpenAIReasoningReferenceType, ID: item.ID,
			})
			if err != nil {
				return nil, err
			}
			pending = append(pending, string(reference))
		}
		reasoning = append(reasoning, rawReasoning...)
		var visible []model.Part
		for _, part := range message.Parts {
			if _, ok := part.(model.ThinkingPart); ok {
				thinking = append(thinking, part)
			} else {
				visible = append(visible, part)
			}
		}
		after, err := metaStrings(message.Meta, searchAfterMetaKey)
		if err != nil {
			return nil, err
		}
		if len(visible) > 0 {
			message.Meta = maps.Clone(message.Meta)
			delete(message.Meta, searchBeforeMetaKey)
			delete(message.Meta, searchAfterMetaKey)
			delete(message.Meta, openAIReasoningItemsMetaKey)
			thinking = append(thinking, visible...)
			message.Parts = thinking
			appendSearchRecords(&message, openAIReasoningItemsMetaKey, reasoning)
			appendSearchRecords(&message, searchBeforeMetaKey, pending)
			grouped = append(grouped, message)
			pending, reasoning, thinking = nil, nil, nil
		}
		pending = append(pending, after...)
	}
	if len(grouped) == 0 {
		return nil, errors.New("openai: tool search invocation finished without text or a tool call")
	}
	last := &grouped[len(grouped)-1]
	last.Parts = append(last.Parts, thinking...)
	appendSearchRecords(last, openAIReasoningItemsMetaKey, reasoning)
	appendSearchRecords(last, searchAfterMetaKey, pending)
	return grouped, nil
}

// validateSearchReasoning requires every saved reasoning item to appear exactly
// once in an ordered search record. Application metadata cannot conceal or
// duplicate a native reasoning item.
func validateSearchReasoning(meta map[string]any, before, after responses.ResponseInputParam) error {
	_, hasBefore := meta[searchBeforeMetaKey]
	_, hasAfter := meta[searchAfterMetaKey]
	if !hasBefore && !hasAfter {
		return nil
	}
	reasoning, err := decodeReasoningItemsMeta(meta)
	if err != nil {
		return err
	}
	pending := make(map[string]struct{}, len(reasoning))
	for _, item := range reasoning {
		if _, exists := pending[item.ID]; exists {
			return errors.New("openai: repeated reasoning item ID in search history")
		}
		pending[item.ID] = struct{}{}
	}
	for _, items := range []responses.ResponseInputParam{before, after} {
		for _, item := range items {
			if item.OfReasoning == nil {
				continue
			}
			id := item.OfReasoning.ID
			if _, exists := pending[id]; !exists {
				return errors.New("openai: repeated or unknown reasoning reference in search history")
			}
			delete(pending, id)
		}
	}
	if len(pending) > 0 {
		return errors.New("openai: search history contains unreferenced reasoning")
	}
	return nil
}
