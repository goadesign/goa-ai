// Package registry provides the internal tool registry service implementation.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/santhosh-tekuri/jsonschema/v6"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/registry/store"
	"goa.design/goa-ai/registry/store/memory"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
)

type (
	// Service implements the registry service interface.
	// It provides toolset registration, discovery, and tool invocation capabilities.
	Service struct {
		store         store.Store
		streamManager StreamManager
		healthTracker HealthTracker

		pulseClient       clientspulse.Client
		rdb               *redis.Client
		resultStreamTTL   time.Duration
	}

	// ServiceOptions configures the registry service.
	ServiceOptions struct {
		// Store is the persistence layer for toolset metadata.
		// Defaults to an in-memory store if not provided.
		Store store.Store
		// StreamManager manages Pulse streams for toolset communication.
		StreamManager StreamManager
		// HealthTracker tracks provider health status.
		HealthTracker HealthTracker
		// PulseClient creates/opens Pulse streams. Required for CallTool.
		PulseClient clientspulse.Client
		// Redis is used to set TTLs on result streams. Required for CallTool.
		Redis *redis.Client
		// ResultStreamTTL controls how long result streams live in Redis.
		// When zero, defaults to 15 minutes.
		ResultStreamTTL time.Duration
	}
)

// Compile-time check that Service implements the generated interface.
var _ genregistry.Service = (*Service)(nil)

// NewService creates a new registry service with the given options.
func NewService(opts ServiceOptions) (*Service, error) {
	st := opts.Store
	if st == nil {
		st = memory.New()
	}
	if opts.StreamManager == nil {
		return nil, fmt.Errorf("stream manager is required")
	}
	if opts.HealthTracker == nil {
		return nil, fmt.Errorf("health tracker is required")
	}
	if opts.PulseClient == nil {
		return nil, fmt.Errorf("pulse client is required")
	}
	if opts.Redis == nil {
		return nil, fmt.Errorf("redis client is required")
	}
	ttl := opts.ResultStreamTTL
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	return &Service{
		store:           st,
		streamManager:   opts.StreamManager,
		healthTracker:   opts.HealthTracker,
		pulseClient:     opts.PulseClient,
		rdb:             opts.Redis,
		resultStreamTTL: ttl,
	}, nil
}

// Register registers a toolset with the registry.
// It validates the toolset schema, stores the metadata, creates or returns
// the Pulse stream identifier, and starts the health ping loop.
func (s *Service) Register(ctx context.Context, p *genregistry.RegisterPayload) (*genregistry.RegisterResult, error) {
	// Validate tool schemas.
	if err := validateToolSchemas(p.Tools); err != nil {
		return nil, genregistry.MakeValidationError(fmt.Errorf("invalid tool schema: %w", err))
	}

	// Get or create the Pulse stream for this toolset.
	_, streamID, err := s.streamManager.GetOrCreateStream(ctx, p.Name)
	if err != nil {
		return nil, fmt.Errorf("create stream for toolset: %w", err)
	}

	registeredAt := time.Now().UTC().Format(time.RFC3339)

	// Build the toolset for storage.
	toolset := &genregistry.Toolset{
		Name:         p.Name,
		Description:  p.Description,
		Version:      p.Version,
		Tags:         p.Tags,
		Tools:        p.Tools,
		StreamID:     streamID,
		RegisteredAt: registeredAt,
	}

	// Store the toolset (creates or updates).
	if err := s.store.SaveToolset(ctx, toolset); err != nil {
		return nil, fmt.Errorf("save toolset: %w", err)
	}

	// Start health ping loop for the toolset.
	if err := s.healthTracker.StartPingLoop(ctx, p.Name); err != nil {
		return nil, fmt.Errorf("start health ping loop: %w", err)
	}

	return &genregistry.RegisterResult{
		StreamID:     streamID,
		RegisteredAt: registeredAt,
	}, nil
}

