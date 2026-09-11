// Package runtime provides history management policies for bounding conversation
// context. Policies bound the messages in each actual model request using the
// destination client's counter, without changing saved conversation history.
package runtime

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// HistoryPolicy transforms the messages in one model request. Implementations
	// must:
	//   - Preserve every System message exactly and in its original order
	//     relative to the other retained messages, wherever it appears.
	//   - Keep each complete assistant response with its tool results and any
	//     user request that introduced the response.
	//   - Maintain ToolUse/ToolResult integrity (never orphan a result without
	//     its call).
	//
	// The runtime supplies the complete request and its destination client's
	// counter immediately before Complete or Stream. A prior summary has matching
	// original source contents and positions; the policy must still verify that
	// its meaning is valid for this request. Policies must preserve every request
	// field other than Messages. Errors prevent provider invocation.
	HistoryPolicy func(ctx context.Context, req *model.Request, counter model.TokenCounter, prior *HistorySummary) (HistoryResult, error)

	// HistoryResult contains the messages prepared for one model request.
	HistoryResult struct {
		// Messages preserves original instructions and complete retained turns.
		Messages []*model.Message
		// Summary optionally describes the exact older prefix replaced in Messages.
		// Its meaning must depend only on its declared source and policy fingerprint.
		// Policies whose transformations cannot make that promise leave it nil.
		Summary *HistorySummary
	}

	// HistorySummary describes original conversation replaced by a summary.
	// The runtime verifies its source; the current policy owns semantic validity.
	HistorySummary = api.HistorySummary

	// CompressOption configures the Compress history policy.
	CompressOption func(*compressConfig)

	// HistoryCompressionConfig describes the runtime defaults or overrides for a
	// compression history policy.
	//
	// Compression has two independent decisions:
	//   - CompressAtTurns and CompressAtMaxInputTokens decide when older history
	//     should be summarized. The triggers are ORed.
	//   - KeepMaxTurns and KeepMaxInputTokens bound which newest complete turns
	//     are eligible to remain exact. The budgets are ANDed when both are set.
	//     A positive total ceiling may require fewer older exact turns once the
	//     actual summary is included.
	//
	// Token counts come from the client receiving the request, not the summary
	// model. Estimated budgets do not guarantee provider context-window fit.
	// KeepMaxInputTokens never truncates a turn; it keeps newest whole turns until
	// adding the next older turn would exceed the budget.
	HistoryCompressionConfig struct {
		// AllowEstimatedTokens permits the destination client's declared estimate.
		// False requires exact counts. Counting errors always remain errors.
		AllowEstimatedTokens bool

		// CompressAtTurns triggers summarization once at least this many logical
		// turns are present. Zero disables the turn-count trigger.
		CompressAtTurns int

		// CompressAtMaxInputTokens triggers summarization once the destination
		// model counts the complete request above this input-token
		// count. The threshold applies to one history-policy invocation and is
		// exclusive: a count equal to the threshold fits. A compressed result
		// must also fit after adding the summary. Zero disables it.
		CompressAtMaxInputTokens int

		// KeepMaxTurns caps exact retention to this many newest logical turns.
		// Zero disables the turn-count retention cap.
		KeepMaxTurns int

		// KeepMaxInputTokens caps exact retention by input tokens. The newest
		// turn is always retained; this budget bounds the measured cost of the
		// older whole turns that join it, anchored on the newest tail so the
		// fixed system-prompt and tool-catalog overhead cancels out (unlike
		// CompressAtMaxInputTokens, which counts the complete history-policy
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
	}

	// turn holds one complete assistant response, its tool results and reminders,
	// and the user request that introduced it, when present. A later response
	// after tool results starts another turn even without a new user request.
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
// a %s placeholder where the complete quoted textual history and source-position
// references will be inserted. Native images and documents follow in user
// messages grouped by their original messages, within the same completion.
// With a positive CompressAtMaxInputTokens, the supplied history includes every
// turn older than newest, even if some remain exact. Without that ceiling, only
// the prefix excluded from exact retention is supplied.
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
// conversation history. A turn contains a complete contiguous assistant response
// and its following tool results and reminders. A user request stays with its
// first response; later responses after tool results start separate turns.
//
// The policy always preserves:
//   - All System messages, in their original order among retained messages
//   - Complete turn boundaries (never splits a user query from its response)
//   - Tool use/result integrity (keeps results with their corresponding calls)
//
// Example: KeepRecentTurns(5) keeps the last 5 complete response/result exchanges,
// even when one user request started all of them. A pending user request counts
// as a turn and remains intact.
func KeepRecentTurns(n int) HistoryPolicy {
	return func(_ context.Context, req *model.Request, _ model.TokenCounter, _ *HistorySummary) (HistoryResult, error) {
		msgs := req.Messages
		if n <= 0 || len(msgs) == 0 {
			return HistoryResult{Messages: msgs}, nil
		}

		systemEnd := systemPrefixEnd(msgs)

		// If everything is system messages, return as-is
		if systemEnd >= len(msgs) {
			return HistoryResult{Messages: msgs}, nil
		}

		// Parse remaining messages into turns
		history := msgs[systemEnd:]
		turns := parseTurns(history)

		// Keep only the last N turns
		if len(turns) <= n {
			return HistoryResult{Messages: msgs}, nil
		}

		return HistoryResult{Messages: requestShape(msgs[:systemEnd], turns, len(turns)-n)}, nil
	}
}

