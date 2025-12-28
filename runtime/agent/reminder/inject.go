package reminder

import (
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
)

// InjectMessages returns a copy of messages with the provided reminders
// injected as additional system messages at appropriate attachment points.
//
// The helper follows a conservative strategy:
//   - AttachmentRunStart reminders are grouped into a single system message
//     prepended to the transcript (or merged into the first existing system
//     message when present).
//   - All other reminders are grouped into a single system message inserted
//     immediately before the last user message. When no user message exists,
//     they are appended as a trailing system message.
//
// Reminders are expected to be pre-ordered by priority (e.g., via Engine);
// InjectMessages preserves the relative order it receives.
func InjectMessages(messages []*model.Message, rems []Reminder) []*model.Message {
	if len(rems) == 0 || len(messages) == 0 {
		// Nothing to inject; return original slice.
		return messages
	}
	runStart := make([]Reminder, 0, len(rems))
	perTurn := make([]Reminder, 0, len(rems))
	for _, r := range rems {
		if r.Attachment.Kind == AttachmentRunStart {
			runStart = append(runStart, r)
			continue
		}
		perTurn = append(perTurn, r)
	}
	out := cloneMessages(messages)
	if len(runStart) > 0 {
		out = injectAtRunStart(out, runStart)
	}
	if len(perTurn) > 0 {
		out = injectBeforeLastUser(out, perTurn)
	}
	return out
}

func injectAtRunStart(msgs []*model.Message, rems []Reminder) []*model.Message {
	if len(rems) == 0 {
		return msgs
	}
	text := combineText(rems)
	if text == "" {
		return msgs
	}
	// If the first message is already a system message, prepend a text part
	// rather than inserting a separate message to keep context compact.
	if len(msgs) > 0 && msgs[0] != nil && msgs[0].Role == model.ConversationRoleSystem {
		m := cloneMessage(msgs[0])
		m.Parts = append([]model.Part{model.TextPart{Text: text}}, m.Parts...)
		out := make([]*model.Message, len(msgs))
		out[0] = m
		copy(out[1:], msgs[1:])
		return out
	}
	m := &model.Message{
		Role:  model.ConversationRoleSystem,
		Parts: []model.Part{model.TextPart{Text: text}},
	}
	out := make([]*model.Message, 0, len(msgs)+1)
	out = append(out, m)
	out = append(out, msgs...)
	return out
}

func injectBeforeLastUser(msgs []*model.Message, rems []Reminder) []*model.Message {
	if len(rems) == 0 {
		return msgs
	}
	text := combineText(rems)
	if text == "" {
		return msgs
	}
	lastUser := -1
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i] != nil && msgs[i].Role == model.ConversationRoleUser {
			lastUser = i
			break
		}
	}
	m := &model.Message{
		Role:  model.ConversationRoleSystem,
		Parts: []model.Part{model.TextPart{Text: text}},
	}
	if lastUser == -1 {
		// No user message: append as trailing system message.
		out := append([]*model.Message(nil), msgs...)
		out = append(out, m)
		return out
	}
	// Bedrock constraint: assistant tool_use must be immediately followed by a
	// user tool_result message. Tool results are encoded as user messages, but
	// they are not a "user turn" and must not be separated from the preceding
	// tool_use by injected system reminders.
	//
	// If the last user message contains tool_result blocks, inject after it
	// rather than before it.
	insertAt := lastUser
	if messageHasToolResult(msgs[lastUser]) {
		insertAt = lastUser + 1
	}
	out := make([]*model.Message, 0, len(msgs)+1)
	out = append(out, msgs[:insertAt]...)
	out = append(out, m)
	out = append(out, msgs[insertAt:]...)
	return out
}

func combineText(rems []Reminder) string {
	var out string
	for i := range rems {
		t := formatReminderText(rems[i])
		if t == "" {
			continue
		}
		if out == "" {
			out = t
			continue
		}
		out += "\n\n" + t
	}
	return out
}

// formatReminderText wraps the reminder text in a <system-reminder> block
// when it is non-empty and not already tagged. Callers should pass plain,
// tag-free guidance in Reminder.Text.
func formatReminderText(r Reminder) string {
	t := strings.TrimSpace(r.Text)
	if t == "" {
		return ""
	}
	if strings.Contains(t, "<system-reminder>") {
		return t
	}
	return "<system-reminder>" + t + "</system-reminder>"
}

func cloneMessages(msgs []*model.Message) []*model.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]*model.Message, len(msgs))
	for i, msg := range msgs {
		if msg == nil {
			continue
		}
		out[i] = cloneMessage(msg)
	}
	return out
}

func cloneMessage(msg *model.Message) *model.Message {
	if msg == nil {
		return nil
	}
	parts := make([]model.Part, len(msg.Parts))
	copy(parts, msg.Parts)
	meta := make(map[string]any, len(msg.Meta))
	for k, v := range msg.Meta {
		meta[k] = v
	}
	return &model.Message{
		Role:  msg.Role,
		Parts: parts,
		Meta:  meta,
	}
}

func messageHasToolResult(msg *model.Message) bool {
	if msg == nil || msg.Role != model.ConversationRoleUser {
		return false
	}
	for _, p := range msg.Parts {
		if _, ok := p.(model.ToolResultPart); ok {
			return true
		}
	}
	return false
}
