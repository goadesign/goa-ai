// Package runtime provides history management policies for bounding conversation
// context. HistoryPolicy implementations transform message history before each
// planner invocation to prevent unbounded context growth.
package runtime

import (
	"context"
	"errors"
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
	// (PlanStart and PlanResume). Policy errors mean the runtime cannot construct a
	// contract-valid planner transcript and should fail the run.
	HistoryPolicy func(ctx context.Context, msgs []*model.Message, tools []*model.ToolDefinition) ([]*model.Message, error)

	// CompressOption configures the Compress history policy.
	CompressOption func(*compressConfig)

	// HistoryCompressionConfig describes the runtime defaults or overrides for a
	// compression history policy.
	//
	// Compression has two independent decisions:
	//   - CompressAtTurns and CompressAtMaxInputTokens decide when older history
	//     should be summarized. The triggers are ORed.
	//   - KeepMaxTurns and KeepMaxInputTokens decide which newest complete turns
	//     remain exact after summarization. The budgets are ANDed when both are
	//     set.
	//
	// Token counts are computed at runtime with the configured history model.
	// KeepMaxInputTokens never truncates a turn; it keeps newest whole turns until
	// adding the next older turn would exceed the budget.
	HistoryCompressionConfig struct {
		// CompressAtTurns triggers summarization once at least this many logical
		// turns are present. Zero disables the turn-count trigger.
		CompressAtTurns int

		// CompressAtMaxInputTokens triggers summarization once the full
		// provider-visible transcript exceeds this input-token count. Zero
		// disables the token trigger.
		CompressAtMaxInputTokens int

		// KeepMaxTurns caps exact retention to this many newest logical turns.
		// Zero disables the turn-count retention cap.
		KeepMaxTurns int

		// KeepMaxInputTokens caps exact retention by input tokens. The newest
		// turn is always retained; this budget bounds the measured cost of the
		// older whole turns that join it, anchored on the newest tail so the
		// fixed system-prompt and tool-catalog overhead cancels out (unlike
		// CompressAtMaxInputTokens, which counts the full provider-visible
		// request). Zero disables the token retention cap.
		KeepMaxInputTokens int
	}

	// compressConfig carries optional configuration for the Compress policy.
	compressConfig struct {
		// summaryPrompt is the instruction for summarization.
		summaryPrompt string
		// summaryRole determines where to place the summary (system or user).
		summaryRole model.ConversationRole
		// modelClass selects the model family for summarization.
		modelClass model.ModelClass
		// tokenCounter overrides the client counter used for preflight counts.
		tokenCounter model.TokenCounter
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

Provide your summary based on the conversation so far, following this structure and ensuring precision and thoroughness in your response.

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

// WithTokenCounter sets the exact counter used for token-trigger and
// token-retention budgets. This is intended for tests and custom provider
// adapters; production model clients should normally satisfy model.TokenCounter
// themselves.
func WithTokenCounter(counter model.TokenCounter) CompressOption {
	return func(c *compressConfig) {
		c.tokenCounter = counter
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
	return func(_ context.Context, msgs []*model.Message, _ []*model.ToolDefinition) ([]*model.Message, error) {
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

// Compress returns a policy that summarizes older conversation history when cfg
// says either the turn count or the provider-counted input-token budget has been
// exceeded. After summarization it keeps a newest exact tail selected by whole
// logical turns, bounded by KeepMaxTurns, KeepMaxInputTokens, or both.
//
// The policy always preserves:
//   - All System messages at the start of the conversation.
//   - Complete turn boundaries; it never splits user, assistant, tool_use, and
//     tool_result messages that belong to the same logical turn.
//   - Tool use/result integrity in every kept exact turn.
//
// KeepMaxInputTokens is an exact-tail budget, not a truncation budget. The
// newest complete turn is always retained — dropping it would break the
// conversation contract — and the budget bounds the older turns that join it.
// If even the newest turn alone cannot fit under CompressAtMaxInputTokens,
// Compress returns an error: no amount of summarization can produce a planner
// request that would not immediately re-trigger compression.
func Compress(client model.Client, policyCfg HistoryCompressionConfig, opts ...CompressOption) HistoryPolicy {
	runtimeCfg := defaultCompressConfig()
	for _, opt := range opts {
		opt(runtimeCfg)
	}

	return func(ctx context.Context, msgs []*model.Message, tools []*model.ToolDefinition) ([]*model.Message, error) {
		if client == nil {
			return msgs, errors.New("runtime: history compression model is required")
		}
		if len(msgs) == 0 {
			return msgs, nil
		}
		if err := validateHistoryCompressionConfig(policyCfg); err != nil {
			return msgs, err
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

		triggered, err := shouldCompress(ctx, policyCfg, runtimeCfg, client, msgs, tools, len(turns))
		if err != nil {
			return msgs, err
		}
		if !triggered {
			return msgs, nil
		}

		keepStart, err := exactTailStart(ctx, policyCfg, runtimeCfg, client, msgs[:systemEnd], tools, turns)
		if err != nil {
			return msgs, err
		}
		if keepStart <= 0 {
			return msgs, nil
		}

		toCompress := turns[:keepStart]
		toKeep := turns[keepStart:]

		// Build conversation text for summarization
		var sb strings.Builder
		for _, t := range toCompress {
			for _, m := range t.messages {
				sb.WriteString(formatMessage(m))
				sb.WriteString("\n")
			}
		}

		// Call the model to summarize
		summaryPrompt := fmt.Sprintf(runtimeCfg.summaryPrompt, sb.String())
		req := &model.Request{
			ModelClass: runtimeCfg.modelClass,
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
			return msgs, errors.New("runtime: history compression model returned empty summary")
		}

		// Build summary message
		summaryMsg := &model.Message{
			Role: runtimeCfg.summaryRole,
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

func validateHistoryCompressionConfig(cfg HistoryCompressionConfig) error {
	return cfg.Validate()
}

// Validate verifies that the compression config has at least one trigger and one
// exact-retention budget. Zero means "unset" for each individual field.
func (cfg HistoryCompressionConfig) Validate() error {
	if cfg.CompressAtTurns <= 0 && cfg.CompressAtMaxInputTokens <= 0 {
		return errors.New("runtime: history compression requires CompressAtTurns or CompressAtMaxInputTokens")
	}
	if cfg.KeepMaxTurns <= 0 && cfg.KeepMaxInputTokens <= 0 {
		return errors.New("runtime: history compression requires KeepMaxTurns or KeepMaxInputTokens")
	}
	if cfg.CompressAtTurns < 0 {
		return errors.New("runtime: CompressAtTurns must be positive when set")
	}
	if cfg.CompressAtMaxInputTokens < 0 {
		return errors.New("runtime: CompressAtMaxInputTokens must be positive when set")
	}
	if cfg.KeepMaxTurns < 0 {
		return errors.New("runtime: KeepMaxTurns must be positive when set")
	}
	if cfg.KeepMaxInputTokens < 0 {
		return errors.New("runtime: KeepMaxInputTokens must be positive when set")
	}
	if cfg.CompressAtTurns > 0 && cfg.KeepMaxTurns >= cfg.CompressAtTurns {
		return errors.New("runtime: KeepMaxTurns must be less than CompressAtTurns")
	}
	return nil
}

func shouldCompress(
	ctx context.Context,
	cfg HistoryCompressionConfig,
	runtimeCfg *compressConfig,
	client model.Client,
	msgs []*model.Message,
	tools []*model.ToolDefinition,
	turnCount int,
) (bool, error) {
	if cfg.CompressAtTurns > 0 && turnCount >= cfg.CompressAtTurns {
		return true, nil
	}
	if cfg.CompressAtMaxInputTokens <= 0 {
		return false, nil
	}
	count, err := countMessages(ctx, runtimeCfg, client, msgs, tools)
	if err != nil {
		return false, err
	}
	return count.InputTokens > cfg.CompressAtMaxInputTokens, nil
}

// exactTailStart selects the oldest turn index retained exactly. The newest
// turn is always retained: compression cannot drop it without breaking the
// conversation contract, so KeepMaxInputTokens budgets the older turns that
// join it. Every candidate is counted as a full planner-request shape
// (system + turns + tools) — providers such as Bedrock reject token counting
// for tool-bearing transcripts without the tool config — and older turns are
// charged by their measured cost relative to the newest tail, so the fixed
// system-prompt and tool-catalog overhead cancels out of the comparison.
func exactTailStart(
	ctx context.Context,
	cfg HistoryCompressionConfig,
	runtimeCfg *compressConfig,
	client model.Client,
	system []*model.Message,
	tools []*model.ToolDefinition,
	turns []turn,
) (int, error) {
	if len(turns) == 0 {
		return 0, nil
	}
	newestTokens := 0
	if cfg.KeepMaxInputTokens > 0 {
		count, err := countMessages(ctx, runtimeCfg, client, requestShape(system, turns[len(turns)-1:]), tools)
		if err != nil {
			return 0, err
		}
		newestTokens = count.InputTokens
		// If even maximal compression — keeping only the newest turn — cannot
		// fit under the compress trigger, the run cannot construct a planner
		// request that compression would not immediately re-trigger on. Fail
		// loudly with the true invariant instead of silently proceeding.
		if cfg.CompressAtMaxInputTokens > 0 && newestTokens > cfg.CompressAtMaxInputTokens {
			return 0, fmt.Errorf("runtime: newest history turn cannot fit within CompressAtMaxInputTokens (%d > %d): compression keeps the newest turn whole and cannot produce a smaller planner request", newestTokens, cfg.CompressAtMaxInputTokens)
		}
	}
	keepStart := len(turns) - 1
	for i := len(turns) - 2; i >= 0; i -= 1 {
		if cfg.KeepMaxTurns > 0 && len(turns)-i > cfg.KeepMaxTurns {
			break
		}
		if cfg.KeepMaxInputTokens > 0 {
			count, err := countMessages(ctx, runtimeCfg, client, requestShape(system, turns[i:]), tools)
			if err != nil {
				return 0, err
			}
			if count.InputTokens-newestTokens > cfg.KeepMaxInputTokens {
				break
			}
		}
		keepStart = i
	}
	return keepStart, nil
}

// requestShape assembles the planner-request message shape used for token
// counting: the preserved system prefix followed by the candidate tail turns.
func requestShape(system []*model.Message, turns []turn) []*model.Message {
	msgs := make([]*model.Message, 0, len(system)+len(turns))
	msgs = append(msgs, system...)
	return append(msgs, flattenTurns(turns)...)
}

func countMessages(
	ctx context.Context,
	cfg *compressConfig,
	client model.Client,
	msgs []*model.Message,
	tools []*model.ToolDefinition,
) (model.TokenCount, error) {
	counter := cfg.tokenCounter
	if counter == nil {
		var ok bool
		counter, ok = client.(model.TokenCounter)
		if !ok {
			return model.TokenCount{}, errors.New("runtime: history compression token counter is required for token budgets")
		}
	}
	// Counting requests are synthesized from history, so they need the same
	// guarantee the configured model client gives Complete and Stream: when
	// the transcript references a tool absent from the advertised list
	// (unknown-tool recovery records runtime.tool_unavailable), the request
	// tool configuration must still cover it or providers such as Bedrock
	// reject the count.
	req := &model.Request{
		ModelClass: cfg.modelClass,
		Messages:   msgs,
		Tools:      tools,
	}
	ensureToolUnavailableDefinition(req)
	count, err := counter.CountTokens(ctx, req)
	if err != nil {
		return model.TokenCount{}, err
	}
	if !count.Exact {
		return model.TokenCount{}, errors.New("runtime: history compression requires exact token counts")
	}
	return count, nil
}

func flattenTurns(turns []turn) []*model.Message {
	total := 0
	for _, t := range turns {
		total += len(t.messages)
	}
	msgs := make([]*model.Message, 0, total)
	for _, t := range turns {
		msgs = append(msgs, t.messages...)
	}
	return msgs
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
