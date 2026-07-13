// Package model defines canonical JSON helpers for provider metadata and
// message parts. This file preserves metadata values and discriminates concrete
// part types by their Kind field.
package model

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"goa.design/goa-ai/runtime/agent/rawjson"
)

// MarshalMetadata encodes Message.Meta as an opaque JSON object for transport
// or persistence. Nil and empty metadata both encode as nil, the canonical
// representation of absent metadata.
func MarshalMetadata(metadata map[string]any) (rawjson.Message, error) {
	if len(metadata) == 0 {
		return nil, nil
	}
	data, err := json.Marshal(metadata)
	if err != nil {
		return nil, fmt.Errorf("model: marshal message metadata: %w", err)
	}
	return rawjson.Message(data), nil
}

// UnmarshalMetadata decodes one Message.Meta JSON object. Numbers are retained
// as json.Number so provider-authored identifiers and counters replay without
// float64 precision loss. Nil and empty objects both decode as nil.
func UnmarshalMetadata(data rawjson.Message) (map[string]any, error) {
	if data == nil {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, errors.New("model: message metadata is empty")
	}
	if trimmed[0] != '{' {
		return nil, errors.New("model: message metadata must be a JSON object")
	}
	var metadata map[string]any
	if err := rawjson.Unmarshal(trimmed, &metadata); err != nil {
		return nil, fmt.Errorf("model: unmarshal message metadata: %w", err)
	}
	if len(metadata) == 0 {
		return nil, nil
	}
	return metadata, nil
}

// MarshalJSON encodes a Message while preserving the concrete Part types stored
// in Parts via an explicit Kind discriminator.
//
// This ensures round-trips through JSON do not lose type information when Parts
// are stored as an interface slice.
func (m Message) MarshalJSON() ([]byte, error) {
	type alias struct {
		Role  ConversationRole `json:"role"`
		Parts []any            `json:"parts"`
		Meta  map[string]any   `json:"meta"`
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
		Role  ConversationRole  `json:"role"`
		Parts []json.RawMessage `json:"parts"`
		Meta  map[string]any    `json:"meta"`
	}
	var tmp alias
	if err := validateExactJSONKeys(data, "role", "parts", "meta"); err != nil {
		return err
	}
	if err := decodeCanonicalJSON(data, &tmp); err != nil {
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
			Kind string `json:"kind"`
			ThinkingPart
		}{
			Kind:         "thinking",
			ThinkingPart: v,
		}, nil
	case TextPart:
		return struct {
			Kind string `json:"kind"`
			TextPart
		}{
			Kind:     "text",
			TextPart: v,
		}, nil
	case ImagePart:
		if v.Format == "" || len(v.Bytes) == 0 {
			return nil, errors.New("ImagePart requires format and bytes")
		}
		return struct {
			Kind string `json:"kind"`
			ImagePart
		}{
			Kind:      "image",
			ImagePart: v,
		}, nil
	case DocumentPart:
		if err := validateDocumentPart(v); err != nil {
			return nil, err
		}
		return struct {
			Kind string `json:"kind"`
			DocumentPart
		}{
			Kind:         "document",
			DocumentPart: v,
		}, nil
	case CitationsPart:
		return struct {
			Kind string `json:"kind"`
			CitationsPart
		}{
			Kind:          "citations",
			CitationsPart: v,
		}, nil
	case ToolUsePart:
		if v.ID == "" || v.Name == "" {
			return nil, errors.New("ToolUsePart requires id and name")
		}
		if data := bytes.TrimSpace(v.Input); !json.Valid(data) || len(data) == 0 || data[0] != '{' {
			return nil, errors.New("ToolUsePart requires input to be a JSON object")
		}
		return struct {
			Kind string `json:"kind"`
			ToolUsePart
		}{
			Kind:        "tool_use",
			ToolUsePart: v,
		}, nil
	case ToolResultPart:
		if v.ToolUseID == "" {
			return nil, errors.New("ToolResultPart requires tool_use_id")
		}
		return struct {
			Kind string `json:"kind"`
			ToolResultPart
		}{
			Kind:           "tool_result",
			ToolResultPart: v,
		}, nil
	case CacheCheckpointPart:
		return struct {
			Kind string `json:"kind"`
		}{
			Kind: "cache_checkpoint",
		}, nil
	default:
		return nil, fmt.Errorf("unknown part type %T", p)
	}
}

