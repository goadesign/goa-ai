package runtime

// workflow_transcript.go records assistant transcript and tool_use declarations into the
// conversation message list that is fed back into the planner.
//
// Contract:
// - Produces a canonical assistant message that includes only planner-visible
//   tool_use parts for the turn.
// - Appends messages to the PlanInput in the same order used for tool_result
//   correlation.

import (
	"context"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

// appendTranscriptMessages appends canonical transcript messages to the planner
// input and persists the exact delta to the durable run log.
func (r *Runtime) appendTranscriptMessages(
	ctx context.Context,
	agentID agent.Ident,
	base *planner.PlanInput,
	turnID string,
	messages []*model.Message,
) error {
	if len(messages) == 0 {
		return nil
	}
	base.Messages = append(base.Messages, messages...)
	return r.publishTranscriptDelta(
		ctx,
		base.RunContext.RunID,
		agentID,
		base.RunContext.SessionID,
		turnID,
		messages,
	)
}

// appendTerminalAssistantMessage persists the final assistant message as the
// canonical transcript tail for a terminal turn.
func (r *Runtime) appendTerminalAssistantMessage(
	ctx context.Context,
	agentID agent.Ident,
	base *planner.PlanInput,
	turnID string,
	msg *model.Message,
) error {
	if agentMessageText(msg) == "" {
		return nil
	}
	return r.appendTranscriptMessages(ctx, agentID, base, turnID, cloneMessages([]*model.Message{msg}))
}

// recordAssistantTurn merges streamed transcript parts with the declared tool calls
// and appends the resulting assistant messages to the conversation state.
func (r *Runtime) recordAssistantTurn(
	ctx context.Context,
	agentID agent.Ident,
	base *planner.PlanInput,
	transcriptMsgs []*model.Message,
	allowed []planner.ToolRequest,
	turnID string,
) error {
	allowed = r.filterPlannerVisibleToolCalls(allowed)
	if len(transcriptMsgs) == 0 && len(allowed) == 0 {
		return nil
	}
	messages := cloneMessages(transcriptMsgs)
	target := findAssistantMessage(messages)
	if target == nil && len(allowed) > 0 {
		target = &model.Message{Role: model.ConversationRoleAssistant}
		messages = append(messages, target)
	}
	for _, call := range allowed {
		target.Parts = append(target.Parts, model.ToolUsePart{
			ID:    call.ToolCallID,
			Name:  string(call.Name),
			Input: call.Payload,
		})
	}
	return r.appendTranscriptMessages(ctx, agentID, base, turnID, messages)
}

// appendLatePlannerVisibleToolRecordUses appends bookkeeping tool_use parts that
// become planner-visible only after execution produced retryable failures.
func (r *Runtime) appendLatePlannerVisibleToolRecordUses(
	ctx context.Context,
	agentID agent.Ident,
	base *planner.PlanInput,
	records []stepToolRecord,
	turnID string,
) error {
	lateRecords, err := r.filterLatePlannerVisibleToolRecords(records)
	if err != nil {
		return err
	}
	if len(lateRecords) == 0 {
		return nil
	}
	msg := &model.Message{Role: model.ConversationRoleAssistant}
	for _, record := range lateRecords {
		msg.Parts = append(msg.Parts, model.ToolUsePart{
			ID:    record.call.ToolCallID,
			Name:  string(record.call.Name),
			Input: record.call.Payload,
		})
	}
	return r.appendTranscriptMessages(ctx, agentID, base, turnID, []*model.Message{msg})
}

// findAssistantMessage returns the last assistant message in msgs, if any.
func findAssistantMessage(msgs []*model.Message) *model.Message {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i] != nil && msgs[i].Role == model.ConversationRoleAssistant {
			return msgs[i]
		}
	}
	return nil
}

// cloneMessages shallow-copies messages and their parts so callers can mutate
// assistant parts without mutating the original transcript slice.
func cloneMessages(msgs []*model.Message) []*model.Message {
	if len(msgs) == 0 {
		return nil
	}
	out := make([]*model.Message, 0, len(msgs))
	for _, msg := range msgs {
		if msg == nil {
			continue
		}
		parts := make([]model.Part, len(msg.Parts))
		copy(parts, msg.Parts)
		out = append(out, &model.Message{
			Role:  msg.Role,
			Parts: parts,
			Meta:  cloneMetadata(msg.Meta),
		})
	}
	return out
}
