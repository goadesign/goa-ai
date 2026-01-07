// Package model defines JSON helpers for marshaling and unmarshaling provider
// message parts. This file focuses on decoding messages and discriminating
// concrete part types based on the Kind field.
package model

import (
	"encoding/json"
	"errors"
	"fmt"
)

// MarshalJSON encodes a Message while preserving the concrete Part types stored
// in Parts via an explicit Kind discriminator.
//
// This ensures round-trips through JSON do not lose type information when Parts
// are stored as an interface slice.
func (m Message) MarshalJSON() ([]byte, error) {
	type alias struct {
		Role  ConversationRole `json:"Role"`  //nolint:tagliatelle
		Parts []any            `json:"Parts"` //nolint:tagliatelle
		Meta  map[string]any   `json:"Meta"`  //nolint:tagliatelle
	}
	if len(m.Parts) == 0 {
		return json.Marshal(alias{
			Role:  m.Role,
			Parts: nil,
			Meta:  m.Meta,
		})
	}

	parts := make([]any, 0, len(m.Parts))
	for i, p := range m.Parts {
		enc, err := encodeMessagePart(p)
		if err != nil {
			return nil, fmt.Errorf("encode parts[%d]: %w", i, err)
		}
		parts = append(parts, enc)
	}

	return json.Marshal(alias{
		Role:  m.Role,
		Parts: parts,
		Meta:  m.Meta,
	})
}

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

func encodeMessagePart(p Part) (any, error) {
	switch v := p.(type) {
	case ThinkingPart:
		return struct {
			Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
			ThinkingPart
		}{
			Kind:         "thinking",
			ThinkingPart: v,
		}, nil
	case TextPart:
		return struct {
			Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
			TextPart
		}{
			Kind:     "text",
			TextPart: v,
		}, nil
	case ImagePart:
		return struct {
			Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
			ImagePart
		}{
			Kind:      "image",
			ImagePart: v,
		}, nil
	case DocumentPart:
		return struct {
			Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
			DocumentPart
		}{
			Kind:         "document",
			DocumentPart: v,
		}, nil
	case CitationsPart:
		return struct {
			Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
			CitationsPart
		}{
			Kind:          "citations",
			CitationsPart: v,
		}, nil
	case ToolUsePart:
		return struct {
			Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
			ToolUsePart
		}{
			Kind:        "tool_use",
			ToolUsePart: v,
		}, nil
	case ToolResultPart:
		return struct {
			Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
			ToolResultPart
		}{
			Kind:           "tool_result",
			ToolResultPart: v,
		}, nil
	case CacheCheckpointPart:
		return struct {
			Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
		}{
			Kind: "cache_checkpoint",
		}, nil
	default:
		return nil, fmt.Errorf("unknown part type %T", p)
	}
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
		case "image":
			var img ImagePart
			if err := json.Unmarshal(raw, &img); err != nil {
				return nil, fmt.Errorf("decode ImagePart: %w", err)
			}
			if img.Format == "" {
				return nil, errors.New("ImagePart requires Format")
			}
			if len(img.Bytes) == 0 {
				return nil, errors.New("ImagePart requires Bytes")
			}
			return img, nil
		case "document":
			var doc DocumentPart
			if err := json.Unmarshal(raw, &doc); err != nil {
				return nil, fmt.Errorf("decode DocumentPart: %w", err)
			}
			if doc.Name == "" {
				return nil, errors.New("DocumentPart requires Name")
			}
			sourceCount := 0
			if len(doc.Bytes) > 0 {
				sourceCount++
			}
			if doc.Text != "" {
				sourceCount++
			}
			if len(doc.Chunks) > 0 {
				sourceCount++
			}
			if doc.URI != "" {
				sourceCount++
			}
			if sourceCount != 1 {
				return nil, errors.New("DocumentPart requires exactly one of Bytes, Text, Chunks, or URI")
			}
			for i, chunk := range doc.Chunks {
				if chunk == "" {
					return nil, fmt.Errorf("DocumentPart requires non-empty Chunks[%d]", i)
				}
			}
			return doc, nil
		case "thinking":
			var thinking ThinkingPart
			if err := json.Unmarshal(raw, &thinking); err != nil {
				return nil, fmt.Errorf("decode ThinkingPart: %w", err)
			}
			return thinking, nil
		case "citations":
			var citations CitationsPart
			if err := json.Unmarshal(raw, &citations); err != nil {
				return nil, fmt.Errorf("decode CitationsPart: %w", err)
			}
			return citations, nil
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
		case "cache_checkpoint":
			return CacheCheckpointPart{}, nil
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
