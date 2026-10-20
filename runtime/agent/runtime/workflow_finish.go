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

	"goa.design/goa-ai/runtime/agent"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/toolstats"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// terminalPlannerState carries the runtime-owned state needed to materialize a
// terminal planner result.
type terminalPlannerState struct {
	result     *PlanResult
	toolEvents []*planner.ToolResult
	usage      model.TokenUsage
}

// finishCurrentPlanResult materializes the current planner result into the
// user-visible RunOutput, preserving streamed transcript recovery and planner
// note publication.
func (r *Runtime) finishCurrentPlanResult(
	ctx context.Context,
	input *RunInput,
	base *workflowConversation,
	st *runLoopState,
	turnID string,
) (*RunOutput, error) {
	if !st.ResponseCommitted {
		return nil, errors.New("cannot finish an uncommitted planner response")
	}
	return r.materializeTerminalPlannerResult(ctx, input, base, turnID, terminalPlannerState{
		result:     st.Result,
		toolEvents: st.ToolEvents,
		usage:      st.AggUsage,
	})
}

// materializeTerminalPlannerResult translates a terminal planner payload into
// the user-visible run output and canonical terminal transcript/events.
func (r *Runtime) materializeTerminalPlannerResult(
	ctx context.Context,
	input *RunInput,
	base *workflowConversation,
	turnID string,
	state terminalPlannerState,
) (*RunOutput, error) {
	executor, _ := input.Subject.Executor.AsAgent()

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

	finalToolResult := finalToolResultEvent(base.RunContext.Tool, result.FinalToolResult)
	if finalToolResult != nil {
		accepted, err := operationResultFromToolEvent(finalToolResult)
		if err != nil {
			return nil, err
		}
		base.finalization.operationResult = accepted
	}
	var finalMsg *model.Message
	if response := terminalResponse(result.FinalResponse, result.FinalToolResult); response != nil {
		finalMsg = response.Message
	}
	for _, note := range result.Notes {
		if err := r.publishHook(
			ctx,
			hooks.NewPlannerNoteEvent(
				base.RunContext.RunID,
				agent.Ident(executor),
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

	toolCount, toolTelemetry, err := completedToolStats(state.toolEvents)
	if err != nil {
		return nil, err
	}

	return &RunOutput{
		Subject: input.Subject,

		RunID:           base.RunContext.RunID,
		Final:           finalMsg,
		FinalToolResult: finalToolResult,
		ToolCount:       toolCount,
		ToolTelemetry:   toolTelemetry,
		Notes:           notes,
		Usage:           &state.usage,
	}, nil
}

func validateTerminalPlanResult(result *PlanResult) error {
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

// completedToolStats includes restored tool results as well as this workflow's
// results, without encoding their payloads again or loading diagnostic history.
func completedToolStats(events []*planner.ToolResult) (int, *telemetry.ToolTelemetry, error) {
	var stats toolstats.Accumulator
	for _, event := range events {
		if err := stats.Add(event.Telemetry); err != nil {
			return 0, nil, fmt.Errorf("completed tool statistics: %w", err)
		}
	}
	count, combined := stats.Result()
	return count, combined, nil
}

// finishAfterSuccessfulToolCompletion completes the run after either a terminal
// tool batch or the run's required completion tool succeeds. It returns the
// successful completion result as FinalToolResult without copying earlier tool
// results, publishing an assistant message, or requesting another planner turn.
func (r *Runtime) finishAfterSuccessfulToolCompletion(
	input *RunInput,
	base *workflowConversation,
	st *runLoopState,
) (*RunOutput, error) {
	finalToolResult, err := r.successfulCompletionToolEvent(input, st.ToolEvents, st.ToolOutputs)
	if err != nil {
		return nil, err
	}
	executor, _ := input.Subject.Executor.AsAgent()
	if err := r.retainCompletionToolResult(base, agent.Ident(executor), finalToolResult); err != nil {
		return nil, err
	}
	toolCount, toolTelemetry, err := completedToolStats(st.ToolEvents)
	if err != nil {
		return nil, err
	}
	return &RunOutput{
		Subject: input.Subject,

		RunID:           base.RunContext.RunID,
		FinalToolResult: finalToolResult,
		ToolCount:       toolCount,
		ToolTelemetry:   toolTelemetry,
		Usage:           &st.AggUsage,
	}, nil
}

// retainCompletionToolResult validates the completed call against the selected
// operation and keeps an independent copy before reporting can fail.
func (r *Runtime) retainCompletionToolResult(base *workflowConversation, executor agent.Ident, finalToolResult *api.ToolEvent) error {
	selected, err := selectedRunTool(base.RunContext, executor, r)
	if err != nil {
		return err
	}
	if selected != nil {
		if _, err := decodeSuccessfulToolResult(*selected, finalToolResult.Result); err != nil {
			return fmt.Errorf("completion result for operation %q: %w", selected.Name, err)
		}
		finalToolResult.Name = selected.Name
	}
	accepted, err := operationResultFromToolEvent(finalToolResult)
	if err != nil {
		return err
	}
	base.finalization.operationResult = accepted
	return nil
}

// successfulCompletionToolEvent returns the one successful tool result that
// ended the run. Earlier ordinary tools and failed completion attempts remain
// only in the Store; multiple successful completion results are ambiguous and
// violate the singular RunOutput contract.
func (r *Runtime) successfulCompletionToolEvent(input *RunInput, events []*planner.ToolResult, outputs []*planner.ToolOutput) (*api.ToolEvent, error) {
	calls, err := recordedToolCalls(outputs)
	if err != nil {
		return nil, err
	}
	var completionTool tools.Ident
	if input.Policy != nil {
		completionTool = input.Policy.CompletionTool
	}
	var final *planner.ToolResult
	for _, event := range events {
		call, err := recordedResultCall(event.Name, event.ToolCallID, calls)
		if err != nil {
			return nil, err
		}
		call.Subject = input.Subject
		spec, ok, err := r.callSpec(call)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, fmt.Errorf("unknown tool %q in completed run output", event.Name)
		}
		if event.Failure != nil || (!spec.TerminalRun && event.Name != completionTool) {
			continue
		}
		if final != nil {
			return nil, errors.New("completed run contains multiple successful terminal tool results")
		}
		final = event
	}
	if final == nil {
		return nil, errors.New("completed run is missing its successful terminal tool result")
	}
	return encodeToolEvent(final, ToolCall{Name: final.Name, ToolCallID: final.ToolCallID}, r)
}

// finalToolResultEvent converts the planner-owned final tool-result envelope
// into the workflow-safe api.ToolEvent shape stored on RunOutput.
func finalToolResultEvent(toolName tools.Ident, result *planner.FinalToolResult) *api.ToolEvent {
	if result == nil {
		return nil
	}
	return &api.ToolEvent{
		Name:       toolName,
		Result:     append(rawjson.Message(nil), result.Result...),
		ServerData: append(rawjson.Message(nil), result.ServerData...),
		Blocks:     result.Blocks.Clone(),
		Bounds:     result.Bounds,
		Failure:    planner.CloneToolFailure(result.Failure),
		Telemetry:  result.Telemetry,
	}
}