// Compress returns a policy that summarizes older conversation history when cfg
// says either the turn count or the selected input-token budget has been
// exceeded. KeepMaxTurns and KeepMaxInputTokens bound eligible exact retention.
// With a positive total token ceiling, one summary covers all turns older than
// newest; the runtime counts that summary with successively shorter eligible
// whole-turn tails and returns the longest one that fits. Some summarized turns
// may also remain exact. Without that ceiling, only the excluded prefix is
// summarized and the selected exact tail stays unchanged.
//
// An eligible prior summary is reconstructed before evaluating the triggers.
// Each call counts its actual destination request. A replacement summary uses
// original evidence only when additional coverage is needed; fitting a smaller
// exact tail never asks the model to summarize the same evidence again.
//
// The policy always preserves:
//   - All System messages, exactly and in their original relative order.
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

	// The fingerprint describes how evidence becomes summary text, not which
	// destination or provider happens to generate or consume that text.
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf(
		"history-summary-v2\n%q\n%q\n%q", historySummaryInstruction, runtimeCfg.summaryPrompt, runtimeCfg.summaryRole,
	))))
	return func(ctx context.Context, request *model.Request, counter model.TokenCounter, prior *HistorySummary) (HistoryResult, error) {
		msgs := request.Messages
		original := HistoryResult{Messages: msgs}
		if client == nil {
			return original, errors.New("runtime: history compression model is required")
		}
		if err := model.ValidateClient(client); err != nil {
			return original, fmt.Errorf("runtime: history compression model: %w", err)
		}
		if len(msgs) == 0 {
			return original, nil
		}
		if err := validateHistoryCompressionConfig(policyCfg); err != nil {
			return original, err
		}

		systemEnd := systemPrefixEnd(msgs)

		// If everything is system messages, return as-is
		if systemEnd >= len(msgs) {
			return original, nil
		}

		// Parse remaining messages into turns
		history := msgs[systemEnd:]
		turns := parseTurns(history)
		candidate := request
		if prior != nil {
			_, currentFingerprint, err := historySummarySource(msgs, prior.SourceMessages, fingerprint)
			if err != nil {
				return original, err
			}
			if prior.PolicyFingerprint != currentFingerprint {
				prior = nil
			}
		}
		if prior != nil {
			messages, err := historySummaryMessages(msgs, prior)
			if err != nil {
				return original, err
			}
			copy := *request
			copy.Messages = messages
			candidate = &copy
		}
		candidateTurns := turns
		if prior != nil {
			candidateTurns = parseTurns(candidate.Messages[systemPrefixEnd(candidate.Messages):])
		}
		triggered, err := shouldCompress(ctx, policyCfg, counter, candidate, len(candidateTurns))
		if err != nil {
			return original, err
		}
		if !triggered {
			return HistoryResult{Messages: candidate.Messages, Summary: prior}, nil
		}

		keepStart, err := exactTailStart(ctx, policyCfg, counter, request, msgs[:systemEnd], turns)
		if err != nil {
			return original, err
		}
		if keepStart <= 0 {
			return original, nil
		}
		if prior != nil {
			result, fits, err := fitHistorySummary(ctx, policyCfg, counter, request, turns, keepStart, prior)
			if err != nil {
				return original, err
			}
			if fits {
				return result, nil
			}
		}

		toCompress := turns[:keepStart]
		if policyCfg.CompressAtMaxInputTokens > 0 {
			// Any optional older turn may need removal after the summary is
			// counted. Supply all of them before making the one summary call.
			toCompress = turns[:len(turns)-1]
		}

		// Supply the selected older evidence once, keeping media native and
		// historical tools quoted rather than executable in this summary call.
		sourceCount := historyConversationCount(toCompress)
		source, sourceFingerprint, err := historySummarySource(msgs, sourceCount, fingerprint)
		if err != nil {
			return original, err
		}
		req, documentSources, err := historySummaryRequest(source, runtimeCfg)
		if err != nil {
			return original, err
		}

		resp, err := client.Complete(ctx, req)
		if err != nil {
			return original, err
		}

		// Preserve generated sentences and any supplied citation fields as text.
		summaryText, err := renderHistorySummary(resp, documentSources)
		if err != nil {
			return original, err
		}

		// Build summary message
		summaryMsg := model.Message{
			Role: runtimeCfg.summaryRole,
			Parts: []model.Part{
				model.TextPart{Text: "[Conversation Summary]\n" + summaryText},
			},
			Meta: map[string]any{
				"goa_ai_history": "summary",
			},
		}
		summary := &HistorySummary{
			SourceMessages:    sourceCount,
			Message:           summaryMsg,
			PolicyFingerprint: sourceFingerprint,
		}

		result, _, err := fitHistorySummary(ctx, policyCfg, counter, request, turns, keepStart, summary)
		if err != nil {
			return original, err
		}
		return result, nil
	}
}

