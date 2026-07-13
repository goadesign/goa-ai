package runtime

// workflow_transcript.go records assistant transcript and tool_use declarations into the
// conversation message list that is fed back into the planner.
//
// Contract:
// - Produces the complete provider assistant response, including every tool_use
//   part in provider order.
// - Appends messages to the PlanInput in the same order used for tool_result
//   correlation.

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
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
	owned, err := model.CloneMessages(messages)
	if err != nil {
		return err
	}
	if err := r.publishTranscriptDelta(
		ctx,
		base.RunContext.RunID,
		agentID,
		base.RunContext.SessionID,
		turnID,
		owned,
	); err != nil {
		return err
	}
	base.Messages = append(base.Messages, owned...)
	return nil
}

// appendSelectedModelResponse persists one selected planner turn. Captured
// provider responses are appended unchanged; planner-authored turns are built
// once from the result's domain values. The workflow loop owns the exactly-once
// state transition around this persistence operation.
func (r *Runtime) appendSelectedModelResponse(
	ctx context.Context,
	agentID agent.Ident,
	base *planner.PlanInput,
	turnID string,
	result *planner.PlanResult,
	transcript []*model.Message,
) error {
	messages := transcript
	if len(messages) == 0 {
		var err error
		messages, err = plannerAuthoredResponseMessages(result)
		if err != nil {
			return err
		}
	}
	return r.appendTranscriptMessages(ctx, agentID, base, turnID, messages)
}

// commitSelectedModelResponse performs the workflow's sole transition from an
// uncommitted planner result to a durable provider transcript.
func (l *workflowLoop) commitSelectedModelResponse(result *planner.PlanResult) error {
	if l.st.ResponseCommitted {
		return errors.New("workflow planner response was committed more than once")
	}
	if err := l.r.appendSelectedModelResponse(
		l.wfCtx.Context(),
		l.input.AgentID,
		l.base,
		l.turnID,
		result,
		l.st.Transcript,
	); err != nil {
		return err
	}
	l.st.ResponseCommitted = true
	return nil
}

// plannerAuthoredResponseMessages builds the transcript shape for a result
// that did not select a provider response.
func plannerAuthoredResponseMessages(result *planner.PlanResult) ([]*model.Message, error) {
	if result == nil {
		return nil, nil
	}
	var messages []*model.Message
	if result.FinalResponse != nil && result.FinalResponse.Message != nil {
		var err error
		messages, err = model.CloneMessages([]*model.Message{result.FinalResponse.Message})
		if err != nil {
			return nil, err
		}
	}
	calls := planResultModelToolCalls(result)
	if len(calls) == 0 {
		return messages, nil
	}
	var target *model.Message
	if len(messages) > 0 && messages[len(messages)-1].Role == model.ConversationRoleAssistant {
		target = messages[len(messages)-1]
	} else {
		target = &model.Message{Role: model.ConversationRoleAssistant}
		messages = append(messages, target)
	}
	for _, call := range calls {
		target.Parts = append(target.Parts, model.ToolUsePart{
			ID:    call.id,
			Name:  string(call.name),
			Input: append(rawjson.Message(nil), call.payload...),
		})
	}
	return messages, nil
}
