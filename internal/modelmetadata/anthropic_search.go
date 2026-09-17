// Package modelmetadata points separately to Claude thinking and semantic parts, so
// removing completed-turn thinking cannot change the meaning of other indices.
package modelmetadata

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// AnthropicSearchParts orders native search blocks and canonical part references.
	AnthropicSearchParts = "anthropic_tool_search_parts_v1"
	// AnthropicThinkingReferenceType identifies a reference to a ThinkingPart.
	AnthropicThinkingReferenceType = "thinking_reference"
)

// WithoutAnthropicReasoning removes only thinking references from already owned
// search metadata. The adapter validates the remaining records when replayed.
func WithoutAnthropicReasoning(meta map[string]any) error {
	value, exists := meta[AnthropicSearchParts]
	if !exists {
		return nil
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
				return errors.New("anthropic search parts must contain strings")
			}
			records[i] = record
		}
	default:
		return errors.New("anthropic search parts must contain a string array")
	}
	retained := make([]string, 0, len(records))
	for _, record := range records {
		var reference struct {
			Type  string `json:"type"`
			Index int    `json:"index"`
		}
		if err := json.Unmarshal([]byte(record), &reference); err != nil {
			return fmt.Errorf("decode Claude search reference: %w", err)
		}
		if reference.Type == AnthropicThinkingReferenceType {
			decoder := json.NewDecoder(bytes.NewBufferString(record))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&reference); err != nil {
				return err
			}
			if reference.Index < 0 {
				return errors.New("negative Claude thinking reference")
			}
			continue
		}
		retained = append(retained, record)
	}
	meta[AnthropicSearchParts] = retained
	return nil
}
