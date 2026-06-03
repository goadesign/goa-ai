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
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// terminalPlannerState carries the runtime-owned state needed to materialize a
// terminal planner result.
type terminalPlannerState struct {
	result     *planner.PlanResult
	transcript []*model.Message
	toolEvents []*planner.ToolResult
	usage      model.TokenUsage
}

// finishCurrentPlanResult materializes the current planner result into the
// user-visible RunOutput, preserving streamed transcript recovery and planner
// note publication.
func (r *Runtime) finishCurrentPlanResult(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	turnID string,
) (*RunOutput, error) {
	return r.materializeTerminalPlannerResult(ctx, input, base, turnID, terminalPlannerState{
		result:     st.Result,
		transcript: st.Transcript,
		toolEvents: st.ToolEvents,
		usage:      st.AggUsage,
	})
}

// materializeTerminalPlannerResult translates a terminal planner payload into
// the user-visible run output and canonical terminal transcript/events.
func (r *Runtime) materializeTerminalPlannerResult(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	turnID string,
	state terminalPlannerState,
) (*RunOutput, error) {
	result := state.result
	if err := validateTerminalPlanResult(result); err != nil {
		r.logger.Error(ctx, "ERROR - invalid planner terminal result", "err", err)
		if result == nil {
			return nil, err
		}
		return nil, fmt.Errorf(
			"%w - ToolCalls=%d, FinalResponse=%v, FinalToolResult=%v, Await=%v",
			err,
			len(result.ToolCalls),
			result.FinalResponse != nil,
			result.FinalToolResult != nil,
			result.Await != nil,
		)
	}

	var finalMsg *model.Message
	if result.FinalResponse != nil {
		finalMsg = result.FinalResponse.Message
		if result.Streamed && agentMessageText(finalMsg) == "" {
			if text := transcriptText(state.transcript); text != "" {
				finalMsg = newTextAgentMessage(model.ConversationRoleAssistant, text)
			}
		}
	}

	if result.FinalResponse != nil && !result.Streamed {
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
	if err := r.appendTerminalAssistantMessage(ctx, input.AgentID, base, turnID, finalMsg); err != nil {
		return nil, err
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

	toolEvents, err := r.encodeToolEvents(ctx, state.toolEvents)
	if err != nil {
		return nil, err
	}

	finalToolResult := finalToolResultEvent(base.RunContext.Tool, result.FinalToolResult)
	return &RunOutput{
		AgentID:         input.AgentID,
		RunID:           base.RunContext.RunID,
		Final:           finalMsg,
		FinalToolResult: finalToolResult,
		ToolEvents:      toolEvents,
		Notes:           notes,
		Usage:           &state.usage,
	}, nil
}

func validateTerminalPlanResult(result *planner.PlanResult) error {
	if result == nil {
		return errors.New("planner returned nil terminal result")
	}
	if result.FinalResponse == nil && result.FinalToolResult == nil {
		return errors.New("planner returned neither FinalResponse nor FinalToolResult")
	}
	if result.FinalResponse != nil && result.FinalToolResult != nil {
		return errors.New("planner returned both FinalResponse and FinalToolResult")
	}
	if result.Await != nil {
		return errors.New("planner returned await alongside terminal payload")
	}
	return nil
}

// finishAfterTerminalToolCalls completes the run after a tool turn whose executed
// tools are declared terminal (ToolSpec.TerminalRun). It returns a RunOutput with
// tool events but does not publish an assistant message event or request any
// follow-up PlanResume/finalization turn. Final remains nil because the run
// ended by terminal tool contract, not by planner-authored assistant text.
func (r *Runtime) finishAfterTerminalToolCalls(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
) (*RunOutput, error) {
	toolEvents, err := r.encodeToolEvents(ctx, st.ToolEvents)
	if err != nil {
		return nil, err
	}
	return &RunOutput{
		AgentID:    input.AgentID,
		RunID:      base.RunContext.RunID,
		ToolEvents: toolEvents,
		Usage:      &st.AggUsage,
	}, nil
}

// finalToolResultEvent converts the planner-owned final tool-result envelope
// into the workflow-safe api.ToolEvent shape stored on RunOutput.
func finalToolResultEvent(toolName tools.Ident, result *planner.FinalToolResult) *api.ToolEvent {
	if result == nil {
		return nil
	}
	return &api.ToolEvent{
		Name:                toolName,
		Result:              append(rawjson.Message(nil), result.Result...),
		ResultBytes:         result.ResultBytes,
		ResultOmitted:       result.ResultOmitted,
		ResultOmittedReason: result.ResultOmittedReason,
		ServerData:          append(rawjson.Message(nil), result.ServerData...),
		Bounds:              result.Bounds,
		Error:               result.Error,
		RetryHint:           result.RetryHint,
		Telemetry:           result.Telemetry,
	}
}
