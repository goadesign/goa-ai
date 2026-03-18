// Package registry provides the internal tool registry service implementation.
//
// This file owns the transport-facing registry contract: it admits toolsets
// into the shared catalog, validates routed tool calls against admitted
// schemas, gates execution on provider health, and publishes tool-call traffic
// onto the canonical registry streams.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	// Service implements the registry service interface.
	// It provides toolset registration, discovery, and tool invocation capabilities.
	Service struct {
		catalog       *toolsetCatalog
		validator     *schemaValidator
		streamManager StreamManager
		healthTracker HealthTracker

		pulseClient     clientspulse.Client
		resultStreamTTL time.Duration
	}

	// serviceOptions configures the registry service.
	serviceOptions struct {
		// catalog is the authoritative toolset catalog.
		catalog *toolsetCatalog
		// StreamManager manages Pulse streams for toolset communication.
		StreamManager StreamManager
		// HealthTracker tracks provider health status.
		HealthTracker HealthTracker
		// PulseClient creates/opens Pulse streams. Required for CallTool.
		PulseClient clientspulse.Client
		// ResultStreamTTL controls how long result streams live in Redis.
		// When zero, defaults to 15 minutes.
		ResultStreamTTL time.Duration
	}
)

// Compile-time check that Service implements the generated interface.
var _ genregistry.Service = (*Service)(nil)

// newService wires the registry service over the already-constructed catalog,
// stream manager, health tracker, and Pulse client.
func newService(opts serviceOptions) (*Service, error) {
	if opts.catalog == nil {
		return nil, fmt.Errorf("toolset catalog is required")
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
	ttl := opts.ResultStreamTTL
	if ttl == 0 {
		ttl = 15 * time.Minute
	}
	return &Service{
		catalog:         opts.catalog,
		validator:       newSchemaValidator(),
		streamManager:   opts.StreamManager,
		healthTracker:   opts.HealthTracker,
		pulseClient:     opts.PulseClient,
		resultStreamTTL: ttl,
	}, nil
}

// Register registers a toolset with the registry.
// It validates the toolset schema, ensures the toolset request stream exists,
// stores the metadata, and starts the health ping loop.
func (s *Service) Register(ctx context.Context, p *genregistry.RegisterPayload) (*genregistry.RegisterResult, error) {
	// Validate tool schemas.
	if err := s.validator.ValidateToolSchemas(p.Tools); err != nil {
		return nil, genregistry.MakeValidationError(fmt.Errorf("invalid tool schema: %w", err))
	}

	// Ensure the Pulse request stream for this toolset exists.
	_, _, err := s.streamManager.GetOrCreateStream(ctx, p.Name)
	if err != nil {
		return nil, fmt.Errorf("create stream for toolset: %w", err)
	}

	// registered_at is transport metadata for discovery and debugging.
	// Registration identity is the catalog-owned opaque token rotated on save.
	registeredAt := time.Now().UTC().Format(time.RFC3339Nano)

	// Build the toolset for storage.
	toolset := &genregistry.Toolset{
		Name:         p.Name,
		Description:  p.Description,
		Version:      p.Version,
		Tags:         p.Tags,
		Tools:        p.Tools,
		RegisteredAt: registeredAt,
	}

	// Store the toolset (creates or updates).
	if err := s.catalog.SaveToolset(ctx, toolset); err != nil {
		return nil, fmt.Errorf("save toolset: %w", err)
	}

	// Start health ping loop for the toolset.
	if err := s.healthTracker.StartPingLoop(ctx, p.Name); err != nil {
		return nil, fmt.Errorf("start health ping loop: %w", err)
	}

	return &genregistry.RegisterResult{
		RegisteredAt: registeredAt,
	}, nil
}

// Unregister removes a toolset from the registry.
// It stops the health ping loop and removes the toolset from the catalog.
// Returns not-found error if the toolset doesn't exist.
// **Validates: Requirements 5.1, 5.2, 5.3**
func (s *Service) Unregister(ctx context.Context, p *genregistry.UnregisterPayload) error {
	// Delete the toolset from the catalog.
	if err := s.catalog.DeleteToolset(ctx, p.Name); err != nil {
		if errors.Is(err, errToolsetNotFound) {
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
	return s.healthTracker.RecordPong(ctx, p.Toolset, p.PingID)
}

// ListToolsets returns all registered toolsets with optional tag filtering.
// Returns all toolsets with metadata, supports tag filtering, and returns
// an empty list when the catalog is empty.
// **Validates: Requirements 6.1, 6.2, 6.3**
func (s *Service) ListToolsets(ctx context.Context, p *genregistry.ListToolsetsPayload) (*genregistry.ListToolsetsResult, error) {
	toolsets, err := s.catalog.ListToolsets(ctx, p.Tags)
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
	toolset, err := s.catalog.GetToolset(ctx, p.Name)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
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
	toolsets, err := s.catalog.SearchToolsets(ctx, p.Query)
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
	toolset, err := s.catalog.GetToolset(ctx, p.Toolset)
	if err != nil {
		if errors.Is(err, errToolsetNotFound) {
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
	if err := s.validator.ValidatePayload(tool.PayloadSchema, p.PayloadJSON); err != nil {
		return nil, genregistry.MakeValidationError(fmt.Errorf("payload validation failed: %w", err))
	}

	// 4. Check provider health - return service_unavailable if unhealthy.
	h, err := s.healthTracker.Health(p.Toolset)
	if err != nil {
		return nil, fmt.Errorf("check toolset %q health: %w", p.Toolset, err)
	}
	if !h.Healthy {
		lastPong := "missing"
		if !h.LastPong.IsZero() {
			lastPong = h.LastPong.UTC().Format(time.RFC3339Nano)
		}
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf(
			"no healthy providers for toolset %q (staleness_threshold=%s, last_pong=%s, age=%s)",
			p.Toolset,
			h.StalenessThreshold,
			lastPong,
			h.Age,
		))
	}

	toolUseID := toolUseIDForCall(p.Meta)
	resultStreamID := toolregistry.ResultStreamID(toolUseID)
	resultStream, err := s.pulseClient.Stream(resultStreamID, streamopts.WithStreamTTL(s.resultStreamTTL))
	if err != nil {
		return nil, fmt.Errorf("open result stream %q: %w", resultStreamID, err)
	}
	if _, err := resultStream.Add(ctx, "init", []byte("{}")); err != nil {
		return nil, fmt.Errorf("initialize result stream %q: %w", resultStreamID, err)
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
		ToolUseID: toolUseID,
	}, nil
}

// toolUseIDForCall returns the stable transport identity for a registry-routed
// tool execution. When callers already supplied a logical ToolCallID, retries
// must reuse it so the registry does not turn one logical tool call into
// multiple transport attempts with different result streams.
func toolUseIDForCall(meta *genregistry.ToolCallMeta) string {
	if meta != nil && meta.ToolCallID != nil && *meta.ToolCallID != "" {
		return *meta.ToolCallID
	}
	return uuid.New().String()
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
