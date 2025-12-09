// Package registry provides the internal tool registry service implementation.
package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
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
	PublishToolCall(ctx context.Context, toolset string, msg *ToolCallMessage) error
}

// ToolCallMessage represents a message published to toolset streams.
type ToolCallMessage struct {
	// Type is the message type: "call" or "ping".
	Type string `json:"type"`
	// ToolUseID is the unique identifier for tool invocations.
	ToolUseID string `json:"tool_use_id,omitempty"`
	// PingID is the unique identifier for health check pings.
	PingID string `json:"ping_id,omitempty"`
	// Tool is the name of the tool to invoke.
	Tool string `json:"tool,omitempty"`
	// Payload is the tool input payload.
	Payload json.RawMessage `json:"payload,omitempty"`
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
func (m *streamManager) PublishToolCall(ctx context.Context, toolset string, msg *ToolCallMessage) error {
	// Use GetOrCreateStream to handle cross-node scenarios where the toolset
	// was registered on a different gateway node.
	stream, _, err := m.GetOrCreateStream(ctx, toolset)
	if err != nil {
		return fmt.Errorf("get stream for toolset %q: %w", toolset, err)
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal tool call message: %w", err)
	}

	_, err = stream.Add(ctx, msg.Type, payload)
	if err != nil {
		return fmt.Errorf("publish to stream: %w", err)
	}
	return nil
}

// MessageTypeCall is the message type for tool invocations.
const MessageTypeCall = "call"

// MessageTypePing is the message type for health check pings.
const MessageTypePing = "ping"

// NewToolCallMessage creates a ToolCallMessage for invoking a tool.
// The toolUseID uniquely identifies this invocation for result correlation.
// The tool parameter specifies which tool to invoke within the toolset.
// The payload contains the tool input parameters as JSON.
func NewToolCallMessage(toolUseID, tool string, payload json.RawMessage) *ToolCallMessage {
	return &ToolCallMessage{
		Type:      MessageTypeCall,
		ToolUseID: toolUseID,
		Tool:      tool,
		Payload:   payload,
	}
}

// NewPingMessage creates a ToolCallMessage for health check pings.
// The pingID uniquely identifies this ping for pong correlation.
func NewPingMessage(pingID string) *ToolCallMessage {
	return &ToolCallMessage{
		Type:   MessageTypePing,
		PingID: pingID,
	}
}
