// Package rawjson provides a safe raw JSON byte type for workflow boundaries.
//
// # Motivation
//
// Go's json.RawMessage implements json.Marshaler and treats a non-nil empty
// slice as invalid JSON, returning:
// "json: error calling MarshalJSON for type json.RawMessage: unexpected end of JSON input".
//
// In this runtime, raw JSON byte fields are intentionally used at workflow and
// activity boundaries (tool payloads/results, hook envelopes, server-data
// sidecars). Message makes absence explicit as nil and rejects accidental
// non-nil empty or malformed payloads at marshaling.
package rawjson

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Message is an opaque JSON value encoded as bytes.
//
// Contract:
//   - Nil represents absence and marshals as JSON null.
//   - Non-empty values must be valid JSON.
//   - Non-nil empty or whitespace-only values are invalid.
type Message json.RawMessage

// Unmarshal decodes one JSON value while preserving numbers as json.Number.
// Generated codecs remain responsible for typed validation.
func Unmarshal(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("rawjson: trailing data")
	}
	return nil
}

// RawMessage returns the underlying value as json.RawMessage.
func (r Message) RawMessage() json.RawMessage {
	return json.RawMessage(r)
}

// MarshalJSON implements json.Marshaler.
//
// Nil encodes absence as JSON null. Non-nil values must contain canonical JSON.
func (r Message) MarshalJSON() ([]byte, error) {
	if r == nil {
		return []byte("null"), nil
	}
	data := []byte(r)
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("rawjson: non-nil message is empty")
	}
	if !json.Valid(data) {
		return nil, fmt.Errorf("rawjson: invalid JSON")
	}
	return data, nil
}

// UnmarshalJSON implements json.Unmarshaler.
//
// The decoder validates non-null JSON and normalizes null to nil.
func (r *Message) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return fmt.Errorf("rawjson: JSON value is empty")
	}
	if bytes.Equal(trimmed, []byte("null")) {
		*r = nil
		return nil
	}
	if !json.Valid(trimmed) {
		return fmt.Errorf("rawjson: invalid JSON")
	}
	out := make([]byte, len(trimmed))
	copy(out, trimmed)
	*r = Message(out)
	return nil
}
