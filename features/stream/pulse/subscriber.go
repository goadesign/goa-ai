package pulse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	streamopts "goa.design/pulse/streaming/options"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/agent/stream"
)

type (
	// EnvelopeDecoder converts raw payloads read from Pulse into runtime stream events.
	// Custom decoders can be provided to handle non-standard envelope formats.
	EnvelopeDecoder func([]byte) (stream.Event, error)

	// SubscriberOptions configures a Pulse-backed subscriber.
	SubscriberOptions struct {
		// Client is the Pulse client used to consume events. Required.
		Client clientspulse.Client
		// SinkName identifies the Pulse consumer group. Defaults to "goa_ai_subscriber".
		SinkName string
		// Buffer specifies the event channel capacity. Defaults to 64.
		Buffer int
		// Decoder deserializes event payloads. Defaults to the built-in JSON decoder.
		Decoder EnvelopeDecoder
	}

	// Subscriber consumes Pulse streams and emits runtime stream events. It wraps
	// a Pulse sink (consumer group) and decodes incoming payloads into stream.Event
	// values.
	Subscriber struct {
		client clientspulse.Client
		buffer int
		name   string
		decode EnvelopeDecoder
	}
	// decodedEvent implements stream.Event for Pulse-decoded envelopes.
	decodedEvent struct {
		t   stream.EventType
		run string
		s   string
		b   json.RawMessage
	}
)

func (e decodedEvent) Type() stream.EventType { return e.t }
func (e decodedEvent) RunID() string          { return e.run }
func (e decodedEvent) SessionID() string      { return e.s }
func (e decodedEvent) Payload() any           { return e.b }

// NewSubscriber constructs a Pulse-backed subscriber. The Client field in opts
// is required; SinkName, Buffer, and Decoder default to sensible values if not
// provided (see SubscriberOptions field documentation).
func NewSubscriber(opts SubscriberOptions) (*Subscriber, error) {
	if opts.Client == nil {
		return nil, errors.New("pulse client is required")
	}
	name := opts.SinkName
	if name == "" {
		name = "goa_ai_subscriber"
	}
	buffer := opts.Buffer
	if buffer <= 0 {
		buffer = 64
	}
	decoder := opts.Decoder
	if decoder == nil {
		decoder = decodeEnvelope
	}
	return &Subscriber{
		client: opts.Client,
		buffer: buffer,
		name:   name,
		decode: decoder,
	}, nil
}

// Subscribe opens a Pulse sink on the given stream ID and returns channels for
// events and errors. It spawns a goroutine that consumes from the sink, decodes
// payloads, and emits stream events. The returned cancel function stops
// consumption, closes the sink, and closes both channels.
//
// Usage:
//
//	events, errs, cancel, err := sub.Subscribe(ctx, "run/abc123")
//	defer cancel()
//	for evt := range events {
//	    // process event
//	}
func (s *Subscriber) Subscribe(
	ctx context.Context,
	streamID string,
	opts ...streamopts.Sink,
) (<-chan stream.Event, <-chan error, context.CancelFunc, error) {
	str, err := s.client.Stream(streamID)
	if err != nil {
		return nil, nil, nil, err
	}
	sink, err := str.NewSink(ctx, s.name, opts...)
	if err != nil {
		return nil, nil, nil, err
	}
	events := make(chan stream.Event, s.buffer)
	errs := make(chan error, 1)
	runCtx, cancel := context.WithCancel(ctx)
	go s.consume(runCtx, sink, events, errs)
	cancelFunc := func() {
		cancel()
		sink.Close(context.Background())
	}
	return events, errs, cancelFunc, nil
}

// consume reads events from the Pulse sink channel, decodes them, and emits them
// on the out channel. It acks each event after successful emission. Closes both
// channels when ctx is canceled or when the sink channel closes. Sends errors
// on the errs channel if decoding or acking fails, then returns.
func (s *Subscriber) consume(ctx context.Context, sink clientspulse.Sink, out chan<- stream.Event, errs chan<- error) {
	defer close(out)
	defer close(errs)
	ch := sink.Subscribe()
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-ch:
			if !ok {
				return
			}
			decoded, err := s.decode(evt.Payload)
			if err != nil {
				errs <- fmt.Errorf("pulse decode payload: %w", err)
				return
			}
			select {
			case out <- decoded:
			case <-ctx.Done():
				return
			}
			if ackErr := sink.Ack(ctx, evt); ackErr != nil {
				errs <- fmt.Errorf("pulse ack: %w", ackErr)
				return
			}
		}
	}
}

// decodeEnvelope deserializes the default JSON envelope format and extracts the
// runtime stream event. Returns an error if the payload is malformed.
func decodeEnvelope(payload []byte) (stream.Event, error) {
	var env struct {
		Type      string          `json:"type"`
		RunID     string          `json:"run_id"`
		SessionID string          `json:"session_id"`
		Timestamp time.Time       `json:"timestamp"`
		Payload   json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, err
	}
	return decodedEvent{
		t:   stream.EventType(env.Type),
		run: env.RunID,
		s:   env.SessionID,
		b:   env.Payload,
	}, nil
}
