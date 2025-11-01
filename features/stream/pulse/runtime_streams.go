package pulse

import (
	"context"
	"errors"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/stream"
)

// RuntimeStreams wires a caller-provided Pulse client into goa-ai's runtime.
// It owns a publishing sink (used by runtime.Options.Stream) and can spawn
// subscribers that reuse the same client so services do not need to manage
// multiple Pulse connections.
type RuntimeStreams struct {
	sink   *Sink
	client clientspulse.Client
}

// RuntimeStreamsOptions configures the helper returned by NewRuntimeStreams.
type RuntimeStreamsOptions struct {
	// Client is the Pulse client used for both publishing and subscribing. It is
	// required and typically built via features/stream/pulse/clients/pulse.
	Client clientspulse.Client
	// Sink holds optional overrides for the publishing sink (stream ID derivation,
	// marshaling). Leave zero-valued for defaults.
	Sink Options
}

// NewRuntimeStreams constructs helpers for publishing runtime hook events to
// Pulse and subscribing to the resulting streams. Callers pass the returned
// sink to runtime.Options.Stream and keep the helper around to create
// subscribers (e.g., SSE fan-out) later on.
func NewRuntimeStreams(opts RuntimeStreamsOptions) (*RuntimeStreams, error) {
	if opts.Client == nil {
		return nil, errors.New("pulse client is required")
	}
	sinkOpts := opts.Sink
	sinkOpts.Client = opts.Client
	sink, err := NewSink(sinkOpts)
	if err != nil {
		return nil, err
	}
	return &RuntimeStreams{sink: sink, client: opts.Client}, nil
}

// Sink exposes the publishing sink so callers can pass it to runtime.Options.
func (r *RuntimeStreams) Sink() stream.Sink {
	return r.sink
}

// NewSubscriber constructs a Pulse-backed subscriber that reuses the helper's
// client. This keeps stream publishing and consumption on the same Redis
// connection pool for efficiency.
func (r *RuntimeStreams) NewSubscriber(opts SubscriberOptions) (*Subscriber, error) {
	opts.Client = r.client
	return NewSubscriber(opts)
}

// Close shuts down the publishing sink (and therefore the underlying Pulse
// client). Call this during service shutdown after all subscribers have been
// canceled.
func (r *RuntimeStreams) Close(ctx context.Context) error {
	return r.sink.Close(ctx)
}
