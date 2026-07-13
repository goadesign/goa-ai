package reminder

import (
	"strings"

	"goa.design/goa-ai/runtime/agent/model"
)

// InjectMessages returns a deep copy of messages with the provided reminders
// injected as additional system messages at appropriate attachment points. It
// returns an error when canonical message content cannot be cloned safely.
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
func InjectMessages(messages []*model.Message, rems []Reminder) ([]*model.Message, error) {
	out, err := model.CloneMessages(messages)
	if err != nil {
		return nil, err
	}
	if len(rems) == 0 || len(out) == 0 {
		return out, nil
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
	if len(runStart) > 0 {
		out = injectAtRunStart(out, runStart)
	}
	if len(perTurn) > 0 {
		out = injectBeforeLastUser(out, perTurn)
	}
	return out, nil
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
		msgs[0].Parts = append([]model.Part{model.TextPart{Text: text}}, msgs[0].Parts...)
		return msgs
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
