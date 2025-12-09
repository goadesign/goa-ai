package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
)

type (
	// ResultStreamManager manages temporary Pulse streams for tool invocation results.
	// Each tool invocation creates a unique result stream that the gateway subscribes
	// to for receiving results from providers. Streams are cleaned up after receiving
	// a result or when the TTL expires.
	//
	// The manager stores tool_use_id to stream_id mappings in Redis to support
	// distributed deployments where EmitToolResult may be called on a different
	// gateway node than the one that created the result stream.
	ResultStreamManager interface {
		// CreateResultStream creates a temporary result stream for a tool invocation.
		// Returns the stream, a unique tool use ID, and the stream ID.
		// The stream is configured with a TTL for automatic cleanup.
		CreateResultStream(ctx context.Context) (clientspulse.Stream, string, string, error)

		// GetResultStream returns the result stream for a tool use ID if it exists.
		// This checks the local cache first, then falls back to Redis lookup.
		GetResultStream(ctx context.Context, toolUseID string) (clientspulse.Stream, error)

		// DestroyResultStream destroys the result stream for a tool use ID.
		// This should be called after receiving a result or on timeout.
		DestroyResultStream(ctx context.Context, toolUseID string) error

		// SetTTL sets the TTL on the result stream's Redis key.
		// This is called after stream creation to configure automatic expiry.
		SetTTL(ctx context.Context, toolUseID string, ttl time.Duration) error

		// WaitForResult subscribes to the result stream and waits for a result.
		// It returns the result message or an error if the timeout is reached.
		// The stream is destroyed after receiving a result or on timeout.
		WaitForResult(ctx context.Context, toolUseID string, opts WaitForResultOptions) (*ToolResultMessage, error)

		// PublishResult publishes a result to the result stream for a tool use ID.
		// This is called by the EmitToolResult method to deliver results from providers.
		// The method looks up the stream_id from Redis, enabling cross-node result delivery.
		PublishResult(ctx context.Context, toolUseID string, msg *ToolResultMessage) error
	}

	// ToolResultMessage represents a message published to result streams.
	ToolResultMessage struct {
		// ToolUseID is the unique identifier for the tool invocation.
		ToolUseID string `json:"tool_use_id"`
		// Result is the tool execution result (JSON-serializable).
		Result json.RawMessage `json:"result,omitempty"`
		// Error contains error details if execution failed.
		Error *ToolResultError `json:"error,omitempty"`
	}

	// ToolResultError represents an error from tool execution.
	ToolResultError struct {
		// Code is the error code.
		Code string `json:"code"`
		// Message is the error message.
		Message string `json:"message"`
	}

	// ResultStreamManagerOptions configures the result stream manager.
	ResultStreamManagerOptions struct {
		// Client is the Pulse client for creating streams.
		Client clientspulse.Client
		// Redis is the Redis client for storing mappings and setting TTL.
		Redis *redis.Client
		// MappingTTL is the TTL for tool_use_id to stream_id mappings in Redis.
		// Defaults to 5 minutes if not specified.
		MappingTTL time.Duration
	}

	// resultStreamManager is the default implementation of ResultStreamManager.
	resultStreamManager struct {
		client     clientspulse.Client
		rdb        *redis.Client
		mappingTTL time.Duration
		mu         sync.RWMutex
		streams    map[string]clientspulse.Stream // local cache keyed by tool_use_id
	}
)

// DefaultMappingTTL is the default TTL for tool_use_id to stream_id mappings.
const DefaultMappingTTL = 5 * time.Minute

// NewResultStreamManager creates a new ResultStreamManager.
func NewResultStreamManager(opts ResultStreamManagerOptions) (ResultStreamManager, error) {
	if opts.Client == nil {
		return nil, fmt.Errorf("pulse client is required")
	}
	if opts.Redis == nil {
		return nil, fmt.Errorf("redis client is required")
	}
	mappingTTL := opts.MappingTTL
	if mappingTTL == 0 {
		mappingTTL = DefaultMappingTTL
	}
	return &resultStreamManager{
		client:     opts.Client,
		rdb:        opts.Redis,
		mappingTTL: mappingTTL,
		streams:    make(map[string]clientspulse.Stream),
	}, nil
}

