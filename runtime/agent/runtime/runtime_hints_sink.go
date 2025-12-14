package runtime

import (
	"context"
	"encoding/json"

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
		if data.DisplayHint == "" {
			if typed := h.decodePayload(ctx, tools.Ident(data.ToolName), data.Payload); typed != nil {
				if s := rthints.FormatCallHint(tools.Ident(data.ToolName), typed); s != "" {
					data.DisplayHint = s
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
// runtime's tool codecs. If decoding fails, it returns nil so callers do not
// compute best-effort hints from an untyped payload.
func (h *hintingSink) decodePayload(ctx context.Context, tool tools.Ident, payload any) any {
	if payload == nil {
		return nil
	}

	var raw json.RawMessage
	switch v := payload.(type) {
	case json.RawMessage:
		if len(v) == 0 {
			return nil
		}
		raw = v
	case []byte:
		if len(v) == 0 {
			return nil
		}
		raw = json.RawMessage(v)
	case string:
		if v == "" {
			return nil
		}
		b := []byte(v)
		if !json.Valid(b) {
			// Not JSON; treat as already-typed scalar.
			return v
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
