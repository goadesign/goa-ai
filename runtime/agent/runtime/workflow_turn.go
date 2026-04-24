package runtime

// workflow_turn.go contains the implementation of a single “tool turn” inside the
// durable workflow plan loop.
//
// Contract:
// - The function in this file is replay-safe: it uses workflow time and publishes
//   hook events deterministically based on inputs.
// - It owns the mechanics of taking planner ToolCalls through policy/confirmation,
//   recording the assistant tool_use turn, executing tools, and producing the next
//   PlanResume request (or finalizing).
// - It may also handle “mixed” turns where the planner returns ToolCalls plus an
//   await handshake, or where executed tools emit runtime-owned pauses from the
//   current batch (execute tools first, then pause once).

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/planner"
)

type toolTurnResolution uint8

const (
	toolTurnResolutionResume toolTurnResolution = iota
	toolTurnResolutionFinishCurrent
	toolTurnResolutionFinishTerminal
)

// handleToolTurn executes the planner-returned tool calls for the current turn
// and advances the workflow to the next planner result.
//
// Return contract:
//   - **out != nil**: the run is complete (success/finalized) and the caller must return.
//   - **out == nil && err == nil**: the turn was executed and st was advanced to the next
//     planner result; the caller should continue the loop.
func (r *Runtime) handleToolTurn(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	resumeOpts engine.ActivityOptions,
	toolOpts engine.ActivityOptions,
	deadlines *runDeadlines,
	turnID string,
	parentTracker *childTracker,
	ctrl *interrupt.Controller,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	result := st.Result
	if deadlines == nil {
		return nil, errors.New("missing run deadlines")
	}

	if !deadlines.Budget.IsZero() && wfCtx.Now().After(deadlines.Budget) {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.ToolOutputs, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonTimeBudget, deadlines.Hard)
		return out, err
	}

	candidates := result.ToolCalls
	r.logger.Info(ctx, "Workflow received tool calls from planner", "count", len(candidates))
	var err error
	candidates, err = r.rewriteUnknownToolCalls(candidates)
	if err != nil {
		return nil, err
	}
	candidates, err = r.applyPerRunOverrides(ctx, input, candidates)
	if err != nil {
		return nil, err
	}
	allowed, nextCaps, err := r.applyRuntimePolicy(ctx, base, input, candidates, st.Caps, turnID, result.RetryHint)
	if err != nil {
		return nil, err
	}
	st.Caps = nextCaps
	if len(allowed) == 0 {
		r.logger.Error(ctx, "ERROR - No tools allowed for execution after filtering", "candidates", len(result.ToolCalls))
		return nil, errors.New("no tools allowed for execution")
	}

	r.logger.Info(ctx, "Executing allowed tool calls", "count", len(allowed))
	if parentTracker != nil {
		ids := collectToolCallIDs(allowed)
		if len(ids) > 0 && parentTracker.registerDiscovered(ids) {
			if base.RunContext.ParentRunID == "" || base.RunContext.ParentAgentID == "" {
				return nil, fmt.Errorf("nested run is missing parent run context")
			}
			if err := r.publishHook(
				ctx,
				hooks.NewToolCallUpdatedEvent(
					base.RunContext.ParentRunID,
					base.RunContext.ParentAgentID,
					base.RunContext.SessionID,
					parentTracker.parentToolCallID,
					parentTracker.currentTotal(),
				),
				turnID,
			); err != nil {
				return nil, err
			}
			parentTracker.markUpdated()
		}
	}

	allowed, budgetCost := r.capAllowedCalls(allowed, st.Caps)
	if len(allowed) == 0 {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.ToolOutputs, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonToolCap, deadlines.Hard)
		return out, err
	}
	allowed = r.prepareAllowedCallsMetadata(input.AgentID, base, allowed, parentTracker)

	toExecute, confirmations, err := r.splitConfirmationCalls(ctx, base, allowed)
	if err != nil {
		return nil, err
	}
	if len(confirmations) > 0 && ctrl == nil {
		return nil, fmt.Errorf("confirmation required but interrupts are not available")
	}
	if len(toExecute) > 0 {
		if err := r.recordAssistantTurn(ctx, input.AgentID, base, st.Transcript, toExecute, turnID); err != nil {
			return nil, err
		}
	}

	execCalls := make([]planner.ToolRequest, len(toExecute))
	for i := range toExecute {
		call := toExecute[i]
		if call.ToolCallID == "" {
			call.ToolCallID = generateDeterministicToolCallID(base.RunContext.RunID, call.TurnID, base.RunContext.Attempt, call.Name, i)
		}
		execCalls[i] = call
	}

	grouped, timeouts := r.groupToolCallsByTimeout(execCalls, input, toolOpts.StartToCloseTimeout)
	finishBy := time.Time{}
	if !deadlines.Hard.IsZero() {
		finishBy = deadlines.Hard.Add(-deadlines.finalizeReserve())
	}
	outcomes, timedOut, err := r.executeGroupedToolCalls(wfCtx, reg, input.AgentID, base, result.ExpectedChildren, parentTracker, finishBy, grouped, timeouts, toolOpts)
	if err != nil {
		return nil, err
	}
	vals := toolResultsFromExecutions(outcomes)
	toolPauses := toolPausesFromExecutions(outcomes)
	lastToolResults := vals
	st.ToolEvents = append(st.ToolEvents, cloneToolResults(vals)...)
	if result.Await != nil && len(result.Await.Items) > 0 && len(toolPauses) > 0 {
		return nil, fmt.Errorf("planner await and tool pause cannot both be present in the same turn")
	}
	hasAwaitWork := len(confirmations) > 0 || (result.Await != nil && len(result.Await.Items) > 0) || len(toolPauses) > 0
	turnResolution := toolTurnResolutionResume
	if hasAwaitWork {
		if err := r.appendPlannerVisibleTurnResults(ctx, input, base, st, turnID, toExecute, vals); err != nil {
			return nil, err
		}
	} else {
		turnResolution, err = r.classifyToolTurn(toExecute, vals, result)
		if err != nil {
			return nil, err
		}
		if turnResolution == toolTurnResolutionResume {
			if err := r.appendPlannerVisibleTurnResults(ctx, input, base, st, turnID, toExecute, vals); err != nil {
				return nil, err
			}
		}
	}
	if timedOut {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.ToolOutputs, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonTimeBudget, deadlines.Hard)
		return out, err
	}
	st.Caps.RemainingToolCalls = decrementCap(st.Caps.RemainingToolCalls, budgetCost)

	if hasAwaitWork {
		items := make([]planner.AwaitItem, 0, len(toolPauses))
		if result.Await != nil {
			items = append(items, result.Await.Items...)
		}
		if len(toolPauses) > 0 {
			pauseItems, err := toolPauseAwaitItems(toolPauses)
			if err != nil {
				return nil, err
			}
			items = append(items, pauseItems...)
		}
		out, err := r.handleAwaitQueue(
			wfCtx,
			reg,
			input,
			base,
			st,
			resumeOpts,
			toolOpts,
			result.ExpectedChildren,
			parentTracker,
			ctrl,
			deadlines,
			turnID,
			confirmations,
			items,
			lastToolResults,
		)
		if err != nil {
			return nil, err
		}
		return out, nil
	}

	switch turnResolution {
	case toolTurnResolutionFinishTerminal:
		return r.finishAfterTerminalToolCalls(ctx, input, base, st)
	case toolTurnResolutionFinishCurrent:
		return r.finishCurrentPlanResult(ctx, input, base, st, turnID)
	case toolTurnResolutionResume:
	}
	if capFailures(vals) > 0 {
		st.Caps.RemainingConsecutiveFailedToolCalls = decrementCap(
			st.Caps.RemainingConsecutiveFailedToolCalls,
			capFailures(vals),
		)
		if st.Caps.MaxConsecutiveFailedToolCalls > 0 && st.Caps.RemainingConsecutiveFailedToolCalls <= 0 {
			out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.ToolOutputs, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonFailureCap, deadlines.Hard)
			return out, err
		}
	} else if st.Caps.MaxConsecutiveFailedToolCalls > 0 {
		st.Caps.RemainingConsecutiveFailedToolCalls = st.Caps.MaxConsecutiveFailedToolCalls
	}

	if out, err := r.handleMissingFieldsPolicy(wfCtx, reg, input, base, vals, st.ToolEvents, st.ToolOutputs, st.AggUsage, &st.NextAttempt, turnID, ctrl, deadlines); err != nil {
		return nil, err
	} else if out != nil {
		return out, nil
	}

	protected, err := r.hardProtectionIfNeeded(ctx, input.AgentID, base, vals, turnID)
	if err != nil {
		return nil, err
	}
	if protected {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.ToolOutputs, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonFailureCap, deadlines.Hard)
		return out, err
	}

	resumeReq, err := r.buildNextResumeRequest(input.AgentID, base, input.Policy, st.ToolOutputs, &st.NextAttempt)
	if err != nil {
		return nil, err
	}
	resOutput, err := r.runPlanActivity(wfCtx, reg.ResumeActivityName, resumeOpts, resumeReq, deadlines.Budget)
	if err != nil {
		return nil, err
	}
	if resOutput == nil || resOutput.Result == nil {
		return nil, fmt.Errorf("plan activity returned nil result on resume")
	}
	st.AggUsage = addTokenUsage(st.AggUsage, resOutput.Usage)
	st.Result = resOutput.Result
	st.Transcript = resOutput.Transcript
	return nil, nil
}