// fitHistorySummary counts whole-turn candidates using existing summary text.
// False means new original evidence must enter a replacement summary before
// more messages can be removed. An error means counting failed or even the
// summary and newest complete turn cannot fit; neither causes another summary.
func fitHistorySummary(ctx context.Context, cfg HistoryCompressionConfig, counter model.TokenCounter, request *model.Request, turns []turn, keepStart int, summary *HistorySummary) (HistoryResult, bool, error) {
	for ; keepStart < len(turns); keepStart++ {
		replaced := historyConversationCount(turns[:keepStart])
		if replaced > summary.SourceMessages {
			return HistoryResult{}, false, nil
		}
		selected := *summary
		selected.ReplacedMessages = replaced
		messages, err := historySummaryMessages(request.Messages, &selected)
		if err != nil {
			return HistoryResult{}, false, err
		}
		result := HistoryResult{Messages: messages, Summary: &selected}
		if cfg.CompressAtMaxInputTokens == 0 {
			return result, true, nil
		}
		count, err := countMessages(ctx, cfg, counter, request, messages)
		if err != nil {
			return HistoryResult{}, false, err
		}
		if count.InputTokens <= cfg.CompressAtMaxInputTokens {
			return result, true, nil
		}
		if keepStart == len(turns)-1 {
			return HistoryResult{}, false, fmt.Errorf(
				"runtime: compressed history exceeds CompressAtMaxInputTokens (%d > %d): the generated summary and newest exact turns do not fit",
				count.InputTokens, cfg.CompressAtMaxInputTokens,
			)
		}
	}
	return HistoryResult{}, false, nil
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
	counter model.TokenCounter,
	request *model.Request,
	turnCount int,
) (bool, error) {
	if cfg.CompressAtTurns > 0 && turnCount >= cfg.CompressAtTurns {
		return true, nil
	}
	if cfg.CompressAtMaxInputTokens <= 0 {
		return false, nil
	}
	count, err := countMessages(ctx, cfg, counter, request, request.Messages)
	if err != nil {
		return false, err
	}
	return count.InputTokens > cfg.CompressAtMaxInputTokens, nil
}

// exactTailStart selects the oldest turn eligible for exact retention before
// the summary is added. The final count may require fewer older turns. The newest
// turn is always retained: compression cannot drop it without breaking the
// conversation contract, so KeepMaxInputTokens budgets the older turns that
// join it. Each candidate keeps the complete destination request and replaces
// only Messages with the preserved system messages and selected whole turns.
// Older turns are charged relative to the newest tail, so fixed request costs
// such as the system prompt and tool definitions cancel out of the comparison.
func exactTailStart(
	ctx context.Context,
	cfg HistoryCompressionConfig,
	counter model.TokenCounter,
	request *model.Request,
	system []*model.Message,
	turns []turn,
) (int, error) {
	if len(turns) == 0 {
		return 0, nil
	}
	newestTokens := 0
	if cfg.KeepMaxInputTokens > 0 || cfg.CompressAtMaxInputTokens > 0 {
		count, err := countMessages(ctx, cfg, counter, request, requestShape(system, turns, len(turns)-1))
		if err != nil {
			return 0, err
		}
		newestTokens = count.InputTokens
		// If even maximal compression — keeping only the newest turn — cannot
		// fit under the compress trigger, the run cannot construct a planner
		// request that compression would not immediately re-trigger on. Fail
		// loudly with the true invariant instead of silently proceeding.
		if cfg.CompressAtMaxInputTokens > 0 && newestTokens > cfg.CompressAtMaxInputTokens {
			systemCount := 0
			for _, message := range request.Messages {
				if message.Role == model.ConversationRoleSystem {
					systemCount++
				}
			}
			return 0, fmt.Errorf(
				"runtime: newest history turn cannot fit within CompressAtMaxInputTokens (%d > %d; system messages=%d, tools=%d, turns=%d, newest turn messages=%d): compression keeps the newest turn whole and cannot produce a smaller planner request",
				newestTokens, cfg.CompressAtMaxInputTokens,
				systemCount, len(request.Tools), len(turns), len(turns[len(turns)-1].messages),
			)
		}
	}
	keepStart := len(turns) - 1
	for i := len(turns) - 2; i >= 0; i -= 1 {
		if cfg.KeepMaxTurns > 0 && len(turns)-i > cfg.KeepMaxTurns {
			break
		}
		if cfg.KeepMaxInputTokens > 0 || cfg.CompressAtMaxInputTokens > 0 {
			count, err := countMessages(ctx, cfg, counter, request, requestShape(system, turns, i))
			if err != nil {
				return 0, err
			}
			if cfg.KeepMaxInputTokens > 0 &&
				count.InputTokens-newestTokens > cfg.KeepMaxInputTokens {
				break
			}
			if cfg.CompressAtMaxInputTokens > 0 &&
				count.InputTokens > cfg.CompressAtMaxInputTokens {
				break
			}
		}
		keepStart = i
	}
	return keepStart, nil
}

