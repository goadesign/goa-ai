// Package model defines JSON helpers for marshaling provider message parts.
// This file emits discriminated unions for ThinkingPart, TextPart, ToolUsePart,
// ToolResultPart, and CacheCheckpointPart so decode logic can recover the
// concrete types.
package model

import "encoding/json"

// MarshalJSON encodes ThinkingPart with a Kind discriminator so that concrete
// part types can be recovered reliably when stored as generic Parts.
func (p ThinkingPart) MarshalJSON() ([]byte, error) {
	type alias ThinkingPart
	return json.Marshal(struct {
		Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
		alias
	}{
		Kind:  "thinking",
		alias: alias(p),
	})
}

// MarshalJSON encodes TextPart with a Kind discriminator to distinguish it from
// ThinkingPart in generic JSON payloads.
func (p TextPart) MarshalJSON() ([]byte, error) {
	type alias TextPart
	return json.Marshal(struct {
		Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
		alias
	}{
		Kind:  "text",
		alias: alias(p),
	})
}

// MarshalJSON encodes ToolUsePart with a Kind discriminator so decode logic can
// reconstruct tool_use blocks precisely.
func (p ToolUsePart) MarshalJSON() ([]byte, error) {
	type alias ToolUsePart
	return json.Marshal(struct {
		Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
		alias
	}{
		Kind:  "tool_use",
		alias: alias(p),
	})
}

// MarshalJSON encodes ToolResultPart with a Kind discriminator so decode logic
// can reconstruct tool_result blocks precisely.
func (p ToolResultPart) MarshalJSON() ([]byte, error) {
	type alias ToolResultPart
	return json.Marshal(struct {
		Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
		alias
	}{
		Kind:  "tool_result",
		alias: alias(p),
	})
}

// MarshalJSON encodes CacheCheckpointPart with a Kind discriminator so decode
// logic can reconstruct cache checkpoint blocks precisely.
func (p CacheCheckpointPart) MarshalJSON() ([]byte, error) {
	type alias CacheCheckpointPart
	return json.Marshal(struct {
		Kind string `json:"Kind"` //nolint:tagliatelle // Kind discriminator is intentionally upper-cased for compatibility.
		alias
	}{
		Kind:  "cache_checkpoint",
		alias: alias(p),
	})
}
