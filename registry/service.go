// Package registry provides the internal tool registry service implementation.
package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/registry/store"
	"goa.design/goa-ai/registry/store/memory"
)

type (
	// Service implements the registry service interface.
	// It provides toolset registration, discovery, and tool invocation capabilities.
	Service struct {
		store               store.Store
		streamManager       StreamManager
		healthTracker       HealthTracker
		resultStreamManager ResultStreamManager
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
		// ResultStreamManager manages temporary result streams.
		ResultStreamManager ResultStreamManager
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
	if opts.ResultStreamManager == nil {
		return nil, fmt.Errorf("result stream manager is required")
	}
	return &Service{
		store:               st,
		streamManager:       opts.StreamManager,
		healthTracker:       opts.HealthTracker,
		resultStreamManager: opts.ResultStreamManager,
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

	// Record registration timestamp.
	registeredAt := time.Now().UTC().Format(time.RFC3339)

	// Convert tools from ToolSchema to Tool.
	tools := make([]*genregistry.Tool, len(p.Tools))
	for i, ts := range p.Tools {
		tools[i] = &genregistry.Tool{
			Name:         ts.Name,
			Description:  ts.Description,
			InputSchema:  ts.InputSchema,
			OutputSchema: ts.OutputSchema,
		}
	}

	// Build the toolset for storage.
	toolset := &genregistry.Toolset{
		Name:         p.Name,
		Description:  p.Description,
		Version:      p.Version,
		Tags:         p.Tags,
		Tools:        tools,
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
		if len(tool.InputSchema) == 0 {
			return fmt.Errorf("tool %q: input schema is required", tool.Name)
		}
		if !json.Valid(tool.InputSchema) {
			return fmt.Errorf("tool %q: input schema is not valid JSON", tool.Name)
		}
		if len(tool.OutputSchema) > 0 && !json.Valid(tool.OutputSchema) {
			return fmt.Errorf("tool %q: output schema is not valid JSON", tool.Name)
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

// EmitToolResult publishes a tool execution result to the result stream.
// This is called by providers to deliver results back to the waiting gateway.
// **Validates: Requirements 4.1, 4.2, 4.3**
func (s *Service) EmitToolResult(ctx context.Context, p *genregistry.EmitToolResultPayload) error {
	// Build the result message.
	var msg *ToolResultMessage
	if p.Error != nil {
		msg = NewToolResultErrorMessage(p.ToolUseID, p.Error.Code, p.Error.Message)
	} else {
		// Marshal the result to JSON.
		resultBytes, err := json.Marshal(p.Result)
		if err != nil {
			return fmt.Errorf("marshal result: %w", err)
		}
		msg = NewToolResultMessage(p.ToolUseID, resultBytes)
	}

	// Publish to the result stream.
	if err := s.resultStreamManager.PublishResult(ctx, p.ToolUseID, msg); err != nil {
		return fmt.Errorf("publish result: %w", err)
	}

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

// DefaultCallToolTimeout is the default timeout for tool invocations.
const DefaultCallToolTimeout = 30 * time.Second

// CallTool invokes a tool through the registry gateway.
// It validates the payload against the tool's input schema, checks provider health,
// publishes the request to the toolset stream, and waits for a result.
// **Validates: Requirements 9.1, 9.2, 9.3, 9.4, 9.5**
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
	var tool *genregistry.Tool
	for _, t := range toolset.Tools {
		if t.Name == p.Tool {
			tool = t
			break
		}
	}
	if tool == nil {
		return nil, genregistry.MakeNotFound(fmt.Errorf("tool %q not found in toolset %q", p.Tool, p.Toolset))
	}

	// 3. Validate payload against tool's input schema.
	if err := validatePayloadAgainstSchema(p.Payload, tool.InputSchema); err != nil {
		return nil, genregistry.MakeValidationError(fmt.Errorf("payload validation failed: %w", err))
	}

	// 4. Check provider health - return service_unavailable if unhealthy.
	if !s.healthTracker.IsHealthy(p.Toolset) {
		return nil, genregistry.MakeServiceUnavailable(fmt.Errorf("no healthy providers for toolset %q", p.Toolset))
	}

	// 5. Create result stream and get tool_use_id.
	_, toolUseID, _, err := s.resultStreamManager.CreateResultStream(ctx)
	if err != nil {
		return nil, fmt.Errorf("create result stream: %w", err)
	}

	// 6. Set TTL on the result stream for automatic cleanup.
	// Non-fatal: TTL will eventually clean up if this fails.
	_ = s.resultStreamManager.SetTTL(ctx, toolUseID, DefaultCallToolTimeout*2)

	// 7. Marshal payload to JSON for the stream message.
	payloadBytes, err := json.Marshal(p.Payload)
	if err != nil {
		// Cleanup the result stream on error.
		_ = s.resultStreamManager.DestroyResultStream(ctx, toolUseID)
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	// 8. Publish request to toolset stream.
	msg := NewToolCallMessage(toolUseID, p.Tool, payloadBytes)
	if err := s.streamManager.PublishToolCall(ctx, p.Toolset, msg); err != nil {
		// Cleanup the result stream on error.
		_ = s.resultStreamManager.DestroyResultStream(ctx, toolUseID)
		return nil, fmt.Errorf("publish tool call: %w", err)
	}

	// 9. Wait for result with timeout.
	resultMsg, err := s.resultStreamManager.WaitForResult(ctx, toolUseID, WaitForResultOptions{
		Timeout: DefaultCallToolTimeout,
	})
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return nil, genregistry.MakeTimeout(fmt.Errorf("tool invocation timed out after %v", DefaultCallToolTimeout))
		}
		return nil, fmt.Errorf("wait for result: %w", err)
	}

	// 10. Build and return the result.
	result := &genregistry.CallToolResult{
		ToolUseID: toolUseID,
	}

	if resultMsg.Error != nil {
		result.Error = &genregistry.ToolError{
			Code:    resultMsg.Error.Code,
			Message: resultMsg.Error.Message,
		}
	} else if len(resultMsg.Result) > 0 {
		// Unmarshal the result JSON into any.
		var resultAny any
		if err := json.Unmarshal(resultMsg.Result, &resultAny); err != nil {
			return nil, fmt.Errorf("unmarshal result: %w", err)
		}
		result.Result = resultAny
	}

	return result, nil
}

// validatePayloadAgainstSchema validates a payload against a JSON Schema.
func validatePayloadAgainstSchema(payload any, schemaBytes []byte) error {
	if len(schemaBytes) == 0 {
		return nil // No schema to validate against
	}

	// Unmarshal the schema JSON.
	var schemaDoc any
	if err := json.Unmarshal(schemaBytes, &schemaDoc); err != nil {
		return fmt.Errorf("unmarshal schema: %w", err)
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
	if err := schema.Validate(payload); err != nil {
		return err
	}

	return nil
}
