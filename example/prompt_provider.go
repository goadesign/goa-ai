package assistantapi

import (
	"context"
	"encoding/json"

	mcpassistant "example.com/assistant/gen/mcp_assistant"
)

// promptProvider implements the generated PromptProvider interface so the MCP
// adapter can serve both static and dynamic prompts.
type promptProvider struct{}

// GetCodeReviewPrompt returns nil to fall back to the static template defined
// in the design. This keeps static prompt maintenance centralized in the DSL.
func (p *promptProvider) GetCodeReviewPrompt(json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
	return nil, nil
}

// GetContextualPromptsPrompt synthesizes a deterministic prompt deck from the
// caller-provided arguments so tests can exercise dynamic prompt flows.
func (p *promptProvider) GetContextualPromptsPrompt(_ context.Context, arguments json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
	var args struct {
		Context string `json:"context"`
		Task    string `json:"task"`
	}
	_ = json.Unmarshal(arguments, &args)

	sys := "You are an assistant generating context-aware prompts."
	usr := "Context: " + args.Context + ", Task: " + args.Task

	msgs := []*mcpassistant.PromptMessage{
		{
			Role: "system",
			Content: &mcpassistant.MessageContent{
				Type: "text",
				Text: strptr(sys),
			},
		},
		{
			Role: "user",
			Content: &mcpassistant.MessageContent{
				Type: "text",
				Text: strptr(usr),
			},
		},
	}
	desc := "Dynamic prompts based on provided context and task"
	return &mcpassistant.PromptsGetResult{
		Description: strptr(desc),
		Messages:    msgs,
	}, nil
}

func strptr(s string) *string { return &s }
