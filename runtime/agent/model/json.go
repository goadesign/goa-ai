// Package model defines JSON helpers for marshaling and unmarshaling provider
// message parts. This file focuses on decoding messages and discriminating
// concrete part types based on the Kind field.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
)

// UnmarshalJSON decodes a Message while materializing concrete Part
// implementations stored in the Parts slice.
func (m *Message) UnmarshalJSON(data []byte) error {
	type alias struct {
		Role  ConversationRole `json:"Role"` //nolint:tagliatelle
		Parts []json.RawMessage
		Meta  map[string]any `json:"Meta"` //nolint:tagliatelle
	}
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	m.Role = tmp.Role
	m.Meta = tmp.Meta
	if len(tmp.Parts) == 0 {
		m.Parts = nil
		return nil
	}
	m.Parts = make([]Part, 0, len(tmp.Parts))
	for i, raw := range tmp.Parts {
		part, err := decodeMessagePart(raw)
		if err != nil {
			return fmt.Errorf("decode parts[%d]: %w", i, err)
		}
		m.Parts = append(m.Parts, part)
	}
	return nil
}

func decodeMessagePart(raw json.RawMessage) (Part, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		var text string
		if errText := json.Unmarshal(raw, &text); errText == nil {
			return TextPart{Text: text}, nil
		}
		return nil, fmt.Errorf("decode part object: %w", err)
	}
	if len(obj) == 0 {
		return nil, errors.New("empty part payload")
	}

	// Discriminator-based decoding when Kind is present (preferred).
	if kindRaw, ok := obj["Kind"]; ok {
		var kind string
		if err := json.Unmarshal(kindRaw, &kind); err != nil {
			return nil, fmt.Errorf("decode Kind: %w", err)
		}
		switch kind {
		case "thinking":
			var thinking ThinkingPart
			if err := json.Unmarshal(raw, &thinking); err != nil {
				return nil, fmt.Errorf("decode ThinkingPart: %w", err)
			}
			return thinking, nil
		case "tool_result":
			var result ToolResultPart
			if err := json.Unmarshal(raw, &result); err != nil {
				return nil, fmt.Errorf("decode ToolResultPart: %w", err)
			}
			if result.ToolUseID == "" {
				return nil, errors.New("ToolResultPart requires ToolUseID")
			}
			return result, nil
		case "tool_use":
			var use ToolUsePart
			if err := json.Unmarshal(raw, &use); err != nil {
				return nil, fmt.Errorf("decode ToolUsePart: %w", err)
			}
			if use.Name == "" {
				return nil, errors.New("ToolUsePart requires Name")
			}
			if use.Input == nil {
				if v, hasArgs := obj["Args"]; hasArgs {
					var args any
					if err := json.Unmarshal(v, &args); err != nil {
						return nil, fmt.Errorf("decode ToolUsePart Args: %w", err)
					}
					use.Input = args
				}
			}
			return use, nil
		case "text":
			var text TextPart
			if err := json.Unmarshal(raw, &text); err != nil {
				return nil, fmt.Errorf("decode TextPart: %w", err)
			}
			return text, nil
		default:
			return nil, fmt.Errorf("unknown part kind %q", kind)
		}
	}

	if hasAnyKey(obj, "Signature", "Redacted", "Index", "Final") {
		var thinking ThinkingPart
		if err := json.Unmarshal(raw, &thinking); err != nil {
			return nil, fmt.Errorf("decode ThinkingPart: %w", err)
		}
		return thinking, nil
	}

	if _, ok := obj["ToolUseID"]; ok {
		var result ToolResultPart
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode ToolResultPart: %w", err)
		}
		if result.ToolUseID == "" {
			return nil, errors.New("ToolResultPart requires ToolUseID")
		}
		return result, nil
	}

	if _, ok := obj["Name"]; ok {
		var use ToolUsePart
		if err := json.Unmarshal(raw, &use); err != nil {
			return nil, fmt.Errorf("decode ToolUsePart: %w", err)
		}
		if use.Name == "" {
			return nil, errors.New("ToolUsePart requires Name")
		}

		if _, hasInput := obj["Input"]; !hasInput {
			if v, hasArgs := obj["Args"]; hasArgs {
				var args any
				if err := json.Unmarshal(v, &args); err != nil {
					return nil, fmt.Errorf("decode ToolUsePart Args: %w", err)
				}
				use.Input = args
			}
		}

		return use, nil
	}

	if _, ok := obj["Text"]; ok {
		var text TextPart
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("decode TextPart: %w", err)
		}
		return text, nil
	}

	return nil, errors.New("unknown part shape")
}

func hasAnyKey(obj map[string]json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}