// appendPlannerVisibleTurnResults appends only the subset of tool results that
// remain visible to future planner turns. Successful bookkeeping batches
// therefore become a no-op here, while retryable bookkeeping failures and mixed
// batches still preserve the planner-visible repair context.
func (r *Runtime) appendPlannerVisibleTurnResults(
	ctx context.Context,
	input *RunInput,
	base *planner.PlanInput,
	st *runLoopState,
	turnID string,
	calls []planner.ToolRequest,
	results []*planner.ToolResult,
) error {
	if err := r.appendToolOutputs(ctx, st, calls, results); err != nil {
		return err
	}
	if err := r.appendLatePlannerVisibleToolUses(ctx, input.AgentID, base, calls, results, turnID); err != nil {
		return err
	}
	if err := r.appendUserToolResults(ctx, input.AgentID, base, calls, results, turnID); err != nil {
		return err
	}
	return nil
}

// classifyToolTurn decides whether an executed batch with no pending await work
// must resume reasoning or can complete immediately without replaying tool
// results back into the planner.
func (r *Runtime) classifyToolTurn(calls []planner.ToolRequest, results []*planner.ToolResult, result *planner.PlanResult) (toolTurnResolution, error) {
	if len(calls) == 0 {
		return toolTurnResolutionResume, nil
	}
	for _, call := range calls {
		if !r.isBookkeeping(call.Name) {
			return toolTurnResolutionResume, nil
		}
	}

	plannerVisibleCalls, _, err := r.filterPlannerVisibleToolResults(calls, results)
	if err != nil {
		return 0, err
	}
	if len(plannerVisibleCalls) > 0 {
		return toolTurnResolutionResume, nil
	}

	terminal, err := r.executedSuccessfulTerminalRunTool(results)
	if err != nil {
		return 0, err
	}
	if terminal {
		return toolTurnResolutionFinishTerminal, nil
	}
	if err := validateTerminalPlanResult(result); err != nil {
		return 0, fmt.Errorf(
			"bookkeeping-only tool batch requires a terminal tool or terminal planner payload in the same turn: %w",
			err,
		)
	}
	return toolTurnResolutionFinishCurrent, nil
}

