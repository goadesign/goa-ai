// Package transcript provides a minimal, provider‑precise ledger that records
// the canonical conversation needed to rebuild provider payloads (e.g., Bedrock)
// without leaking provider SDK types into workflow state. The ledger stores
// only the essential, JSON‑friendly parts in the exact order in which they
// must be presented to the provider (thinking → tool_use → tool_result).
//
// Design goals (see AGENTS.md):
//   - Provider‑fidelity: preserve ordering/shape required by providers.
//   - Minimalism: store only what is needed to rebuild payloads exactly.
//   - Stateless API: pure methods that are safe for workflow replay.
//   - Provider‑agnostic at rest: convert to/from provider formats at edges.
package transcript

import (
	"encoding/json"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// Part is the canonical provider‑precise content fragment stored by the ledger.
	// Implementations must be one of ThinkingPart, TextPart, ToolUsePart, or
	// ToolResultPart.
	Part interface {
		isPart()
	}

	// ThinkingPart carries provider reasoning. Exactly one variant must be set:
	// either signed plaintext (Text+Signature) or Redacted bytes. Index tracks
	// the provider content block index when available; Final indicates finalization.
	ThinkingPart struct {
		// Text is provider‑issued plaintext reasoning when available.
		Text string
		// Signature is the provider signature that authenticates Text.
		Signature string
		// Redacted holds provider opaque redacted reasoning bytes.
		Redacted []byte
		// Index is the provider content block index (negative if unknown).
		Index int
		// Final marks the finalization of this reasoning block.
		Final bool
	}

	// TextPart carries assistant or user visible text.
	TextPart struct {
		// Text is visible content intended for users.
		Text string
	}

	// ToolUsePart declares a tool invocation by the assistant.
	ToolUsePart struct {
		// ID is the provider tool_use identifier (for correlating tool_result).
		ID string
		// Name is the provider‑visible tool name (sanitized as required).
		Name string
		// Args are the JSON‑encodable tool arguments.
		Args any
	}

	// ToolResultPart communicates a tool result by the user back to the model,
	// correlated via ToolUseID.
	ToolResultPart struct {
		// ToolUseID correlates to a prior assistant ToolUsePart.ID.
		ToolUseID string
		// Content is the JSON‑encodable tool result payload.
		Content any
		// IsError indicates whether the tool invocation failed.
		IsError bool
	}

	// ToolResultSpec describes a single tool_result block for appending user
	// messages in a turn. It is used by AppendUserToolResults to build a single
	// user message containing multiple tool_result parts.
	ToolResultSpec struct {
		ToolUseID string
		Content   any
		IsError   bool
	}

	// Message groups ordered parts under a role for the provider conversation.
	Message struct {
		// Role is one of "assistant", "user", or "system".
		Role string
		// Parts must be in final provider order for this message.
		Parts []Part
		// Meta carries optional provider‑agnostic metadata for diagnostics.
		Meta map[string]any
	}

	// Ledger holds the ordered transcript for the current turn. It records only
	// the minimal set of parts required to rebuild provider payloads with exact
	// ordering (thinking → tool_use → tool_result). It is JSON‑friendly and safe
	// to store in workflow state.
	Ledger struct {
		messages []Message
		// current accumulates the pending assistant message so thinking/text/tool_use
		// can be coalesced before flushing to messages.
		current *Message
	}
)

// NewLedger constructs an empty Ledger ready to record a turn transcript.
func NewLedger() *Ledger {
	return &Ledger{
		messages: make([]Message, 0, 8),
	}
}

// FromModelMessages constructs a ledger initialized with the provided assistant
// messages. Only assistant-role messages contribute to the transcript; other
// roles are ignored.
func FromModelMessages(msgs []*model.Message) *Ledger {
	led := NewLedger()
	for _, msg := range msgs {
		if msg == nil || msg.Role != model.ConversationRoleAssistant {
			continue
		}
		for _, p := range msg.Parts {
			switch v := p.(type) {
			case model.ThinkingPart:
				cp := ThinkingPart{
					Text:      v.Text,
					Signature: v.Signature,
					Index:     v.Index,
					Final:     v.Final,
				}
				if len(v.Redacted) > 0 {
					cp.Redacted = append([]byte(nil), v.Redacted...)
				}
				led.AppendThinking(cp)
			case model.TextPart:
				led.AppendText(v.Text)
			case model.ToolUsePart:
				led.DeclareToolUse(v.ID, v.Name, v.Input)
				// Tool results are not part of assistant messages; they are
				// reconstructed from events or planner results.
			}
		}
	}
	return led
}

