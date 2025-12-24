// Package registry provides the internal tool registry service implementation.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

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

	// GetStream returns the stream for a toolset if it exists.
	// Returns nil if the toolset has no associated stream.
	GetStream(toolset string) clientspulse.Stream

	// RemoveStream removes the stream tracking for a toolset.
	// This does not destroy the underlying Pulse stream.
	RemoveStream(toolset string)

	// PublishToolCall publishes a tool call message to the toolset's stream.
	PublishToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage) error
}

// streamManager is the default implementation of StreamManager.
type streamManager struct {
	client  clientspulse.Client
	mu      sync.RWMutex
	streams map[string]clientspulse.Stream
}

// NewStreamManager creates a new StreamManager backed by the given Pulse client.
func NewStreamManager(client clientspulse.Client) StreamManager {
	return &streamManager{
		client:  client,
		streams: make(map[string]clientspulse.Stream),
	}
}

// streamIDForToolset returns the deterministic stream ID for a toolset.
func streamIDForToolset(toolset string) string {
	return fmt.Sprintf("toolset:%s:requests", toolset)
}

// GetOrCreateStream returns the stream for a toolset, creating it if needed.
func (m *streamManager) GetOrCreateStream(ctx context.Context, toolset string) (clientspulse.Stream, string, error) {
	streamID := streamIDForToolset(toolset)

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

// GetStream returns the stream for a toolset if it exists.
func (m *streamManager) GetStream(toolset string) clientspulse.Stream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.streams[toolset]
}

// RemoveStream removes the stream tracking for a toolset.
func (m *streamManager) RemoveStream(toolset string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.streams, toolset)
}

// PublishToolCall publishes a tool call message to the toolset's stream.
// It lazily creates a local stream handle if one doesn't exist, enabling
// cross-node tool invocation where the toolset was registered on a different node.
func (m *streamManager) PublishToolCall(ctx context.Context, toolset string, msg toolregistry.ToolCallMessage) error {
	// Use GetOrCreateStream to handle cross-node scenarios where the toolset
	// was registered on a different gateway node.
	stream, streamID, err := m.GetOrCreateStream(ctx, toolset)
	if err != nil {
		return fmt.Errorf("get stream for toolset %q: %w", toolset, err)
	}

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

	eventID, err := stream.Add(ctx, string(msg.Type), payload)
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