// resultStreamIDForToolUse returns the stream ID for a tool use ID.
func resultStreamIDForToolUse(toolUseID string) string {
	return fmt.Sprintf("result:%s", toolUseID)
}

// redisKeyForMapping returns the Redis key for storing tool_use_id to stream_id mappings.
func redisKeyForMapping(toolUseID string) string {
	return fmt.Sprintf("registry:result-stream:%s", toolUseID)
}

// redisKeyForStream returns the Redis key for a Pulse stream.
// Pulse uses the prefix "pulse:stream:" for stream keys.
func redisKeyForStream(streamID string) string {
	return fmt.Sprintf("pulse:stream:%s", streamID)
}

// CreateResultStream creates a temporary result stream for a tool invocation.
func (m *resultStreamManager) CreateResultStream(ctx context.Context) (clientspulse.Stream, string, string, error) {
	toolUseID := uuid.New().String()
	streamID := resultStreamIDForToolUse(toolUseID)

	stream, err := m.client.Stream(streamID)
	if err != nil {
		return nil, "", "", fmt.Errorf("create result stream: %w", err)
	}

	// Store the mapping in Redis for cross-node lookup.
	mappingKey := redisKeyForMapping(toolUseID)
	if err := m.rdb.Set(ctx, mappingKey, streamID, m.mappingTTL).Err(); err != nil {
		return nil, "", "", fmt.Errorf("store result stream mapping: %w", err)
	}

	// Cache locally for the waiting gateway node.
	m.mu.Lock()
	m.streams[toolUseID] = stream
	m.mu.Unlock()

	return stream, toolUseID, streamID, nil
}

// GetResultStream returns the result stream for a tool use ID if it exists.
// This checks the local cache first, then falls back to Redis lookup for
// cross-node result delivery.
func (m *resultStreamManager) GetResultStream(ctx context.Context, toolUseID string) (clientspulse.Stream, error) {
	// Fast path: check local cache.
	m.mu.RLock()
	if stream, ok := m.streams[toolUseID]; ok {
		m.mu.RUnlock()
		return stream, nil
	}
	m.mu.RUnlock()

	// Slow path: look up stream_id from Redis and create a stream handle.
	mappingKey := redisKeyForMapping(toolUseID)
	streamID, err := m.rdb.Get(ctx, mappingKey).Result()
	if errors.Is(err, redis.Nil) {
		return nil, ErrResultStreamNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("lookup result stream mapping: %w", err)
	}

	// Create a stream handle for publishing.
	stream, err := m.client.Stream(streamID)
	if err != nil {
		return nil, fmt.Errorf("get result stream: %w", err)
	}
	return stream, nil
}

// ErrResultStreamNotFound is returned when no result stream exists for a tool use ID.
var ErrResultStreamNotFound = fmt.Errorf("no result stream for tool use ID")

// DestroyResultStream destroys the result stream for a tool use ID.
func (m *resultStreamManager) DestroyResultStream(ctx context.Context, toolUseID string) error {
	// Remove from local cache.
	m.mu.Lock()
	stream, ok := m.streams[toolUseID]
	delete(m.streams, toolUseID)
	m.mu.Unlock()

	// Delete the mapping from Redis.
	// Errors are ignored - we still want to destroy the stream.
	mappingKey := redisKeyForMapping(toolUseID)
	_ = m.rdb.Del(ctx, mappingKey).Err()

	// If we have a local stream handle, use it to destroy.
	if ok {
		if err := stream.Destroy(ctx); err != nil {
			return fmt.Errorf("destroy result stream: %w", err)
		}
		return nil
	}

	// Otherwise, look up the stream_id and destroy.
	streamID := resultStreamIDForToolUse(toolUseID)
	stream, err := m.client.Stream(streamID)
	if err != nil {
		// Stream may already be destroyed or expired.
		return fmt.Errorf("get stream for destroy: %w", err)
	}
	if err := stream.Destroy(ctx); err != nil {
		return fmt.Errorf("destroy result stream: %w", err)
	}
	return nil
}

// SetTTL sets the TTL on the result stream's Redis key.
func (m *resultStreamManager) SetTTL(ctx context.Context, toolUseID string, ttl time.Duration) error {
	streamID := resultStreamIDForToolUse(toolUseID)
	key := redisKeyForStream(streamID)
	if err := m.rdb.Expire(ctx, key, ttl).Err(); err != nil {
		return fmt.Errorf("set TTL on result stream: %w", err)
	}
	return nil
}