// ValidateBedrock verifies critical Bedrock constraints when thinking is enabled:
//   - Any assistant message that contains tool_use must start with thinking.
//   - For each user message containing tool_result, the immediately prior assistant
//     message must contain at least as many tool_use blocks.
//
// It returns a descriptive error when a constraint is violated.
func ValidateBedrock(messages []*model.Message, thinkingEnabled bool) error {
	if len(messages) == 0 {
		return nil
	}
	// Validate assistant → user tooling handshakes. When thinking is enabled,
	// also enforce that assistant messages with tool_use begin with thinking.
	for i, m := range messages {
		if m == nil || m.Role != model.ConversationRoleAssistant {
			continue
		}
		// Detect tool_use presence and optionally enforce thinking-first.
		hasToolUse := false
		for _, p := range m.Parts {
			if _, ok := p.(model.ToolUsePart); ok {
				hasToolUse = true
				break
			}
		}
		if hasToolUse {
			if len(m.Parts) == 0 {
				return errors.New("bedrock: assistant message is empty where tool_use present")
			}
			if thinkingEnabled {
				if _, ok := m.Parts[0].(model.ThinkingPart); !ok {
					return errors.New("bedrock: assistant message with tool_use must start with thinking")
				}
			}
			// The very next message must be a user message containing tool_result
			// blocks that correspond to the tool_use IDs in this assistant message.
			if i+1 >= len(messages) || messages[i+1] == nil || messages[i+1].Role != model.ConversationRoleUser {
				return errors.New("bedrock: expected user tool_result following assistant tool_use")
			}
			next := messages[i+1]
			// Collect tool_use IDs from assistant and tool_result IDs from user.
			useIDs := make(map[string]struct{})
			for _, p := range m.Parts {
				if tu, ok := p.(model.ToolUsePart); ok {
					if tu.ID != "" {
						useIDs[tu.ID] = struct{}{}
					}
				}
			}
			resIDs := make(map[string]struct{})
			for _, p := range next.Parts {
				if tr, ok := p.(model.ToolResultPart); ok {
					if tr.ToolUseID != "" {
						resIDs[tr.ToolUseID] = struct{}{}
					}
				}
			}
			// Count check (cannot exceed).
			if len(resIDs) > len(useIDs) {
				return errors.New("bedrock: tool_result count exceeds prior assistant tool_use count")
			}
			// Subset check (all tool_result IDs must match declared tool_use IDs).
			for id := range resIDs {
				if _, ok := useIDs[id]; !ok {
					return errors.New("bedrock: tool_result id does not match prior assistant tool_use id")
				}
			}
		}
	}
	return nil
}

// BuildMessagesFromEvents reconstructs provider-ready messages from durable
// memory events by replaying them through a Ledger. It returns messages in the
// canonical provider order (assistant thinking → text → tool_use; user tool_result).
func BuildMessagesFromEvents(events []memory.Event) []*model.Message {
	l := NewLedger()
	var pendingResults []ToolResultSpec
	var toolOrder []string
	for _, e := range events {
		switch e.Type {
		case memory.EventAssistantMessage:
			if m, ok := e.Data.(map[string]any); ok {
				if s, ok2 := m["message"].(string); ok2 && s != "" {
					l.AppendText(s)
				}
			}
		case memory.EventToolCall:
			if m, ok := e.Data.(map[string]any); ok {
				var id string
				if v, ok2 := m["tool_call_id"].(string); ok2 {
					id = v
				}
				var name string
				if v, ok2 := m["tool_name"].(string); ok2 {
					name = v
				}
				var payload any
				if v, ok2 := m["payload"]; ok2 {
					payload = v
				}
				if id != "" && name != "" {
					l.DeclareToolUse(id, name, payload)
					toolOrder = append(toolOrder, id)
				}
			}
		case memory.EventToolResult:
			if m, ok := e.Data.(map[string]any); ok {
				var id string
				if v, ok2 := m["tool_call_id"].(string); ok2 {
					id = v
				}
				var isErr bool
				var terr any
				if v, ok2 := m["error"]; ok2 && v != nil {
					isErr = true
					terr = v
				}
				var result any
				if v, ok2 := m["result"]; ok2 {
					result = v
				}
				content := result
				if isErr {
					if result == nil {
						content = map[string]any{
							"error": terr,
						}
					} else {
						content = map[string]any{
							"result": result,
							"error":  terr,
						}
					}
				}
				if id != "" {
					pendingResults = append(pendingResults, ToolResultSpec{
						ToolUseID: id,
						Content:   content,
						IsError:   isErr,
					})
				}
			}
		case memory.EventPlannerNote:
			// Planner notes are not part of provider messages; ignore here.
		case memory.EventUserMessage:
			// User messages are not stored today by the runtime; if present, ignore here.
		case memory.EventThinking:
			if m, ok := e.Data.(map[string]any); ok {
				var tp ThinkingPart
				if v, ok2 := m["text"].(string); ok2 && v != "" {
					tp.Text = v
				}
				if v, ok2 := m["signature"].(string); ok2 && v != "" {
					tp.Signature = v
				}
				if v, ok2 := m["redacted"].([]byte); ok2 && len(v) > 0 {
					tp.Redacted = append([]byte(nil), v...)
				}
				if v, ok2 := m["content_index"].(int); ok2 {
					tp.Index = v
				}
				if v, ok2 := m["final"].(bool); ok2 {
					tp.Final = v
				}
				l.AppendThinking(tp)
			}
		}
	}
	if len(pendingResults) > 0 {
		// Order tool results to match the assistant tool_use declaration order
		// recorded in toolOrder so that provider handshakes are deterministic.
		// Flush the assistant message before appending user tool_results so that
		// the final message sequence is assistant (thinking/text/tool_use)
		// followed by user (tool_result), matching provider expectations.
		l.FlushAssistant()
		byID := make(map[string]ToolResultSpec, len(pendingResults))
		for _, r := range pendingResults {
			if r.ToolUseID == "" {
				continue
			}
			byID[r.ToolUseID] = r
		}
		ordered := make([]ToolResultSpec, 0, len(byID))
		for _, id := range toolOrder {
			if r, ok := byID[id]; ok {
				ordered = append(ordered, r)
				delete(byID, id)
			}
		}
		// Append any remaining results with unknown IDs at the end to preserve
		// observability; this should not happen in normal operation.
		for _, r := range byID {
			ordered = append(ordered, r)
		}
		l.AppendUserToolResults(ordered)
	}
	return l.BuildMessages()
}

