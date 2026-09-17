// Package runtime renders progress labels when scheduled calls are stored. Stream
// subscribers forward the saved label and never run a second template.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"goa.design/goa-ai/runtime/agent/rawjson"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/tools"
)

// renderToolCallDisplayHint returns the canonical user-facing label for a
// scheduled tool call. Typed templates provide argument-specific wording when
// payload decoding succeeds; registered tool metadata provides the invariant
// display label when a malformed payload must still be shown and then rejected.
func (r *Runtime) renderToolCallDisplayHint(ctx context.Context, tool tools.Ident, payload any) (string, error) {
	override := r.hintOverrides[tool]
	typed := r.decodeHintPayload(ctx, tool, payload)
	if override != nil {
		if hint, ok := override(ctx, tool, typed); ok {
			if strings.TrimSpace(hint) == "" {
				return "", fmt.Errorf("runtime: hint override for tool %q returned empty display hint", tool)
			}
			return hint, nil
		}
	}
	if typed != nil {
		hint, ok, err := rthints.RenderCallHint(tool, typed)
		if err != nil {
			return "", err
		}
		if ok {
			return hint, nil
		}
	}
	return r.toolDisplayTitle(tool)
}

// toolDisplayTitle returns the generation-time display title for a registered
// tool. Missing metadata is a runtime registration invariant violation.
func (r *Runtime) toolDisplayTitle(tool tools.Ident) (string, error) {
	r.mu.RLock()
	if r.policyToolMetadata != nil {
		if meta, ok := r.policyToolMetadata[tool]; ok {
			r.mu.RUnlock()
			title := strings.TrimSpace(meta.Title)
			if title == "" {
				return "", fmt.Errorf("runtime: tool %q has empty display title", tool)
			}
			return title, nil
		}
	}
	r.mu.RUnlock()
	return "", fmt.Errorf("runtime: missing canonical display metadata for tool %q", tool)
}

// decodeHintPayload turns a canonical JSON payload into a typed value using the
// runtime's tool codecs.
//
// Contract:
//   - Tool payloads are canonical JSON values for the tool payload schema.
//   - A missing/empty payload is normalized to "{}" (empty object) so tools with
//     empty payload schemas still render call hints deterministically.
//   - Hints are only rendered from typed payloads produced by registered codecs.
//     If decode fails, this function returns nil.
func (r *Runtime) decodeHintPayload(ctx context.Context, tool tools.Ident, payload any) any {
	raw := json.RawMessage("{}")
	switch v := payload.(type) {
	case nil:
		// Keep canonical empty object.
	case rawjson.Message:
		if len(v) > 0 {
			raw = v.RawMessage()
		}
	case json.RawMessage:
		if len(v) > 0 {
			raw = v
		}
	case []byte:
		if len(v) > 0 {
			raw = json.RawMessage(v)
		}
	case string:
		if v == "" {
			break
		}
		b := []byte(v)
		if !json.Valid(b) {
			return nil
		}
		raw = json.RawMessage(b)
	default:
		// Already a typed value (e.g., inline producers); honor it as-is.
		return payload
	}
	raw = normalizeHintPayload(raw)

	// Prefer tool-specific codecs for schema-aware decoding.
	val, err := r.unmarshalToolValue(ctx, tool, raw, true)
	if err == nil {
		return val
	}
	return nil
}

func normalizeHintPayload(raw json.RawMessage) json.RawMessage {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return json.RawMessage("{}")
	}
	return raw
}