// MessageTypeResult is the message type for tool results.
const MessageTypeResult = "result"

// NewToolResultMessage creates a ToolResultMessage for a successful result.
func NewToolResultMessage(toolUseID string, result json.RawMessage) *ToolResultMessage {
	return &ToolResultMessage{
		ToolUseID: toolUseID,
		Result:    result,
	}
}

// NewToolResultErrorMessage creates a ToolResultMessage for an error result.
func NewToolResultErrorMessage(toolUseID, code, message string) *ToolResultMessage {
	return &ToolResultMessage{
		ToolUseID: toolUseID,
		Error: &ToolResultError{
			Code:    code,
			Message: message,
		},
	}
}

// WaitForResultOptions configures the WaitForResult operation.
type WaitForResultOptions struct {
	// Timeout is the maximum time to wait for a result.
	Timeout time.Duration
	// SinkName is the name of the sink to create for subscribing.
	// Defaults to "gateway" if not specified.
	SinkName string
}

// WaitForResult subscribes to the result stream and waits for a result.
// It returns the result message or an error if the timeout is reached.
// The stream is destroyed after receiving a result or on timeout.
func (m *resultStreamManager) WaitForResult(ctx context.Context, toolUseID string, opts WaitForResultOptions) (*ToolResultMessage, error) {
	stream, err := m.GetResultStream(ctx, toolUseID)
	if err != nil {
		return nil, fmt.Errorf("get result stream: %w", err)
	}

	sinkName := opts.SinkName
	if sinkName == "" {
		sinkName = "gateway"
	}

	// Create a sink to subscribe to the result stream.
	sink, err := stream.NewSink(ctx, sinkName)
	if err != nil {
		return nil, fmt.Errorf("create sink for result stream: %w", err)
	}
	defer sink.Close(ctx)

	// Set up timeout context.
	timeout := opts.Timeout
	if timeout == 0 {
		timeout = 30 * time.Second // Default timeout
	}
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Subscribe and wait for result.
	events := sink.Subscribe()
	for {
		select {
		case <-timeoutCtx.Done():
			// Cleanup on timeout. Errors are ignored - the TTL will clean up eventually.
			_ = m.DestroyResultStream(ctx, toolUseID)
			if errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
				return nil, ErrTimeout
			}
			return nil, timeoutCtx.Err()

		case event, ok := <-events:
			if !ok {
				// Channel closed unexpectedly.
				return nil, fmt.Errorf("result stream closed unexpectedly")
			}

			// Parse the result message.
			var msg ToolResultMessage
			if err := json.Unmarshal(event.Payload, &msg); err != nil {
				// Ack the malformed event and continue waiting.
				// Errors are ignored - we continue waiting for valid results.
				_ = sink.Ack(ctx, event)
				continue
			}

			// Verify this is the result we're waiting for.
			if msg.ToolUseID != toolUseID {
				// Not our result, ack and continue.
				// Errors are ignored - we continue waiting for our result.
				_ = sink.Ack(ctx, event)
				continue
			}

			// Ack the event. Errors are ignored - we have the result.
			_ = sink.Ack(ctx, event)

			// Cleanup the stream immediately on success.
			// Errors are ignored - the TTL will clean up eventually.
			_ = m.DestroyResultStream(ctx, toolUseID)

			return &msg, nil
		}
	}
}

// ErrTimeout is returned when waiting for a result times out.
var ErrTimeout = fmt.Errorf("timeout waiting for tool result")

// PublishResult publishes a result to the result stream for a tool use ID.
// This is called by the EmitToolResult method to deliver results from providers.
// The method looks up the stream_id from Redis, enabling cross-node result delivery.
func (m *resultStreamManager) PublishResult(ctx context.Context, toolUseID string, msg *ToolResultMessage) error {
	stream, err := m.GetResultStream(ctx, toolUseID)
	if err != nil {
		return fmt.Errorf("get result stream: %w", err)
	}

	payload, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal result message: %w", err)
	}

	_, err = stream.Add(ctx, MessageTypeResult, payload)
	if err != nil {
		return fmt.Errorf("publish result to stream: %w", err)
	}
	return nil
}