// UnmarshalJSON customizes Message decoding so that Parts (which contain
// interface implementations) can be reconstructed from stored JSON.
func (m *Message) UnmarshalJSON(data []byte) error {
	type alias struct {
		Role  string            `json:"Role"`  //nolint:tagliatelle
		Parts []json.RawMessage `json:"Parts"` //nolint:tagliatelle
		Meta  map[string]any    `json:"Meta"`  //nolint:tagliatelle
	}
	var tmp alias
	if err := json.Unmarshal(data, &tmp); err != nil {
		return err
	}
	m.Role = tmp.Role
	m.Meta = tmp.Meta
	if len(tmp.Parts) == 0 {
		m.Parts = nil
		return nil
	}
	m.Parts = make([]Part, 0, len(tmp.Parts))
	for i, raw := range tmp.Parts {
		part, err := decodeLedgerPart(raw)
		if err != nil {
			return fmt.Errorf("decode parts[%d]: %w", i, err)
		}
		m.Parts = append(m.Parts, part)
	}
	return nil
}

// AppendThinking records a structured thinking block and ensures it appears at
// the head of the current assistant message. When a message is not yet open,
// a new assistant message is started.
func (l *Ledger) AppendThinking(tp ThinkingPart) {
	if l.current == nil {
		l.current = &Message{Role: "assistant", Parts: make([]Part, 0, 2)}
	}
	// Ensure all thinking parts stay at the head of the message.
	// Insert this block directly after any existing leading thinking parts.
	if len(l.current.Parts) == 0 {
		l.current.Parts = append(l.current.Parts, tp)
		return
	}
	// Find the end of the leading thinking run (may be zero).
	i := 0
	for i < len(l.current.Parts) {
		if _, ok := l.current.Parts[i].(ThinkingPart); ok {
			i++
			continue
		}
		break
	}
	// Insert tp at position i (which may be 0 to prepend).
	l.current.Parts = append(
		l.current.Parts[:i],
		append(
			[]Part{
				tp,
			},
			l.current.Parts[i:]...,
		)...,
	)
}

// AppendText appends assistant text to the current assistant message. When no
// assistant message is open, a new one is started.
func (l *Ledger) AppendText(text string) {
	if text == "" {
		return
	}
	if l.current == nil {
		l.current = &Message{Role: "assistant", Parts: make([]Part, 0, 1)}
	}
	l.current.Parts = append(l.current.Parts, TextPart{Text: text})
}

// DeclareToolUse appends a tool_use to the current assistant message. The
// caller is responsible for flushing the assistant message at the end of the
// turn so that subsequent user tool_result messages can correlate to the full
// set of tool_use blocks.
func (l *Ledger) DeclareToolUse(id, name string, args any) {
	if l.current == nil {
		l.current = &Message{Role: "assistant", Parts: make([]Part, 0, 1)}
	}
	l.current.Parts = append(l.current.Parts, ToolUsePart{
		ID:   id,
		Name: name,
		Args: args,
	})
}

// FlushAssistant finalizes the current assistant message (if any) and appends
// it to the ledger. It is safe to call when no assistant message is open.
func (l *Ledger) FlushAssistant() {
	l.flushAssistant()
}

