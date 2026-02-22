// Package runtime provides history management policies for bounding conversation
// context. HistoryPolicy implementations transform message history before each
// planner invocation to prevent unbounded context growth.
package runtime

import (
	"context"
	"fmt"
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// HistoryPolicy transforms message history before planning. Implementations
	// must:
	//   - Preserve the System Prompt (typically the first message(s) with
	//     system role).
	//   - Respect turn boundaries (User + Assistant pairs).
	//   - Maintain ToolUse/ToolResult integrity (never orphan a result without
	//     its call).
	//
	// Policies are applied by the runtime before each planner invocation
	// (PlanStart and PlanResume). Callers may log policy errors and fall back to
	// the original messages when appropriate.
	HistoryPolicy func(ctx context.Context, msgs []*model.Message) ([]*model.Message, error)

	// CompressOption configures the Compress history policy.
	CompressOption func(*compressConfig)

	// compressConfig carries optional configuration for the Compress policy.
	compressConfig struct {
		// summaryPrompt is the instruction for summarization.
		summaryPrompt string
		// summaryRole determines where to place the summary (system or user).
		summaryRole model.ConversationRole
		// modelClass selects the model family for summarization.
		modelClass model.ModelClass
	}

	// turn represents a logical conversation turn: a user message and its
	// corresponding assistant response (including any tool exchanges).
	turn struct {
		messages []*model.Message
	}
)

func defaultCompressConfig() *compressConfig {
	return &compressConfig{
		summaryPrompt: defaultSummaryPrompt,
		summaryRole:   model.ConversationRoleSystem,
		modelClass:    model.ModelClassSmall,
	}
}

const defaultSummaryPrompt = `
Your task is to create a detailed summary of the conversation so far, paying close attention to the user's explicit requests and your previous actions.
This summary should be thorough in capturing key details, decisions, and context that would be essential for continuing the work without losing important information.

Before providing your final summary, wrap your analysis in tags to organize your thoughts and ensure you've covered all necessary points. In your analysis process:

1. Chronologically analyze each message and section of the conversation. For each section thoroughly identify:
  - The user's explicit requests and intents
  - Your approach to addressing the user's requests
  - Key decisions, concepts, and patterns
  - Specific details like names, references, artifacts, edits, or outputs produced
2. Double-check for accuracy and completeness, addressing each required element thoroughly.

Your summary should include the following sections:

1. Primary Request and Intent: Capture all of the user's explicit requests and intents in detail
2. Key Concepts: List all important concepts, topics, and domains discussed.
3. Artifacts and References: Enumerate specific items examined, modified, or created (documents, data, outputs, etc.). Pay special attention to the most recent messages and include relevant excerpts where applicable, with a summary
of why each is important.
4. Problem Solving: Document problems solved and any ongoing efforts.
5. Pending Tasks: Outline any pending tasks that you have explicitly been asked to work on.
6. Current Work: Describe in detail precisely what was being worked on immediately before this summary request, paying special attention to the most recent messages from both user and assistant. Include specific references and
excerpts where applicable.
7. Optional Next Step: List the next step that you will take that is related to the most recent work you were doing. IMPORTANT: ensure that this step is DIRECTLY in line with the user's explicit requests, and the task you were
working on immediately before this summary request. If your last task was concluded, then only list next steps if they are explicitly in line with the user's request. Do not start on tangential requests without confirming with the
user first.
8. If there is a next step, include direct quotes from the most recent conversation showing exactly what task you were working on and where you left off. This should be verbatim to ensure there's no drift in task interpretation.

Here's an example of how your output should be structured:

2. Key Concepts:
  - [Concept 1]
  - [Concept 2]
  - [...]
3. Artifacts and References:
  - [Item 1]
      - [Summary of why this item is important]
    - [Summary of changes or observations, if any]
    - [Relevant excerpt]
  - [Item 2]
      - [Relevant excerpt]
  - [...]
4. Problem Solving:
[Description of solved problems and ongoing efforts]
5. Pending Tasks:
  - [Task 1]
  - [Task 2]
  - [...]
6. Current Work:
[Precise description of current work]
7. Optional Next Step:
[Next step to take, if applicable]

Please provide your summary based on the conversation so far, following this structure and ensuring precision and thoroughness in your response.

CONVERSATION:
%s`

