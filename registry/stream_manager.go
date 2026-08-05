// Package registry provides the internal tool registry service implementation.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/runtime/toolregistry"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// StreamManager manages Pulse streams for toolset communication.
// It creates and tracks streams for each registered toolset, enabling
// tool request routing and result delivery.
type StreamManager interface {
	// GetOrCreateStream returns the stream for a toolset, creating it if needed.
	// The stream ID is deterministic based on the toolset name.
	GetOrCreateStream(ctx context.Context, toolset string) (clientspulse.Stream, string, error)

	// PublishToolCall publishes a tool call message to the toolset's stream.
	PublishToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage) error

	// PublishAdmittedToolCall atomically publishes one exact initial or overload
	// attempt and commits it in the immutable call admission.
	PublishAdmittedToolCall(
		ctx context.Context,
		toolset string,
		msg toolregistry.ToolCallMessage,
		admission callAdmission,
		overloadEventID string,
	) error
}

// streamManager is the default implementation of StreamManager.
type streamManager struct {
	client  clientspulse.Client
	rdb     *redis.Client
	mu      sync.RWMutex
	streams map[string]clientspulse.Stream
}

// NewStreamManager creates a new StreamManager backed by the given Pulse
// client. The raw Redis client backs the atomic bounded publication that the
// Pulse stream API cannot express.
func NewStreamManager(client clientspulse.Client, rdb *redis.Client) StreamManager {
	return &streamManager{
		client:  client,
		rdb:     rdb,
		streams: make(map[string]clientspulse.Stream),
	}
}

// GetOrCreateStream returns the stream for a toolset, creating it if needed.
func (m *streamManager) GetOrCreateStream(ctx context.Context, toolset string) (clientspulse.Stream, string, error) {
	streamID := toolregistry.ToolsetStreamID(toolset)

	// Fast path: check if stream already exists.
	m.mu.RLock()
	if stream, ok := m.streams[toolset]; ok {
		m.mu.RUnlock()
		return stream, streamID, nil
	}
	m.mu.RUnlock()

	// Slow path: create stream under write lock.
	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check after acquiring write lock.
	if stream, ok := m.streams[toolset]; ok {
		return stream, streamID, nil
	}

	stream, err := m.client.Stream(streamID)
	if err != nil {
		return nil, "", fmt.Errorf("create stream for toolset %q: %w", toolset, err)
	}
	m.streams[toolset] = stream
	return stream, streamID, nil
}

// PublishToolCall publishes a tool call message to the toolset's stream.
// It lazily creates a local stream handle if one doesn't exist, enabling
// cross-node tool invocation where the toolset was registered on a different node.
func (m *streamManager) PublishToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage) error {
	return m.publishToolCall(ctx, toolset, msg, nil, "")
}

// PublishAdmittedToolCall publishes one exact call request at the same Redis
// linearization point that records its initial or overload publication.
func (m *streamManager) PublishAdmittedToolCall(
	ctx context.Context,
	toolset string,
	msg toolregistry.ToolCallMessage,
	admission callAdmission,
	overloadEventID string,
) error {
	if msg.Type != toolregistry.MessageTypeCall {
		return fmt.Errorf("admitted publication requires a call message")
	}
	return m.publishToolCall(ctx, toolset, msg, &admission, overloadEventID)
}

// publishToolCall validates and encodes one toolset message before selecting
// ordinary or call-admission-owned publication.
func (m *streamManager) publishToolCall(
	ctx context.Context,
	toolset string,
	msg toolregistry.ToolCallMessage,
	admission *callAdmission,
	overloadEventID string,
) error {
	if err := toolregistry.ValidateToolCallMessage(msg); err != nil {
		return fmt.Errorf("publish invalid toolset message: %w", err)
	}
	streamID := toolregistry.ToolsetStreamID(toolset)

	if msg.Type == toolregistry.MessageTypeCall {
		tracer := otel.Tracer("goa.design/goa-ai/registry")
		var span trace.Span
		ctx, span = tracer.Start(
			ctx,
			"toolregistry.publish",
			trace.WithSpanKind(trace.SpanKindProducer),
			trace.WithAttributes(
				attribute.String("messaging.system", "pulse"),
				attribute.String("messaging.destination.name", streamID),
				attribute.String("messaging.operation", "publish"),
				attribute.String("toolregistry.toolset", toolset),
				attribute.String("toolregistry.tool_use_id", msg.ToolUseID),
				attribute.String("toolregistry.tool", msg.Tool.String()),
				attribute.String("toolregistry.stream_id", streamID),
			),
		)
		defer span.End()

		msg.TraceParent, msg.TraceState, msg.Baggage = toolregistry.InjectTraceContext(ctx)
		if msg.TraceParent != "" {
			span.SetAttributes(attribute.Bool("toolregistry.trace_injected", true))
		}
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		if msg.Type == toolregistry.MessageTypeCall {
			span := trace.SpanFromContext(ctx)
			span.RecordError(err)
			span.SetStatus(codes.Error, "marshal tool call message")
		}
		return fmt.Errorf("marshal tool call message: %w", err)
	}

	// Calls are backlog-bounded and retention trims only below the earliest
	// consumer-group PEL entry. Pings bypass the bound because health must flow
	// while calls are queued.
	bound := 0
	if msg.Type == toolregistry.MessageTypeCall {
		bound = maxQueuedToolCalls
	}
	var eventID string
	if admission == nil {
		eventID, err = publishBounded(ctx, m.rdb, streamID, bound, string(msg.Type), payload)
	} else {
		eventID, err = publishAdmittedBounded(
			ctx,
			m.rdb,
			streamID,
			bound,
			string(msg.Type),
			payload,
			*admission,
			overloadEventID,
		)
	}
	if err != nil {
		if msg.Type == toolregistry.MessageTypeCall {
			span := trace.SpanFromContext(ctx)
			span.RecordError(err)
			span.SetStatus(codes.Error, "publish to stream")
		}
		return fmt.Errorf("publish to stream: %w", err)
	}
	if msg.Type == toolregistry.MessageTypeCall {
		trace.SpanFromContext(ctx).AddEvent(
			"toolregistry.tool_call_published",
			trace.WithAttributes(attribute.String("toolregistry.event_id", eventID)),
		)
	}
	return nil
}