func decodeMessagePart(raw json.RawMessage) (Part, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, fmt.Errorf("decode part object: %w", err)
	}
	rawKind, ok := envelope["kind"]
	if !ok {
		return nil, errors.New("message part requires kind")
	}
	var kind string
	if err := json.Unmarshal(rawKind, &kind); err != nil {
		return nil, fmt.Errorf("decode part kind: %w", err)
	}
	if kind == "" {
		return nil, errors.New("message part requires kind")
	}
	switch kind {
	case "thinking":
		var encoded struct {
			Kind string `json:"kind"`
			ThinkingPart
		}
		if err := decodeCanonicalPartJSON(raw, &encoded, "kind", "text", "signature", "redacted", "index", "final"); err != nil {
			return nil, fmt.Errorf("decode ThinkingPart: %w", err)
		}
		return encoded.ThinkingPart, nil
	case "text":
		var encoded struct {
			Kind string `json:"kind"`
			TextPart
		}
		if err := decodeCanonicalPartJSON(raw, &encoded, "kind", "text"); err != nil {
			return nil, fmt.Errorf("decode TextPart: %w", err)
		}
		return encoded.TextPart, nil
	case "image":
		var encoded struct {
			Kind string `json:"kind"`
			ImagePart
		}
		if err := decodeCanonicalPartJSON(raw, &encoded, "kind", "format", "bytes"); err != nil {
			return nil, fmt.Errorf("decode ImagePart: %w", err)
		}
		part := encoded.ImagePart
		if part.Format == "" || len(part.Bytes) == 0 {
			return nil, errors.New("ImagePart requires format and bytes")
		}
		return part, nil
	case "document":
		var encoded struct {
			Kind string `json:"kind"`
			DocumentPart
		}
		if err := decodeCanonicalPartJSON(
			raw,
			&encoded,
			"kind",
			"name",
			"format",
			"bytes",
			"text",
			"chunks",
			"uri",
			"context",
			"cite",
		); err != nil {
			return nil, fmt.Errorf("decode DocumentPart: %w", err)
		}
		part := encoded.DocumentPart
		if err := validateDocumentPart(part); err != nil {
			return nil, err
		}
		return part, nil
	case "citations":
		var encoded struct {
			Kind string `json:"kind"`
			CitationsPart
		}
		if err := decodeCanonicalPartJSON(raw, &encoded, "kind", "text", "citations"); err != nil {
			return nil, fmt.Errorf("decode CitationsPart: %w", err)
		}
		if err := validateCitationJSON(raw); err != nil {
			return nil, fmt.Errorf("decode CitationsPart: %w", err)
		}
		return encoded.CitationsPart, nil
	case "tool_use":
		var encoded struct {
			Kind             string          `json:"kind"`
			ID               string          `json:"id"`
			Name             string          `json:"name"`
			Input            json.RawMessage `json:"input"`
			ThoughtSignature string          `json:"thought_signature"`
		}
		if err := decodeCanonicalPartJSON(raw, &encoded, "kind", "id", "name", "input", "thought_signature"); err != nil {
			return nil, fmt.Errorf("decode ToolUsePart: %w", err)
		}
		if encoded.ID == "" || encoded.Name == "" {
			return nil, errors.New("ToolUsePart requires id and name")
		}
		if data := bytes.TrimSpace(encoded.Input); !json.Valid(data) || len(data) == 0 || data[0] != '{' {
			return nil, errors.New("ToolUsePart requires input to be a JSON object")
		}
		return ToolUsePart{
			ID:               encoded.ID,
			Name:             encoded.Name,
			Input:            append(rawjson.Message(nil), encoded.Input...),
			ThoughtSignature: encoded.ThoughtSignature,
		}, nil
	case "tool_result":
		var encoded struct {
			Kind string `json:"kind"`
			ToolResultPart
		}
		if err := decodeCanonicalPartJSON(raw, &encoded, "kind", "tool_use_id", "content", "is_error"); err != nil {
			return nil, fmt.Errorf("decode ToolResultPart: %w", err)
		}
		part := encoded.ToolResultPart
		if part.ToolUseID == "" {
			return nil, errors.New("ToolResultPart requires tool_use_id")
		}
		return part, nil
	case "cache_checkpoint":
		var encoded struct {
			Kind string `json:"kind"`
		}
		if err := decodeCanonicalPartJSON(raw, &encoded, "kind"); err != nil {
			return nil, fmt.Errorf("decode CacheCheckpointPart: %w", err)
		}
		return CacheCheckpointPart{}, nil
	default:
		return nil, fmt.Errorf("unknown part kind %q", kind)
	}
}