// executedSuccessfulTerminalRunTool reports whether the executed batch contains
// a terminal tool result that completed without a tool error.
func (r *Runtime) executedSuccessfulTerminalRunTool(results []*planner.ToolResult) (bool, error) {
	for _, tr := range results {
		if tr == nil {
			continue
		}
		spec, ok := r.toolSpec(tr.Name)
		if !ok {
			return false, fmt.Errorf("unknown tool %q", tr.Name)
		}
		if spec.TerminalRun && tr.Error == nil {
			return true, nil
		}
	}
	return false, nil
}

// toolResultsFromExecutions extracts durable planner-visible tool results from a
// batch of runtime-owned execution outcomes.
func toolResultsFromExecutions(outcomes []*ToolExecutionResult) []*planner.ToolResult {
	if len(outcomes) == 0 {
		return nil
	}
	results := make([]*planner.ToolResult, 0, len(outcomes))
	for _, outcome := range outcomes {
		if outcome == nil || outcome.ToolResult == nil {
			continue
		}
		results = append(results, outcome.ToolResult)
	}
	return results
}

// toolPausesFromExecutions extracts current-batch runtime pause signals from a
// batch of execution outcomes in canonical tool-call order.
func toolPausesFromExecutions(outcomes []*ToolExecutionResult) []*ToolPause {
	if len(outcomes) == 0 {
		return nil
	}
	pauses := make([]*ToolPause, 0, len(outcomes))
	for _, outcome := range outcomes {
		if outcome == nil || outcome.Pause == nil {
			continue
		}
		pauses = append(pauses, outcome.Pause)
	}
	return pauses
}

// toolPauseAwaitItems projects runtime-owned tool pauses into the existing await
// queue item model.
func toolPauseAwaitItems(pauses []*ToolPause) ([]planner.AwaitItem, error) {
	if len(pauses) == 0 {
		return nil, nil
	}
	items := make([]planner.AwaitItem, 0, len(pauses))
	for i, pause := range pauses {
		if pause == nil || pause.Clarification == nil {
			return nil, fmt.Errorf("tool pause %d is invalid", i)
		}
		items = append(items, planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID:       pause.Clarification.ID,
			Question: pause.Clarification.Question,
		}))
	}
	return items, nil
}
