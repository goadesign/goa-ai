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
// sidecars). A single accidental `json.RawMessage{}` or `[]byte{}` assignment
// can therefore crash workflow encoding.
//
// Message eliminates that failure mode by normalizing empty/whitespace payloads
// to JSON null during marshaling while still validating non-empty payloads.
package rawjson

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Message is an opaque JSON value encoded as bytes.
//
// Contract:
//   - Nil represents absence (preferred).
//   - Non-empty values must be valid JSON.
//   - Empty/whitespace-only values are normalized to JSON null during marshaling
//     to avoid runtime encoding failures at workflow boundaries.
type Message json.RawMessage

// RawMessage returns the underlying value as json.RawMessage.
func (r Message) RawMessage() json.RawMessage {
	return json.RawMessage(r)
}

// MarshalJSON implements json.Marshaler.
//
// This method never returns an "unexpected end of JSON input" error for empty
// slices; empty/whitespace is encoded as JSON null.
func (r Message) MarshalJSON() ([]byte, error) {
	data := []byte(r)
	if len(bytes.TrimSpace(data)) == 0 {
		return []byte("null"), nil
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
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
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