// WithSummaryPrompt sets a custom summarization prompt. The prompt should contain
// a %s placeholder where the conversation text will be inserted.
func WithSummaryPrompt(prompt string) CompressOption {
	return func(c *compressConfig) {
		c.summaryPrompt = prompt
	}
}

// WithSummaryRole sets the role for the summary message (system or user).
func WithSummaryRole(role model.ConversationRole) CompressOption {
	return func(c *compressConfig) {
		c.summaryRole = role
	}
}

// WithModelClass sets the model class used for summarization.
func WithModelClass(class model.ModelClass) CompressOption {
	return func(c *compressConfig) {
		c.modelClass = class
	}
}

// KeepRecentTurns returns a policy that keeps only the most recent N turns of
// conversation history. A "turn" is defined as a User message followed by its
// corresponding Assistant response (including any tool use/result exchanges).
//
// The policy always preserves:
//   - All System messages at the start of the conversation
//   - Complete turn boundaries (never splits a user query from its response)
//   - Tool use/result integrity (keeps results with their corresponding calls)
//
// Example: KeepRecentTurns(5) keeps the last 5 user-assistant exchanges.
func KeepRecentTurns(n int) HistoryPolicy {
	return func(_ context.Context, msgs []*model.Message) ([]*model.Message, error) {
		if n <= 0 || len(msgs) == 0 {
			return msgs, nil
		}

		// Identify system messages at the start (context, not history)
		systemEnd := 0
		for i, m := range msgs {
			if m.Role != model.ConversationRoleSystem {
				break
			}
			systemEnd = i + 1
		}

		// If everything is system messages, return as-is
		if systemEnd >= len(msgs) {
			return msgs, nil
		}

		// Parse remaining messages into turns
		history := msgs[systemEnd:]
		turns := parseTurns(history)

		// Keep only the last N turns
		if len(turns) <= n {
			return msgs, nil
		}

		keepTurns := turns[len(turns)-n:]
		var keepMsgs []*model.Message
		for _, t := range keepTurns {
			keepMsgs = append(keepMsgs, t.messages...)
		}

		// Reconstruct: system messages + kept turns
		result := make([]*model.Message, 0, systemEnd+len(keepMsgs))
		result = append(result, msgs[:systemEnd]...)
		result = append(result, keepMsgs...)

		return result, nil
	}
}

// Compress returns a policy that summarizes older conversation history when
// the turn count exceeds triggerAt. The policy uses the provided model client
// to generate a summary of older turns, keeping the most recent keepRecent turns
// intact.
//
// Hysteresis: Compression only triggers when len(turns) >= triggerAt. After
// compression, history drops to keepRecent + 1 (summary), so it won't trigger
// again until history regrows to triggerAt.
//
// The policy always preserves:
//   - All System messages at the start of the conversation
//   - The most recent keepRecent turns in full fidelity
//   - Tool use/result integrity in the kept turns
//
// Example: Compress(30, 10, client) triggers at 30 turns, compresses to a
// summary + 10 recent turns, then won't trigger again until 30 turns accumulate.
func Compress(triggerAt, keepRecent int, client model.Client, opts ...CompressOption) HistoryPolicy {
	cfg := defaultCompressConfig()
	for _, opt := range opts {
		opt(cfg)
	}

	return func(ctx context.Context, msgs []*model.Message) ([]*model.Message, error) {
		if triggerAt <= 0 || keepRecent < 0 || client == nil || len(msgs) == 0 {
			return msgs, nil
		}

		// Identify system messages at the start (context, not history)
		systemEnd := 0
		for i, m := range msgs {
			if m.Role != model.ConversationRoleSystem {
				break
			}
			systemEnd = i + 1
		}

		// If everything is system messages, return as-is
		if systemEnd >= len(msgs) {
			return msgs, nil
		}

		// Parse remaining messages into turns
		history := msgs[systemEnd:]
		turns := parseTurns(history)

		// Check if compression should trigger
		if len(turns) < triggerAt {
			return msgs, nil
		}

		// Split into [toCompress] and [keepRecent]
		splitIdx := len(turns) - keepRecent
		if splitIdx <= 0 {
			return msgs, nil
		}

		toCompress := turns[:splitIdx]
		toKeep := turns[splitIdx:]

		// Build conversation text for summarization
		var sb strings.Builder
		for _, t := range toCompress {
			for _, m := range t.messages {
				sb.WriteString(formatMessage(m))
				sb.WriteString("\n")
			}
		}

		// Call the model to summarize
		summaryPrompt := fmt.Sprintf(cfg.summaryPrompt, sb.String())
		req := &model.Request{
			ModelClass: cfg.modelClass,
			Messages: []*model.Message{
				{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: summaryPrompt}},
				},
			},
		}

		resp, err := client.Complete(ctx, req)
		if err != nil {
			// Surface the error so callers can decide whether to fall back to the
			// original messages or terminate the run.
			return msgs, err
		}

		// Extract summary text
		summaryText := extractResponseText(resp)
		if summaryText == "" {
			return msgs, nil
		}

		// Build summary message
		summaryMsg := &model.Message{
			Role: cfg.summaryRole,
			Parts: []model.Part{
				model.TextPart{Text: "[Conversation Summary]\n" + summaryText},
			},
			Meta: map[string]any{
				"goa_ai_history": "summary",
			},
		}

		// Reconstruct: system messages + summary + kept turns
		var keepMsgs []*model.Message
		for _, t := range toKeep {
			keepMsgs = append(keepMsgs, t.messages...)
		}

		result := make([]*model.Message, 0, systemEnd+1+len(keepMsgs))
		result = append(result, msgs[:systemEnd]...)
		result = append(result, summaryMsg)
		result = append(result, keepMsgs...)

		return result, nil
	}
}

