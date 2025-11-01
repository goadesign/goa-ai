package assistantapi

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agents/model"
	"goa.design/goa-ai/runtime/agents/planner"
)

type (
	// chatPlanner is a deterministic planner used by the example runtime harness.
	// It demonstrates the data-loop flow described in docs/plan.md: the first turn
	// always calls the MCP search tool, then the second turn summarizes the returned
	// documents into a natural-language reply.
	chatPlanner struct {
		modelID string
	}
)

const searchToolName = "assistant.assistant-mcp.search"

// newChatPlanner constructs a chat planner with the specified model ID for
// streaming responses.
func newChatPlanner(modelID string) *chatPlanner {
	return &chatPlanner{modelID: modelID}
}

// PlanStart always schedules a single MCP search tool call. The query is derived
// from the last user message so tests can control the response deterministically.
// Returns a plan with one tool call and a planner annotation describing the action.
func (p *chatPlanner) PlanStart(
	ctx context.Context,
	input planner.PlanInput,
) (planner.PlanResult, error) {
	query := "status update"
	if len(input.Messages) > 0 {
		query = input.Messages[len(input.Messages)-1].Content
	}
	payload := map[string]any{
		"query": query,
		"limit": 3,
	}
	note := planner.PlannerAnnotation{
		Text:   "Querying MCP knowledge base",
		Labels: map[string]string{"tool": "search"},
	}
	return planner.PlanResult{
		ToolCalls: []planner.ToolRequest{{Name: searchToolName, Payload: payload}},
		Notes:     []planner.PlannerAnnotation{note},
	}, nil
}

// PlanResume inspects the MCP tool results and emits a friendly summary. Any
// structured payloads passed through the tool telemetry are echoed so the runtime
// stream demonstrates both planner notes and assistant replies.
//
// If a streaming model is configured, this method uses it to generate the final
// response incrementally, emitting text chunks via the planner's streaming API.
func (p *chatPlanner) PlanResume(
	ctx context.Context,
	input planner.PlanResumeInput,
) (planner.PlanResult, error) {
	summary := "I could not find any related documents."
	if len(input.ToolResults) > 0 {
		summary = summarizeResult(input.ToolResults[0])
	}
	finalText := summary
	if p.modelID != "" {
		client, ok := input.Agent.ModelClient(p.modelID)
		if !ok {
			return planner.PlanResult{}, fmt.Errorf("model %s not registered", p.modelID)
		}
		streamer, err := client.Stream(ctx, model.Request{
			Model: p.modelID,
			Messages: []model.Message{
				{Role: "system", Content: "Summarize MCP results."},
				{Role: "assistant", Content: summary},
			},
			Stream: true,
		})
		if err != nil {
			if !errors.Is(err, model.ErrStreamingUnsupported) {
				return planner.PlanResult{}, err
			}
		} else {
			result, err := planner.ConsumeStream(ctx, streamer, input.Agent)
			if err != nil {
				return planner.PlanResult{}, err
			}
			if len(result.ToolCalls) > 0 {
				return planner.PlanResult{ToolCalls: result.ToolCalls}, nil
			}
			if result.Text != "" {
				finalText = result.Text
			}
		}
	}
	response := planner.AgentMessage{Role: "assistant", Content: finalText}
	return planner.PlanResult{
		FinalResponse: &planner.FinalResponse{Message: response},
		Notes: []planner.PlannerAnnotation{{
			Text:   "Summarized MCP search results",
			Labels: map[string]string{"phase": "respond"},
		}},
	}, nil
}

// summarizeResult extracts a human-readable summary from the MCP search tool
// result payload. It handles different payload structures (string arrays, object
// arrays with title/content fields) and returns a descriptive message.
func summarizeResult(result planner.ToolResult) string {
	docs, ok := result.Result.(map[string]any)
	if !ok {
		return "Search completed but returned an unexpected result."
	}
	items, _ := docs["documents"].([]any)
	if len(items) == 0 {
		return "Search completed but did not return any documents."
	}
	switch first := items[0].(type) {
	case string:
		return fmt.Sprintf("Top document: %s", first)
	case map[string]any:
		title, _ := first["title"].(string)
		content, _ := first["content"].(string)
		if title == "" && content == "" {
			return "Search returned a structured document without title or content."
		}
		if title == "" {
			return fmt.Sprintf("Key finding: %s", content)
		}
		return fmt.Sprintf("%s — %s", title, content)
	default:
		return fmt.Sprintf("Search returned %T, showing raw payload", first)
	}
}

var _ planner.Planner = (*chatPlanner)(nil)
