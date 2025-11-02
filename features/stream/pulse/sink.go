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
		// StreamID derives the target Pulse stream from an event. Defaults to `run/<RunID>`.
		StreamID func(stream.Event) (string, error)
		// MarshalEnvelope allows overriding the envelope serialization (primarily for tests).
		MarshalEnvelope func(envelope) ([]byte, error)
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
		marshalEnvelope func(envelope) ([]byte, error)
	}

	// envelope wraps runtime events for transmission over Pulse streams.
	// It adds metadata and serializes the event content as JSON.
	envelope struct {
		// Type identifies the event kind (e.g., "tool_end", "assistant_reply").
		Type string `json:"type"`
		// RunID links the event to a specific workflow execution.
		RunID string `json:"run_id"`
		// Timestamp records when the event was published (UTC).
		Timestamp time.Time `json:"timestamp"`
		// Payload contains the event-specific data, if any.
		Payload any `json:"payload,omitempty"`
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
	env := envelope{
		Type:      string(event.Type()),
		RunID:     event.RunID(),
		Timestamp: time.Now().UTC(),
		Payload:   event.Payload(),
	}
	payload, err := s.opts.marshalEnvelope(env)
	if err != nil {
		return err
	}
	if _, err := handle.Add(ctx, env.Type, payload); err != nil {
		return err
	}
	return nil
}

// Close releases resources owned by the sink. This delegates to the underlying
// Pulse client, which may or may not close the Redis connection depending on
// the client implementation.
func (s *Sink) Close(ctx context.Context) error {
	return s.client.Close(ctx)
}

// defaultStreamID derives the Pulse stream name from the event's RunID.
// Returns an error if the RunID is empty.
func defaultStreamID(event stream.Event) (string, error) {
	if event.RunID() == "" {
		return "", errors.New("stream event missing run id")
	}
	return fmt.Sprintf("run/%s", event.RunID()), nil
}

// defaultMarshal serializes an envelope to JSON.
func defaultMarshal(env envelope) ([]byte, error) {
	return json.Marshal(env)
}