// parseTurns groups messages into logical turns. A turn starts with a User
// message (query) and includes all subsequent messages (assistant responses
// and tool result exchanges) until the next User query message.
//
// To preserve tool call/result integrity, User messages containing only
// tool_result parts are treated as continuations of the current turn rather
// than the start of a new turn.
func parseTurns(msgs []*model.Message) []turn {
	if len(msgs) == 0 {
		return nil
	}

	var turns []turn
	var current turn

	for _, m := range msgs {
		if m == nil {
			continue
		}
		// A User message starts a new turn UNLESS it contains only tool results,
		// in which case it is a continuation of the prior assistant turn.
		isNewTurn := m.Role == model.ConversationRoleUser && !isToolResultOnly(m)

		if isNewTurn {
			// Start of a new turn - save previous if non-empty
			if len(current.messages) > 0 {
				turns = append(turns, current)
			}
			current = turn{messages: []*model.Message{m}}
		} else {
			// Continue current turn (assistant, tool results, etc.)
			current.messages = append(current.messages, m)
		}
	}

	// Don't forget the last turn
	if len(current.messages) > 0 {
		turns = append(turns, current)
	}

	return turns
}

// isToolResultOnly reports whether a message contains only tool_result parts.
func isToolResultOnly(m *model.Message) bool {
	if m == nil || m.Role != model.ConversationRoleUser || len(m.Parts) == 0 {
		return false
	}
	for _, p := range m.Parts {
		if _, ok := p.(model.ToolResultPart); !ok {
			return false
		}
	}
	return true
}

// formatMessage converts a message to a readable string for summarization.
func formatMessage(m *model.Message) string {
	var sb strings.Builder
	sb.WriteString(string(m.Role))
	sb.WriteString(": ")

	for _, p := range m.Parts {
		switch v := p.(type) {
		case model.TextPart:
			sb.WriteString(v.Text)
		case model.ToolUsePart:
			fmt.Fprintf(&sb, "[Tool Call: %s]", v.Name)
		case model.ToolResultPart:
			sb.WriteString("[Tool Result]")
		case model.ThinkingPart:
			// Skip thinking parts in summary
		}
	}

	return sb.String()
}

// extractResponseText extracts text content from a model response.
func extractResponseText(resp *model.Response) string {
	if resp == nil {
		return ""
	}

	var sb strings.Builder
	for _, msg := range resp.Content {
		for _, p := range msg.Parts {
			if tp, ok := p.(model.TextPart); ok {
				sb.WriteString(tp.Text)
			}
		}
	}

	return strings.TrimSpace(sb.String())
}
