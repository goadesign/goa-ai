package assistantapi

import (
	"context"
	"encoding/json"

	mcpassistant "example.com/assistant/gen/mcp_assistant"
)

type (
	// promptProvider implements the generated PromptProvider interface so the MCP
	// adapter can serve both static and dynamic prompts. Static prompts are defined
	// in the DSL and served automatically; dynamic prompts are synthesized at runtime
	// based on caller-provided arguments.
	promptProvider struct{}
)

// GetCodeReviewPrompt returns nil to fall back to the static template defined in
// the design. This keeps static prompt maintenance centralized in the DSL rather
// than duplicating template content in the service implementation.
//
// When nil is returned, the MCP adapter serves the static prompt template
// from the design definition.
func (p *promptProvider) GetCodeReviewPrompt(
	json.RawMessage,
) (*mcpassistant.PromptsGetResult, error) {
	return nil, nil
}

// GetContextualPromptsPrompt synthesizes a deterministic prompt deck from the
// caller-provided arguments so tests can exercise dynamic prompt flows. The
// generated prompt includes a system message and user message constructed from
// the context and task arguments.
//
// This demonstrates how to implement dynamic prompts that vary based on runtime
// parameters while maintaining type safety through the generated interface.
func (p *promptProvider) GetContextualPromptsPrompt(
	_ context.Context,
	arguments json.RawMessage,
) (*mcpassistant.PromptsGetResult, error) {
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

// strptr returns a pointer to the provided string. Helper for constructing
// pointer fields in generated types.
func strptr(s string) *string {
	return &s
}
