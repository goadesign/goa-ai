// Package pulse exposes a stream.Sink implementation that publishes runtime
// events to goa.design/pulse streams. It mirrors the layering used by existing
// Pulse deployments: services build a Redis client, pass it to the Pulse client,
// and hand the resulting sink to the runtime.
package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/stream"
)

type (
	// Options configures the Pulse sink.
	Options struct {
		// Client is the Pulse client used to publish events. Required.
		Client pulse.Client
		// StreamID derives the target Pulse stream from an event. Defaults to
		// `session/<SessionID>`.
		StreamID func(stream.Event) (string, error)
		// MarshalEnvelope allows overriding the envelope serialization (primarily for tests).
		MarshalEnvelope func(Envelope) ([]byte, error)
		// OnPublished, when set, is invoked after an event has been successfully
		// written to the underlying Pulse stream. If it returns an error, Send
		// fails and callers should treat the event as not fully emitted.
		OnPublished func(context.Context, PublishedEvent) error
	}

	// Sink publishes runtime Event values into Pulse streams. It delegates
	// serialization to the configured envelope marshaler.
	// Thread-safe for concurrent Send operations.
	Sink struct {
		client pulse.Client
		opts   sinkOptions
	}

	// sinkOptions holds internal configuration derived from Options.
	sinkOptions struct {
		streamID        func(stream.Event) (string, error)
		marshalEnvelope func(Envelope) ([]byte, error)
		onPublished     func(context.Context, PublishedEvent) error
	}

	// Envelope wraps runtime events for transmission over Pulse streams.
	// It adds metadata and serializes the event content as JSON.
	//
	// Envelope is part of the sink's public configuration surface to support
	// callers that need to customize JSON serialization (e.g., for tests or
	// transport interop). The sink always publishes an envelope as the value
	// stored in Pulse; only the marshaler is customizable.
	Envelope struct {
		// Type identifies the event kind (e.g., "tool_end", "assistant_reply").
		Type string `json:"type"`
		// RunID links the event to a specific workflow execution.
		RunID string `json:"run_id"`
		// SessionID links the event to the logical session that owns the run.
		SessionID string `json:"session_id,omitempty"`
		// Timestamp records when the event was published (UTC).
		Timestamp time.Time `json:"timestamp"`
		// Payload contains the event-specific data, if any.
		Payload any `json:"payload,omitempty"`
		// ServerData carries server-only metadata for events that support it
		// (currently `tool_end`). It is never forwarded to model providers, but
		// downstream subscribers (e.g., persistence drains) may consume it.
		ServerData json.RawMessage `json:"server_data,omitempty"`
	}

	// PublishedEvent describes a runtime event that has been successfully
	// written to a Pulse stream. It carries the original event together with
	// the concrete stream name and the Redis-assigned entry ID.
	PublishedEvent struct {
		Event    stream.Event
		StreamID string
		EntryID  string
	}
)

// NewSink constructs a Pulse-backed stream sink. The Client field in opts is
// required; StreamID and MarshalEnvelope default to the built-in implementations
// if not provided.
func NewSink(opts Options) (*Sink, error) {
	if opts.Client == nil {
		return nil, errors.New("pulse client is required")
	}
	cfg := sinkOptions{
		streamID:        defaultStreamID,
		marshalEnvelope: defaultMarshal,
		onPublished:     opts.OnPublished,
	}
	if opts.StreamID != nil {
		cfg.streamID = opts.StreamID
	}
	if opts.MarshalEnvelope != nil {
		cfg.marshalEnvelope = opts.MarshalEnvelope
	}
	return &Sink{
		client: opts.Client,
		opts:   cfg,
	}, nil
}

// Send publishes the event to the derived Pulse stream. It derives the stream ID,
// wraps the event in an envelope, marshals it to JSON, and publishes it via the
// Pulse client. Thread-safe for concurrent calls.
func (s *Sink) Send(ctx context.Context, event stream.Event) error {
	streamID, err := s.opts.streamID(event)
	if err != nil {
		return err
	}
	handle, err := s.client.Stream(streamID)
	if err != nil {
		return err
	}
	env := Envelope{
		Type:      string(event.Type()),
		RunID:     event.RunID(),
		SessionID: event.SessionID(),
		Timestamp: time.Now().UTC(),
		Payload:   event.Payload(),
	}
	switch ev := event.(type) {
	case stream.ToolEnd:
		env.ServerData = ev.Data.ServerData
		payload := ev.Data
		payload.ServerData = nil
		env.Payload = payload
	case *stream.ToolEnd:
		env.ServerData = ev.Data.ServerData
		payload := ev.Data
		payload.ServerData = nil
		env.Payload = payload
	}
	payload, err := s.opts.marshalEnvelope(env)
	if err != nil {
		return err
	}
	entryID, err := handle.Add(ctx, env.Type, payload)
	if err != nil {
		return err
	}
	if cb := s.opts.onPublished; cb != nil {
		return cb(ctx, PublishedEvent{
			Event:    event,
			StreamID: streamID,
			EntryID:  entryID,
		})
	}
	return nil
}

// Close releases resources owned by the sink. This delegates to the underlying
// Pulse client, which may or may not close the Redis connection depending on
// the client implementation.
func (s *Sink) Close(ctx context.Context) error {
	return s.client.Close(ctx)
}

// defaultStreamID derives the Pulse stream name from the event's SessionID.
// Returns an error if the SessionID is empty.
func defaultStreamID(event stream.Event) (string, error) {
	if event.SessionID() == "" {
		return "", errors.New("stream event missing session id")
	}
	return fmt.Sprintf("session/%s", event.SessionID()), nil
}

// defaultMarshal serializes an envelope to JSON.
func defaultMarshal(env Envelope) ([]byte, error) {
	return json.Marshal(env)
}