// validateToolSchemas validates that all tool schemas are valid JSON Schema.
func validateToolSchemas(tools []*genregistry.ToolSchema) error {
	for _, tool := range tools {
		if tool == nil {
			return fmt.Errorf("tool schema is nil")
		}
		if tool.Name == "" {
			return fmt.Errorf("tool schema missing name")
		}
		if len(tool.PayloadSchema) == 0 {
			return fmt.Errorf("tool %q: payload schema is required", tool.Name)
		}
		if len(tool.ResultSchema) == 0 {
			return fmt.Errorf("tool %q: result schema is required", tool.Name)
		}
		if !json.Valid(tool.PayloadSchema) {
			return fmt.Errorf("tool %q: payload schema is not valid JSON", tool.Name)
		}
		if !json.Valid(tool.ResultSchema) {
			return fmt.Errorf("tool %q: result schema is not valid JSON", tool.Name)
		}
		if len(tool.SidecarSchema) > 0 && !json.Valid(tool.SidecarSchema) {
			return fmt.Errorf("tool %q: sidecar schema is not valid JSON", tool.Name)
		}
	}
	return nil
}

// Unregister removes a toolset from the registry.
// It stops the health ping loop and removes the toolset from the store.
// Returns not-found error if the toolset doesn't exist.
// **Validates: Requirements 5.1, 5.2, 5.3**
func (s *Service) Unregister(ctx context.Context, p *genregistry.UnregisterPayload) error {
	// Delete the toolset from the store.
	if err := s.store.DeleteToolset(ctx, p.Name); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return genregistry.MakeNotFound(fmt.Errorf("toolset %q not found", p.Name))
		}
		return fmt.Errorf("delete toolset: %w", err)
	}

	// Stop the health ping loop.
	s.healthTracker.StopPingLoop(ctx, p.Name)

	// Remove the stream tracking (does not destroy the underlying stream).
	s.streamManager.RemoveStream(p.Name)

	return nil
}

// Pong records a pong response for a health check ping.
// This restores healthy status if the provider was previously marked unhealthy.
func (s *Service) Pong(ctx context.Context, p *genregistry.PongPayload) error {
	return s.healthTracker.RecordPong(ctx, p.Toolset)
}

// ListToolsets returns all registered toolsets with optional tag filtering.
// Returns all toolsets with metadata, supports tag filtering, and returns
// an empty list when the catalog is empty.
// **Validates: Requirements 6.1, 6.2, 6.3**
func (s *Service) ListToolsets(ctx context.Context, p *genregistry.ListToolsetsPayload) (*genregistry.ListToolsetsResult, error) {
	toolsets, err := s.store.ListToolsets(ctx, p.Tags)
	if err != nil {
		return nil, fmt.Errorf("list toolsets: %w", err)
	}

	infos := make([]*genregistry.ToolsetInfo, len(toolsets))
	for i, ts := range toolsets {
		infos[i] = toolsetToInfo(ts)
	}

	return &genregistry.ListToolsetsResult{
		Toolsets: infos,
	}, nil
}

// toolsetToInfo converts a Toolset to ToolsetInfo (metadata without full tool schemas).
func toolsetToInfo(ts *genregistry.Toolset) *genregistry.ToolsetInfo {
	return &genregistry.ToolsetInfo{
		Name:         ts.Name,
		Description:  ts.Description,
		Version:      ts.Version,
		Tags:         ts.Tags,
		ToolCount:    len(ts.Tools),
		RegisteredAt: ts.RegisteredAt,
	}
}

// GetToolset returns a specific toolset by name including all tool schemas.
// Returns the complete toolset with tool schemas, or not-found error if
// the toolset doesn't exist.
// **Validates: Requirements 7.1, 7.2**
func (s *Service) GetToolset(ctx context.Context, p *genregistry.GetToolsetPayload) (*genregistry.Toolset, error) {
	toolset, err := s.store.GetToolset(ctx, p.Name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, genregistry.MakeNotFound(fmt.Errorf("toolset %q not found", p.Name))
		}
		return nil, fmt.Errorf("get toolset: %w", err)
	}
	return toolset, nil
}

// Search searches toolsets by keyword matching name, description, or tags.
// Returns matching toolsets or an empty list when no matches are found.
// **Validates: Requirements 8.1, 8.2**
func (s *Service) Search(ctx context.Context, p *genregistry.SearchPayload) (*genregistry.SearchResult, error) {
	toolsets, err := s.store.SearchToolsets(ctx, p.Query)
	if err != nil {
		return nil, fmt.Errorf("search toolsets: %w", err)
	}

	infos := make([]*genregistry.ToolsetInfo, len(toolsets))
	for i, ts := range toolsets {
		infos[i] = toolsetToInfo(ts)
	}

	return &genregistry.SearchResult{
		Toolsets: infos,
	}, nil
}

