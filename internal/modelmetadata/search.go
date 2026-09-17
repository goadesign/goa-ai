// Package modelmetadata defines the reasoning references shared by native
// search replay and the explicit policy that removes completed-turn reasoning.
// References preserve ordering; encrypted reasoning remains in its existing
// metadata field and is never copied into search records.
package modelmetadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

type (
	// OpenAIReasoningReference points to a native item in OpenAIReasoningItems.
	OpenAIReasoningReference struct {
		// Type is OpenAIReasoningReferenceType.
		Type string `json:"type"`
		// ID is the native reasoning item's exact identifier.
		ID string `json:"id"`
	}
)

const (
	// OpenAISearchBefore orders native discovery before assistant content.
	OpenAISearchBefore = "openai_tool_search_before_v1"
	// OpenAISearchAfter orders native discovery after assistant content.
	OpenAISearchAfter = "openai_tool_search_after_v1"
	// OpenAIReasoningReferenceType distinguishes reasoning references from
	// native search call and search result records.
	OpenAIReasoningReferenceType = "reasoning_reference"
)

// WithoutOpenAIReasoning removes native reasoning and references to it from
// already owned metadata. All search call and result bytes remain unchanged.
func WithoutOpenAIReasoning(meta map[string]any) error {
	for _, key := range []string{OpenAISearchBefore, OpenAISearchAfter} {
		value, exists := meta[key]
		if !exists {
			continue
		}
		var records []string
		switch actual := value.(type) {
		case []string:
			records = actual
		case []any:
			records = make([]string, len(actual))
			for i, value := range actual {
				record, ok := value.(string)
				if !ok {
					return fmt.Errorf("%s must contain strings", key)
				}
				records[i] = record
			}
		default:
			return fmt.Errorf("%s must contain a string array", key)
		}
		retained := make([]string, 0, len(records))
		for _, record := range records {
			var kind struct {
				Type string `json:"type"`
			}
			if err := json.Unmarshal([]byte(record), &kind); err != nil {
				return fmt.Errorf("decode native search record: %w", err)
			}
			if kind.Type == OpenAIReasoningReferenceType {
				if _, err := DecodeOpenAIReasoningReference(record); err != nil {
					return err
				}
				continue
			}
			retained = append(retained, record)
		}
		if len(retained) == 0 {
			delete(meta, key)
		} else {
			meta[key] = retained
		}
	}
	delete(meta, OpenAIReasoningItems)
	return nil
}

// DecodeOpenAIReasoningReference validates one private reference record. The
// surrounding search decoder owns validation of other native record types.
func DecodeOpenAIReasoningReference(record string) (OpenAIReasoningReference, error) {
	var reference OpenAIReasoningReference
	decoder := json.NewDecoder(bytes.NewBufferString(record))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reference); err != nil {
		return reference, err
	}
	if !json.Valid([]byte(record)) || reference.Type != OpenAIReasoningReferenceType || reference.ID == "" {
		return reference, errors.New("invalid native search reasoning reference")
	}
	return reference, nil
}
