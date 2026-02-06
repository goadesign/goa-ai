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
//   Await.ExternalTools handshake (execute internal tools first, then pause).

import (
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
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
	budgetDeadline time.Time,
	hardDeadline time.Time,
	finalizerGrace time.Duration,
	turnID string,
	parentTracker *childTracker,
	ctrl *interrupt.Controller,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	result := st.Result

	if st.Caps.RemainingToolCalls == 0 && st.Caps.MaxToolCalls > 0 {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonToolCap, hardDeadline)
		return out, err
	}
	if !budgetDeadline.IsZero() && wfCtx.Now().After(budgetDeadline) {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonTimeBudget, hardDeadline)
		return out, err
	}

	candidates := result.ToolCalls
	r.logger.Info(ctx, "Workflow received tool calls from planner", "count", len(candidates))
	candidates = r.applyPerRunOverrides(ctx, input, candidates)
	var err error
	candidates, err = r.rewriteUnknownToolCalls(candidates)
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

	allowed = r.capAllowedCalls(allowed, input, st.Caps)
	allowed = r.prepareAllowedCallsMetadata(input.AgentID, base, allowed, parentTracker)

	toExecute, confirmations, err := r.splitConfirmationCalls(ctx, base, allowed)
	if err != nil {
		return nil, err
	}
	if len(confirmations) > 0 && ctrl == nil {
		return nil, fmt.Errorf("confirmation required but interrupts are not available")
	}
	if len(toExecute) > 0 {
		r.recordAssistantTurn(base, st.Transcript, toExecute, st.Ledger)
	}

	artifactsModeByCallID := make(map[string]tools.ArtifactsMode, len(toExecute))
	execCalls := make([]planner.ToolRequest, len(toExecute))
	for i := range toExecute {
		call := toExecute[i]
		if call.ToolCallID == "" {
			call.ToolCallID = generateDeterministicToolCallID(base.RunContext.RunID, call.TurnID, base.RunContext.Attempt, call.Name, i)
		}
		mode, stripped, err := extractArtifactsMode(call.Payload)
		if err != nil {
			return nil, err
		}
		call.ArtifactsMode = mode
		if mode != "" {
			artifactsModeByCallID[call.ToolCallID] = mode
		}
		call.Payload = stripped
		execCalls[i] = call
	}

	grouped, timeouts := r.groupToolCallsByTimeout(execCalls, input, toolOpts.Timeout)
	finishBy := time.Time{}
	if !hardDeadline.IsZero() {
		reserve := finalizerGrace
		if reserve == 0 {
			reserve = minActivityTimeout
		}
		finishBy = hardDeadline.Add(-reserve)
	}
	vals, timedOut, err := r.executeGroupedToolCalls(wfCtx, reg, input.AgentID, base, result.ExpectedChildren, parentTracker, finishBy, grouped, timeouts, toolOpts)
	if err != nil {
		return nil, err
	}
	lastToolResults := vals
	st.ToolEvents = append(st.ToolEvents, cloneToolResults(vals)...)
	if err := r.appendUserToolResults(base, toExecute, vals, st.Ledger, artifactsModeByCallID); err != nil {
		return nil, err
	}
	if timedOut {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonTimeBudget, hardDeadline)
		return out, err
	}

	terminal, err := r.executedTerminalRunTool(vals)
	if err != nil {
		return nil, err
	}
	if terminal {
		return r.finishAfterTerminalToolCalls(ctx, input, base, st)
	}

	st.Caps.RemainingToolCalls = decrementCap(st.Caps.RemainingToolCalls, len(allowed))
	if len(confirmations) > 0 || (result.Await != nil && len(result.Await.Items) > 0) {
		items := []planner.AwaitItem(nil)
		if result.Await != nil {
			items = result.Await.Items
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
			budgetDeadline,
			hardDeadline,
			finalizerGrace,
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
	if capFailures(vals) > 0 {
		st.Caps.RemainingConsecutiveFailedToolCalls = decrementCap(
			st.Caps.RemainingConsecutiveFailedToolCalls,
			capFailures(vals),
		)
		if st.Caps.MaxConsecutiveFailedToolCalls > 0 && st.Caps.RemainingConsecutiveFailedToolCalls <= 0 {
			out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonFailureCap, hardDeadline)
			return out, err
		}
	} else if st.Caps.MaxConsecutiveFailedToolCalls > 0 {
		st.Caps.RemainingConsecutiveFailedToolCalls = st.Caps.MaxConsecutiveFailedToolCalls
	}

	if out, err := r.handleMissingFieldsPolicy(wfCtx, reg, input, base, vals, st.ToolEvents, st.AggUsage, &st.NextAttempt, turnID, ctrl, budgetDeadline, hardDeadline); err != nil {
		return nil, err
	} else if out != nil {
		return out, nil
	}

	protected, err := r.hardProtectionIfNeeded(ctx, input.AgentID, base, vals, turnID)
	if err != nil {
		return nil, err
	}
	if protected {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonFailureCap, hardDeadline)
		return out, err
	}

	resumeReq, err := r.buildNextResumeRequest(ctx, input.AgentID, base, lastToolResults, &st.NextAttempt)
	if err != nil {
		return nil, err
	}
	resOutput, err := r.runPlanActivity(wfCtx, reg.ResumeActivityName, resumeOpts, resumeReq, budgetDeadline)
	if err != nil {
		return nil, err
	}
	if resOutput == nil || resOutput.Result == nil {
		return nil, fmt.Errorf("plan activity returned nil result on resume")
	}
	st.AggUsage = addTokenUsage(st.AggUsage, resOutput.Usage)
	st.Result = resOutput.Result
	st.Transcript = resOutput.Transcript
	st.Ledger = transcript.FromModelMessages(st.Transcript)
	return nil, nil
}

func (r *Runtime) executedTerminalRunTool(results []*planner.ToolResult) (bool, error) {
	for _, tr := range results {
		if tr == nil {
			continue
		}
		spec, ok := r.toolSpec(tr.Name)
		if !ok {
			return false, fmt.Errorf("unknown tool %q", tr.Name)
		}
		if spec.TerminalRun {
			return true, nil
		}
	}
	return false, nil
}
