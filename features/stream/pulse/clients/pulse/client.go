// Package pulse provides a thin goa-ai specific wrapper around Pulse streams.
// It mirrors the layering used across existing deployments: callers build a
// Redis client, pass it to New, and receive a typed interface that exposes only
// the operations needed by the stream sink.
package pulse

//go:generate cmg gen .

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	// Options configures the Pulse client.
	Options struct {
		// Redis is the Redis connection used to back Pulse streams. Required.
		Redis *redis.Client
		// StreamMaxLen bounds the number of entries kept per stream. Zero uses Pulse defaults.
		StreamMaxLen int
		// StreamOptions returns additional stream options to apply when opening a stream.
		// It is invoked once per Stream call with the stream name.
		//
		// Returning nil means "no additional options".
		StreamOptions func(name string) []streamopts.Stream
		// OperationTimeout bounds individual Add operations. Zero means no timeout.
		OperationTimeout time.Duration
	}

	// Client exposes the subset of Pulse APIs required by the goa-ai stream sink.
	// Implementations wrap goa.design/pulse streaming and provide type-safe access
	// to stream operations.
	Client interface {
		// Stream returns a handle to the named Pulse stream, creating it if needed.
		Stream(name string, opts ...streamopts.Stream) (Stream, error)
		// Close releases resources owned by the client. Callers typically own the Redis
		// connection and may provide a no-op implementation.
		Close(ctx context.Context) error
	}

	// Stream exposes the operations needed to publish runtime events and create sinks
	// (consumer groups).
	Stream interface {
		// Add publishes an event with the given name and payload to the stream, returning
		// the event ID assigned by Redis (e.g., "1234567890-0").
		Add(ctx context.Context, event string, payload []byte) (string, error)
		// NewSink creates a Pulse sink (consumer group) on this stream for reading events.
		NewSink(ctx context.Context, name string, opts ...streamopts.Sink) (Sink, error)
		// Destroy deletes the entire stream and all its messages from Redis.
		Destroy(ctx context.Context) error
	}

	// Sink mirrors the subset of goa.design/pulse streaming sinks required by the subscriber.
	// It represents a consumer group that reads from a Pulse stream.
	Sink interface {
		// Subscribe returns a channel that emits events as they arrive from the stream.
		Subscribe() <-chan *streaming.Event
		// Ack acknowledges successful processing of an event, removing it from the pending list.
		Ack(context.Context, *streaming.Event) error
		// Close stops the sink and releases resources.
		Close(context.Context)
	}
)

// client wraps a Redis connection and provides stream access.
type client struct {
	redis        *redis.Client
	maxLen       int
	streamOptsFn func(name string) []streamopts.Stream
	timeout      time.Duration
}

// New constructs a Pulse client backed by the provided Redis connection. The
// Redis field in opts is required; other fields are optional. Returns an error
// if opts.Redis is nil.
func New(opts Options) (Client, error) {
	if opts.Redis == nil {
		return nil, errors.New("redis client is required")
	}
	return &client{
		redis:        opts.Redis,
		maxLen:       opts.StreamMaxLen,
		streamOptsFn: opts.StreamOptions,
		timeout:      opts.OperationTimeout,
	}, nil
}

// Stream returns a handle to the named Pulse stream, creating it if it doesn't
// exist. Returns an error if the name is empty or if stream creation fails.
func (c *client) Stream(name string, opts ...streamopts.Stream) (Stream, error) {
	if name == "" {
		return nil, errors.New("stream name is required")
	}
	var streamOptions []streamopts.Stream
	if c.maxLen > 0 {
		streamOptions = append(streamOptions, streamopts.WithStreamMaxLen(c.maxLen))
	}
	if c.streamOptsFn != nil {
		streamOptions = append(streamOptions, c.streamOptsFn(name)...)
	}
	streamOptions = append(streamOptions, opts...)
	str, err := streaming.NewStream(name, c.redis, streamOptions...)
	if err != nil {
		return nil, fmt.Errorf("create pulse stream: %w", err)
	}
	return &handle{stream: str, timeout: c.timeout}, nil
}

// Close is a no-op because the caller typically owns and manages the Redis
// connection lifecycle.
func (c *client) Close(ctx context.Context) error {
	return nil
}

// handle wraps a Pulse stream and applies optional timeouts to operations.
type handle struct {
	stream  *streaming.Stream
	timeout time.Duration
}

// Add publishes an event to the stream with an optional timeout. Returns the
// Redis-assigned event ID or an error if the event name is empty or the
// operation fails.
func (h *handle) Add(ctx context.Context, event string, payload []byte) (string, error) {
	if event == "" {
		return "", errors.New("event name is required")
	}
	if h.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, h.timeout)
		defer cancel()
	}
	id, err := h.stream.Add(ctx, event, payload)
	if err != nil {
		return "", fmt.Errorf("pulse add: %w", err)
	}
	return id, nil
}

// NewSink creates a consumer group on the stream. Delegates to the underlying
// Pulse stream and wraps the result in a sinkAdapter.
func (h *handle) NewSink(ctx context.Context, name string, opts ...streamopts.Sink) (Sink, error) {
	sink, err := h.stream.NewSink(ctx, name, opts...)
	if err != nil {
		return nil, err
	}
	return &sinkAdapter{Sink: sink}, nil
}

// Destroy deletes the entire stream and all its messages from Redis.
func (h *handle) Destroy(ctx context.Context) error {
	return h.stream.Destroy(ctx)
}

// sinkAdapter adapts streaming.Sink to the Sink interface, making Close match
// the expected signature (return void instead of error).
type sinkAdapter struct {
	*streaming.Sink
}

// Close delegates to the underlying Pulse sink.
func (s sinkAdapter) Close(ctx context.Context) {
	s.Sink.Close(ctx)
}
