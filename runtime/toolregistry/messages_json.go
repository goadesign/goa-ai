// Package toolregistry decodes result messages at registry, stream, and retained
// record boundaries. Tool-specific values stay raw for their existing codecs.
package toolregistry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
)

// DecodeToolResultMessage decodes one non-null JSON object into the existing wire
// message. It rejects unknown fields in fixed records and trailing documents,
// returning a zero message on every error. Result, server-data payloads, and
// recovery JSON retain their existing raw JSON representations.
//
// Decoding follows encoding/json's duplicate-member, case-insensitive field
// matching, and Unicode replacement behavior. It does not validate identities,
// success/error/retry combinations, or tool-specific result and bounds contracts.
// Callers apply the existing validators after their identity filtering.
func DecodeToolResultMessage(data []byte) (ToolResultMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var message *ToolResultMessage
	if err := decoder.Decode(&message); err != nil {
		return ToolResultMessage{}, fmt.Errorf("decode tool result message: %w", err)
	}
	if message == nil {
		return ToolResultMessage{}, fmt.Errorf("decode tool result message: expected non-null object")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err != nil {
			return ToolResultMessage{}, fmt.Errorf("decode tool result message: trailing data: %w", err)
		}
		return ToolResultMessage{}, fmt.Errorf("decode tool result message: trailing document")
	}
	return *message, nil
}
