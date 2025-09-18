package assistantapi

import (
    "context"
    "encoding/json"

    mcpassistant "example.com/assistant/gen/mcp_assistant"
)

// ExamplePromptProvider is a minimal implementation of the generated
// mcpassistant.PromptProvider interface used by the example server.
type ExamplePromptProvider struct{}

// NewPromptProvider returns a simple prompt provider implementation.
func NewPromptProvider() mcpassistant.PromptProvider { return &ExamplePromptProvider{} }

// GetCodeReviewPrompt returns a basic static prompt for code review.
func (p *ExamplePromptProvider) GetCodeReviewPrompt(arguments json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
    return &mcpassistant.PromptsGetResult{
        Description: strptr("Template for code review"),
        Messages: []*mcpassistant.PromptMessage{
            {Role: "system", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr("You are a helpful code reviewer.")}},
            {Role: "user", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr("Please review the following code and suggest improvements.")}},
        },
    }, nil
}

// GetExplainConceptPrompt returns a basic static prompt for concept explanation.
func (p *ExamplePromptProvider) GetExplainConceptPrompt(arguments json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
    return &mcpassistant.PromptsGetResult{
        Description: strptr("Template for explaining concepts"),
        Messages: []*mcpassistant.PromptMessage{
            {Role: "system", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr("You explain complex topics simply with examples.")}},
            {Role: "user", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr("Explain the concept in simple terms with an analogy.")}},
        },
    }, nil
}

// GetContextualPromptsPrompt demonstrates a simple dynamic prompt using the raw
// JSON arguments as part of the content. A real implementation would unmarshal
// and customize messages based on the inputs.
func (p *ExamplePromptProvider) GetContextualPromptsPrompt(ctx context.Context, arguments json.RawMessage) (*mcpassistant.PromptsGetResult, error) {
    return &mcpassistant.PromptsGetResult{
        Description: strptr("Generate prompts based on context"),
        Messages: []*mcpassistant.PromptMessage{
            {Role: "system", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr("You produce prompts tailored to the provided context.")}},
            {Role: "user", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr("Context:")}},
            {Role: "user", Content: &mcpassistant.MessageContent{Type: "text", Text: strptr(string(arguments))}},
        },
    }, nil
}

func strptr(s string) *string { return &s }

