package runtime

// workflow_finish.go contains “finish” helpers that translate a terminal planner
// result into the user-visible RunOutput and hook events.
//
// Contract:
// - These helpers must preserve the streaming semantics for streamed planners:
//   when the provider streamed content, the final message text may come from the
//   transcript rather than PlanResult.FinalResponse.Message.

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
)

// finishWithoutToolCalls finalizes a plan result when the planner returned no
// tool calls, producing the final assistant message and planner notes.
func (r *Runtime) finishWithoutToolCalls(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	turnID string,
) (*RunOutput, error) {
	result := st.Result
	if result.FinalResponse == nil {
		r.logger.Error(ctx, "ERROR - Neither tool calls nor final response!")
		return nil, fmt.Errorf(
			"CRITICAL: planner returned neither tool calls nor final response - ToolCalls=%d, FinalResponse=%v, Await=%v",
			len(result.ToolCalls),
			result.FinalResponse != nil,
			result.Await != nil,
		)
	}

	finalMsg := result.FinalResponse.Message
	if result.Streamed && agentMessageText(finalMsg) == "" {
		if text := transcriptText(st.Transcript); text != "" {
			finalMsg = newTextAgentMessage(model.ConversationRoleAssistant, text)
		}
	}

	if !result.Streamed {
		if err := r.publishHook(
			ctx,
			hooks.NewAssistantMessageEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				agentMessageText(finalMsg),
				nil,
			),
			turnID,
		); err != nil {
			return nil, err
		}
	}

	for _, note := range result.Notes {
		if err := r.publishHook(
			ctx,
			hooks.NewPlannerNoteEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				note.Text,
				note.Labels,
			),
			turnID,
		); err != nil {
			return nil, err
		}
	}
	notes := make([]*planner.PlannerAnnotation, len(result.Notes))
	for i := range result.Notes {
		notes[i] = &result.Notes[i]
	}

	return &RunOutput{
		AgentID:    input.AgentID,
		RunID:      base.RunContext.RunID,
		Final:      finalMsg,
		ToolEvents: st.ToolEvents,
		Notes:      notes,
		Usage:      &st.AggUsage,
	}, nil
}
