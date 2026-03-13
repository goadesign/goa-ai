package runtime

import (
	"context"
	"encoding/json"

	"goa.design/goa-ai/runtime/agent/rawjson"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

// hintingSink decorates a stream.Sink by enriching tool start events with
// typed, schema-aware call hints. It decodes canonical JSON payloads using
// the runtime's tool codecs before executing hint templates.
type hintingSink struct {
	rt   *Runtime
	sink stream.Sink
}

// newHintingSink wraps s with hint enrichment when a runtime is available.
// When rt or s is nil, it returns s unchanged.
func newHintingSink(rt *Runtime, s stream.Sink) stream.Sink {
	if rt == nil || s == nil {
		return s
	}
	return &hintingSink{rt: rt, sink: s}
}

// Send intercepts ToolStart events to populate DisplayHint using typed
// payloads from tool codecs. All other events are forwarded unchanged.
func (h *hintingSink) Send(ctx context.Context, ev stream.Event) error {
	switch e := ev.(type) {
	case stream.ToolStart:
		data := e.Data

		toolName := tools.Ident(data.ToolName)
		override := h.rt.hintOverrides[toolName]
		if data.DisplayHint == "" || override != nil {
			if typed := h.decodePayload(ctx, toolName, data.Payload); typed != nil {
				if override != nil {
					if hint, ok := override(ctx, toolName, typed); ok {
						data.DisplayHint = hint
					}
				}
				if data.DisplayHint == "" {
					if s := rthints.FormatCallHint(toolName, typed); s != "" {
						data.DisplayHint = s
					}
				}
			}
		}
		base := stream.NewBase(e.Type(), e.RunID(), e.SessionID(), data)
		return h.sink.Send(ctx, stream.ToolStart{
			Base: base,
			Data: data,
		})
	default:
		return h.sink.Send(ctx, ev)
	}
}

// Close delegates to the underlying sink.
func (h *hintingSink) Close(ctx context.Context) error {
	return h.sink.Close(ctx)
}

// decodePayload turns a canonical JSON payload into a typed value using the
// runtime's tool codecs.
//
// Contract:
//   - Tool payloads are canonical JSON values for the tool payload schema.
//   - A missing/empty payload is normalized to "{}" (empty object) so tools with
//     empty payload schemas still render call hints deterministically.
//   - Hints are only rendered from typed payloads produced by registered codecs.
//     If decode fails, this function returns nil.
func (h *hintingSink) decodePayload(ctx context.Context, tool tools.Ident, payload any) any {
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

	// Prefer tool-specific codecs for schema-aware decoding.
	val, err := h.rt.unmarshalToolValue(ctx, tool, raw, true)
	if err == nil {
		return val
	}
	return nil
}
