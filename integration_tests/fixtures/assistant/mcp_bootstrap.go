package assistantapi

import (
	"context"
	"encoding/json"

	mcpassistant "example.com/assistant/gen/mcp_assistant"
	mcpruntime "goa.design/goa-ai/runtime/mcp"
)

// NewMcpAssistant returns an MCP server implementation for the assistant
// service used by the integration test fixture. It wraps the generated
// MCP adapter and provides a minimal prompt provider so dynamic prompts
// are available during tests. It also shims a static prompt expected by
// scenarios (e.g., "code_review").
func NewMcpAssistant() mcpassistant.Service {
	base := mcpassistant.NewMCPAdapter(NewAssistant(), promptProvider{}, nil)
	return &mcpShim{MCPAdapter: base}
}

// mcpShim embeds the generated adapter and overrides specific methods
// needed by the integration scenarios without changing generated code.
type mcpShim struct {
	*mcpassistant.MCPAdapter
}

// ToolsCall delegates to the adapter.
func (s *mcpShim) ToolsCall(ctx context.Context, p *mcpassistant.ToolsCallPayload, stream mcpassistant.ToolsCallServerStream) (*mcpassistant.ToolsCallResult, error) {
	err := s.MCPAdapter.ToolsCall(ctx, p, stream)
	return nil, err
}

// PromptsGet implements a minimal static prompt for tests and otherwise
// delegates to the generated adapter implementation.
func (s *mcpShim) PromptsGet(ctx context.Context, p *mcpassistant.PromptsGetPayload) (*mcpassistant.PromptsGetResult, error) {
	if p != nil && p.Name == "code_review" {
		// Return a simple static prompt structure; scenarios only assert success.
		return &mcpassistant.PromptsGetResult{
			Description: nil,
			Messages: []*mcpassistant.PromptMessage{
				{Role: "system", Content: &mcpassistant.MessageContent{Type: "text", Text: strPtr("Review the following code")}},
			},
		}, nil
	}
	return s.MCPAdapter.PromptsGet(ctx, p)
}

// Initialize delegates to the adapter.
func (s *mcpShim) Initialize(ctx context.Context, p *mcpassistant.InitializePayload) (*mcpassistant.InitializeResult, error) {
	return s.MCPAdapter.Initialize(ctx, p)
}

// Ping delegates to the adapter.
func (s *mcpShim) Ping(ctx context.Context) (*mcpassistant.PingResult, error) {
	return s.MCPAdapter.Ping(ctx)
}

// ResourcesList delegates to the adapter.
func (s *mcpShim) ResourcesList(ctx context.Context, p *mcpassistant.ResourcesListPayload) (*mcpassistant.ResourcesListResult, error) {
	return s.MCPAdapter.ResourcesList(ctx, p)
}

// ResourcesRead delegates to the adapter.
func (s *mcpShim) ResourcesRead(ctx context.Context, p *mcpassistant.ResourcesReadPayload) (*mcpassistant.ResourcesReadResult, error) {
	return s.MCPAdapter.ResourcesRead(ctx, p)
}

// ResourcesSubscribe delegates to the adapter.
func (s *mcpShim) ResourcesSubscribe(ctx context.Context, p *mcpassistant.ResourcesSubscribePayload) error {
	return s.MCPAdapter.ResourcesSubscribe(ctx, p)
}

// ResourcesUnsubscribe delegates to the adapter.
func (s *mcpShim) ResourcesUnsubscribe(ctx context.Context, p *mcpassistant.ResourcesUnsubscribePayload) error {
	return s.MCPAdapter.ResourcesUnsubscribe(ctx, p)
}

// PromptsList delegates to the adapter.
func (s *mcpShim) PromptsList(ctx context.Context, p *mcpassistant.PromptsListPayload) (*mcpassistant.PromptsListResult, error) {
	return s.MCPAdapter.PromptsList(ctx, p)
}

// NotifyStatusUpdate converts payload to runtime notification and delegates to the adapter.
func (s *mcpShim) NotifyStatusUpdate(ctx context.Context, p *mcpassistant.SendNotificationPayload) error {
	if p == nil {
		return nil
	}
	n := &mcpruntime.Notification{Type: p.Type, Message: p.Message, Data: p.Data}
	return s.MCPAdapter.NotifyStatusUpdate(ctx, n)
}

// EventsStream delegates to the adapter.
func (s *mcpShim) EventsStream(ctx context.Context, stream mcpassistant.EventsStreamServerStream) (*mcpassistant.EventsStreamResult, error) {
	err := s.MCPAdapter.EventsStream(ctx, stream)
	return nil, err
}

// ToolsList delegates to the adapter.
func (s *mcpShim) ToolsList(ctx context.Context, p *mcpassistant.ToolsListPayload) (*mcpassistant.ToolsListResult, error) {
	return s.MCPAdapter.ToolsList(ctx, p)
}

// promptProvider implements the generated PromptProvider interface to
// serve dynamic prompts used by tests (e.g., "contextual_prompts").
type promptProvider struct{}

func (promptProvider) GetContextualPromptsPrompt(ctx context.Context, arguments json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
	// Produce a simple message that echoes the request; tests only
	// verify success path, not specific content.
	return &mcpassistant.PromptsGetResult{
		Description: nil,
		Messages: []*mcpassistant.PromptMessage{
			{Role: "system", Content: &mcpassistant.MessageContent{Type: "text", Text: strPtr("Dynamic contextual prompts")}},
		},
	}, nil
}

// GetCodeReviewPrompt satisfies the generated provider when a static prompt is present.
func (promptProvider) GetCodeReviewPrompt(arguments json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
	return &mcpassistant.PromptsGetResult{
		Description: strPtr("Code review guidance"),
		Messages: []*mcpassistant.PromptMessage{
			{Role: "system", Content: &mcpassistant.MessageContent{Type: "text", Text: strPtr("Review the provided code and suggest improvements.")}},
		},
	}, nil
}

func strPtr(s string) *string { return &s }