// AppendUserToolResults appends a single user message containing tool_result
// parts for the provided specs, preserving their order. Specs with empty
// ToolUseID are ignored.
func (l *Ledger) AppendUserToolResults(results []ToolResultSpec) {
	if len(results) == 0 {
		return
	}
	parts := make([]Part, 0, len(results))
	for _, r := range results {
		if r.ToolUseID == "" {
			continue
		}
		parts = append(parts, ToolResultPart(r))
	}
	if len(parts) == 0 {
		return
	}
	l.messages = append(l.messages, Message{Role: "user", Parts: parts})
}

// BuildMessages flushes the current assistant (if any) and converts the ledger
// to provider‑agnostic model messages suitable for provider adapters.
func (l *Ledger) BuildMessages() []*model.Message {
	// Flush any open assistant.
	l.flushAssistant()
	if len(l.messages) == 0 {
		return nil
	}
	out := make([]*model.Message, 0, len(l.messages))
	for i := range l.messages {
		m := l.messages[i]
		msg := &model.Message{
			Role:  model.ConversationRole(m.Role),
			Parts: make([]model.Part, 0, len(m.Parts)),
			Meta:  m.Meta,
		}
		for _, p := range m.Parts {
			switch v := p.(type) {
			case ThinkingPart:
				if len(v.Redacted) > 0 {
					msg.Parts = append(
						msg.Parts,
						model.ThinkingPart{
							Redacted: append([]byte(nil), v.Redacted...),
							Index:    v.Index,
							Final:    v.Final,
						},
					)
				} else if v.Text != "" && v.Signature != "" {
					msg.Parts = append(
						msg.Parts,
						model.ThinkingPart{
							Text:      v.Text,
							Signature: v.Signature,
							Index:     v.Index,
							Final:     v.Final,
						},
					)
				}
			case TextPart:
				msg.Parts = append(
					msg.Parts,
					model.TextPart{
						Text: v.Text,
					},
				)
			case ToolUsePart:
				msg.Parts = append(
					msg.Parts,
					model.ToolUsePart{
						ID:    v.ID,
						Name:  v.Name,
						Input: v.Args,
					},
				)
			case ToolResultPart:
				msg.Parts = append(
					msg.Parts,
					model.ToolResultPart{
						ToolUseID: v.ToolUseID,
						Content:   v.Content,
						IsError:   v.IsError,
					},
				)
			}
		}
		if len(msg.Parts) > 0 {
			out = append(out, msg)
		}
	}
	return out
}

// IsEmpty reports whether the ledger currently holds any committed or pending parts.
func (l *Ledger) IsEmpty() bool {
	if l == nil {
		return true
	}
	if l.current != nil && len(l.current.Parts) > 0 {
		return false
	}
	return len(l.messages) == 0
}

func decodeLedgerPart(raw json.RawMessage) (Part, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		var text string
		if errText := json.Unmarshal(raw, &text); errText == nil {
			return TextPart{Text: text}, nil
		}
		return nil, fmt.Errorf("decode part object: %w", err)
	}
	if len(obj) == 0 {
		return nil, errors.New("empty part payload")
	}

	if hasAnyKey(obj, "Signature", "Redacted", "Index", "Final") {
		var thinking ThinkingPart
		if err := json.Unmarshal(raw, &thinking); err != nil {
			return nil, fmt.Errorf("decode ThinkingPart: %w", err)
		}
		return thinking, nil
	}

	if _, ok := obj["ToolUseID"]; ok {
		var result ToolResultPart
		if err := json.Unmarshal(raw, &result); err != nil {
			return nil, fmt.Errorf("decode ToolResultPart: %w", err)
		}
		if result.ToolUseID == "" {
			return nil, errors.New("ToolResultPart requires ToolUseID")
		}
		return result, nil
	}

	if _, ok := obj["Name"]; ok {
		var use ToolUsePart
		if err := json.Unmarshal(raw, &use); err != nil {
			return nil, fmt.Errorf("decode ToolUsePart: %w", err)
		}
		if use.Name == "" {
			return nil, errors.New("ToolUsePart requires Name")
		}
		return use, nil
	}

	if _, ok := obj["Text"]; ok {
		var text TextPart
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, fmt.Errorf("decode TextPart: %w", err)
		}
		return text, nil
	}

	return nil, errors.New("unknown part shape")
}

func hasAnyKey(obj map[string]json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		if _, ok := obj[k]; ok {
			return true
		}
	}
	return false
}

func (ThinkingPart) isPart()   {}
func (TextPart) isPart()       {}
func (ToolUsePart) isPart()    {}
func (ToolResultPart) isPart() {}

func (l *Ledger) flushAssistant() {
	if l.current == nil || len(l.current.Parts) == 0 {
		l.current = nil
		return
	}
	l.messages = append(l.messages, *l.current)
	l.current = nil
}
