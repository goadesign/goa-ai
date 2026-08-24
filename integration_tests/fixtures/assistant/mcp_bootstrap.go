package assistantapi

import (
	"context"
	"encoding/json"

	mcpassistant "example.com/assistant/gen/mcp_assistant"
)

// NewMcpAssistant returns an MCP server implementation for the assistant
// service used by the integration test fixture. The prompt provider supplies
// the dynamic prompt content exercised by the scenarios.
func NewMcpAssistant() mcpassistant.Service {
	return mcpassistant.NewMCPAdapter(NewAssistant(), promptProvider{}, nil)
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