// requestShape assembles a candidate without moving original instructions.
// Removed turns lose only conversational messages; their System messages stay
// in order before the retained complete turns. System messages inside retained
// turns keep their exact placement. The same selection supplies every count.
func requestShape(prefix []*model.Message, turns []turn, keepStart int) []*model.Message {
	msgs := make([]*model.Message, 0, len(prefix)+len(turns))
	msgs = append(msgs, prefix...)
	for i, turn := range turns {
		for _, message := range turn.messages {
			if i >= keepStart || message.Role == model.ConversationRoleSystem {
				msgs = append(msgs, message)
			}
		}
	}
	return msgs
}

func countMessages(
	ctx context.Context,
	cfg HistoryCompressionConfig,
	counter model.TokenCounter,
	request *model.Request,
	msgs []*model.Message,
) (model.TokenCount, error) {
	// Preserve the actual destination, tools, output budget, thinking and cache
	// options. Only the candidate conversation changes during retention search.
	req := *request
	req.Messages = msgs
	count, err := counter.CountTokens(ctx, &req)
	if err != nil {
		if errors.Is(err, model.ErrTokenCountingUnsupported) {
			return model.TokenCount{}, fmt.Errorf(
				"runtime: history compression requires a model provider with token counting: %w",
				err,
			)
		}
		return model.TokenCount{}, err
	}
	if count.InputTokens < 0 {
		return model.TokenCount{}, errors.New("runtime: history counter returned a negative input token count")
	}
	if !cfg.AllowEstimatedTokens && !count.Exact {
		return model.TokenCount{}, errors.New("runtime: history compression requires exact token counts")
	}
	return count, nil
}

// parseTurns keeps each contiguous assistant response with its following tool
// results and reminders. Providers may split one response into several messages
// for reasoning, text, or parallel calls; none is an independent history turn.
// A new user request or a later response after results starts the next turn.
// This groups existing messages only: transcript validation owns call/result
// matching, and no message, part, signature, or order is changed here.
func parseTurns(msgs []*model.Message) []turn {
	if len(msgs) == 0 {
		return nil
	}

	var turns []turn
	var current turn

	hasAssistant := false
	var previousRole model.ConversationRole
	for _, m := range msgs {
		if m == nil {
			continue
		}
		// A result message belongs to the response it answers, including when
		// it also carries text. Only an ordinary user request starts a turn.
		isNewTurn := m.Role == model.ConversationRoleUser && !hasToolResult(m)
		// Keep adjacent assistant messages together, but allow many completed
		// response/result exchanges after a single user kickoff to be bounded.
		if m.Role == model.ConversationRoleAssistant && hasAssistant && previousRole != model.ConversationRoleAssistant {
			isNewTurn = true
		}
		if isNewTurn {
			hasAssistant = m.Role == model.ConversationRoleAssistant
		} else if m.Role == model.ConversationRoleAssistant {
			hasAssistant = true
		}
		previousRole = m.Role

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

// hasToolResult distinguishes a response's result message from a new user
// request. The transcript contract permits text alongside tool results.
func hasToolResult(m *model.Message) bool {
	for _, p := range m.Parts {
		if _, ok := p.(model.ToolResultPart); ok {
			return true
		}
	}
	return false
}