// CallTool invokes a tool through the registry gateway.
// It validates the payload against the tool's payload schema, checks provider health,
// creates the per-call result stream, and publishes the request to the toolset stream.
func (s *Service) CallTool(ctx context.Context, p *genregistry.CallToolPayload) (*genregistry.CallToolResult, error) {
	// 1. Get the toolset to validate the tool exists and get its schema.
	toolset, err := s.store.GetToolset(ctx, p.Toolset)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, genregistry.MakeNotFound(fmt.Errorf("toolset %q not found", p.Toolset))
		}
		return nil, fmt.Errorf("get toolset: %w", err)
	}

	// 2. Find the tool within the toolset.
	var tool *genregistry.ToolSchema
	for _, t := range toolset.Tools {
		if t.Name == p.Tool {
			tool = t
			break
		}
	}
	if tool == nil {
		return nil, genregistry.MakeNotFound(fmt.Errorf("tool %q not found in toolset %q", p.Tool, p.Toolset))
	}

	// 3. Validate payload against tool's payload schema.
	if err := validatePayloadJSONAgainstSchema(p.PayloadJSON, tool.PayloadSchema); err != nil {
		return nil, genregistry.MakeValidationError(fmt.Errorf("payload validation failed: %w", err))
	}

	// 4. Check provider health - return service_unavailable if unhealthy.
	if !s.healthTracker.IsHealthy(p.Toolset) {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("no healthy providers for toolset %q", p.Toolset))
	}

	toolUseID := uuid.New().String()
	resultStreamID := toolregistry.ResultStreamID(toolUseID)
	resultStream, err := s.pulseClient.Stream(resultStreamID)
	if err != nil {
		return nil, fmt.Errorf("open result stream %q: %w", resultStreamID, err)
	}
	if _, err := resultStream.Add(ctx, "init", []byte("{}")); err != nil {
		return nil, fmt.Errorf("initialize result stream %q: %w", resultStreamID, err)
	}
	if err := s.setResultStreamTTL(ctx, resultStreamID); err != nil {
		return nil, err
	}

	meta := toolregistry.ToolCallMeta{
		RunID:            p.Meta.RunID,
		SessionID:        p.Meta.SessionID,
		TurnID:           derefString(p.Meta.TurnID),
		ToolCallID:       derefString(p.Meta.ToolCallID),
		ParentToolCallID: derefString(p.Meta.ParentToolCallID),
	}
	msg := toolregistry.NewToolCallMessage(toolUseID, tools.Ident(p.Tool), json.RawMessage(p.PayloadJSON), &meta)
	if err := s.streamManager.PublishToolCall(ctx, p.Toolset, msg); err != nil {
		return nil, fmt.Errorf("publish tool call: %w", err)
	}
	return &genregistry.CallToolResult{
		ToolUseID:      toolUseID,
		ResultStreamID: resultStreamID,
	}, nil
}

func validatePayloadJSONAgainstSchema(payloadJSON []byte, schemaBytes []byte) error {
	if len(schemaBytes) == 0 {
		return nil // No schema to validate against
	}

	// Unmarshal the schema JSON.
	var schemaDoc any
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		return fmt.Errorf("unmarshal schema: %w", err)
	}

	var payloadDoc any
	if err := json.Unmarshal(payloadJSON, &payloadDoc); err != nil {
		return fmt.Errorf("unmarshal payload: %w", err)
	}

	// Compile the schema.
	c := jsonschema.NewCompiler()
	if err := c.AddResource("schema.json", schemaDoc); err != nil {
		return fmt.Errorf("add schema resource: %w", err)
	}
	schema, err := c.Compile("schema.json")
	if err != nil {
		return fmt.Errorf("compile schema: %w", err)
	}

	// Validate the payload.
	if err := schema.Validate(payloadDoc); err != nil {
		return err
	}

	return nil
}

func (s *Service) setResultStreamTTL(ctx context.Context, streamID string) error {
	key := fmt.Sprintf("pulse:stream:%s", streamID)
	ok, err := s.rdb.Expire(ctx, key, s.resultStreamTTL).Result()
	if err != nil {
		return fmt.Errorf("set result stream TTL: %w", err)
	}
	if !ok {
		return fmt.Errorf("set result stream TTL: stream key %q missing", key)
	}
	return nil
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
