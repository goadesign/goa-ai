package assistantapi

import (
	"context"
	"encoding/json"

	mcpassistant "example.com/assistant/gen/mcp_assistant"
)

// promptProvider implements mcpassistant.PromptProvider for dynamic prompts.
type promptProvider struct{}

// GetCodeReviewPrompt returns nil to fall back to the static template.
func (p *promptProvider) GetCodeReviewPrompt(arguments json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
	return nil, nil
}

// GetContextualPromptsPrompt returns a deterministic prompt using provided arguments.
func (p *promptProvider) GetContextualPromptsPrompt(ctx context.Context, arguments json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
	var args struct {
		Context string `json:"context"`
		Task    string `json:"task"`
	}
	_ = json.Unmarshal(arguments, &args)

	// Build simple messages
	sys := "You are an assistant generating context-aware prompts."
	usr := "Context: " + args.Context + ", Task: " + args.Task

	msgs := []*mcpassistant.PromptMessage{
		{Role: "system", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr(sys)}},
		{Role: "user", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr(usr)}},
	}
	desc := "Dynamic prompts based on provided context and task"
	return &mcpassistant.PromptsGetResult{Description: strptr(desc), Messages: msgs}, nil
}

func strptr(s string) *string { return &s }