// decodeCanonicalPartJSON rejects non-canonical field names before decoding a
// concrete message part.
func decodeCanonicalPartJSON(data []byte, value any, fields ...string) error {
	if err := validateExactJSONKeys(data, fields...); err != nil {
		return err
	}
	return decodeCanonicalJSON(data, value)
}

// validateExactJSONKeys enforces exact canonical field spelling. The standard
// library otherwise matches JSON keys case-insensitively, which would silently
// preserve obsolete Temporal payload shapes.
func validateExactJSONKeys(data []byte, allowed ...string) error {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(data, &object); err != nil {
		return err
	}
	if object == nil {
		return errors.New("expected JSON object")
	}
	fields := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		fields[field] = struct{}{}
	}
	keys := make([]string, 0, len(object))
	for field := range object {
		keys = append(keys, field)
	}
	sort.Strings(keys)
	for _, field := range keys {
		if _, ok := fields[field]; !ok {
			return fmt.Errorf("json: unknown field %q", field)
		}
	}
	return nil
}

// validateCitationJSON enforces canonical field spelling inside nested
// citation and location objects.
func validateCitationJSON(data []byte) error {
	var part struct {
		Citations []json.RawMessage `json:"citations"`
	}
	if err := json.Unmarshal(data, &part); err != nil {
		return err
	}
	for citationIndex, rawCitation := range part.Citations {
		if err := validateExactJSONKeys(rawCitation, "title", "source", "location", "source_content"); err != nil {
			return fmt.Errorf("citation %d: %w", citationIndex, err)
		}
		var citation struct {
			Location json.RawMessage `json:"location"`
		}
		if err := json.Unmarshal(rawCitation, &citation); err != nil {
			return fmt.Errorf("citation %d: %w", citationIndex, err)
		}
		if err := validateExactJSONKeys(citation.Location, "document_char", "document_chunk", "document_page"); err != nil {
			return fmt.Errorf("citation %d location: %w", citationIndex, err)
		}
		var location struct {
			DocumentChar  json.RawMessage `json:"document_char"`
			DocumentChunk json.RawMessage `json:"document_chunk"`
			DocumentPage  json.RawMessage `json:"document_page"`
		}
		if err := decodeCanonicalJSON(citation.Location, &location); err != nil {
			return fmt.Errorf("citation %d location: %w", citationIndex, err)
		}
		locations := []struct {
			name string
			data json.RawMessage
		}{
			{name: "document_char", data: location.DocumentChar},
			{name: "document_chunk", data: location.DocumentChunk},
			{name: "document_page", data: location.DocumentPage},
		}
		for _, nested := range locations {
			if len(nested.data) == 0 || bytes.Equal(nested.data, []byte("null")) {
				continue
			}
			if err := validateExactJSONKeys(nested.data, "document_index", "start", "end"); err != nil {
				return fmt.Errorf("citation %d %s: %w", citationIndex, nested.name, err)
			}
		}
	}
	return nil
}

// decodeCanonicalJSON preserves JSON numbers when decoding interface-valued
// metadata and tool results while retaining json.Unmarshal's strict framing.
func decodeCanonicalJSON(data []byte, value any) error {
	if !json.Valid(data) {
		return errors.New("invalid JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	return decoder.Decode(value)
}

func validateDocumentPart(part DocumentPart) error {
	if part.Name == "" {
		return errors.New("DocumentPart requires Name")
	}
	sourceCount := 0
	if len(part.Bytes) > 0 {
		sourceCount++
	}
	if part.Text != "" {
		sourceCount++
	}
	if len(part.Chunks) > 0 {
		sourceCount++
	}
	if part.URI != "" {
		sourceCount++
	}
	if sourceCount != 1 {
		return errors.New("DocumentPart requires exactly one of Bytes, Text, Chunks, or URI")
	}
	for i, chunk := range part.Chunks {
		if chunk == "" {
			return fmt.Errorf("DocumentPart requires non-empty Chunks[%d]", i)
		}
	}
	return nil
}
