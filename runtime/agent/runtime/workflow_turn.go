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
	"context"
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

	toExecute, deniedResults, cerr := r.confirmToolsIfNeeded(wfCtx, input, base, allowed, turnID, ctrl, budgetDeadline)
	if cerr != nil {
		if errors.Is(cerr, context.DeadlineExceeded) {
			out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonTimeBudget, hardDeadline)
			return out, err
		}
		return nil, cerr
	}

	declaredCalls := allowed
	var awaitExpectedIDs map[string]struct{}
	if result.Await != nil {
		if result.Await.Clarification != nil {
			return nil, errors.New("planner returned both tool calls and await clarification")
		}
		if result.Await.ExternalTools != nil && result.Await.Questions != nil {
			return nil, errors.New("planner returned multiple await kinds with tool calls")
		}
		if result.Await.Questions != nil {
			q := result.Await.Questions
			if q.ToolCallID == "" {
				return nil, errors.New("await_questions: missing tool_call_id")
			}
			awaitExpectedIDs = map[string]struct{}{
				q.ToolCallID: {},
			}
			awaitCalls := []planner.ToolRequest{
				{
					Name:       q.ToolName,
					ToolCallID: q.ToolCallID,
					Payload:    q.Payload,
				},
			}
			declaredCalls = make([]planner.ToolRequest, 0, len(allowed)+len(awaitCalls))
			declaredCalls = append(declaredCalls, allowed...)
			declaredCalls = append(declaredCalls, awaitCalls...)
		}
		if result.Await.ExternalTools != nil {
			e := result.Await.ExternalTools
			if len(e.Items) == 0 {
				return nil, errors.New("await_external_tools: no items in await")
			}
			awaitCalls := make([]planner.ToolRequest, 0, len(e.Items))
			awaitExpectedIDs = make(map[string]struct{}, len(e.Items))
			for _, it := range e.Items {
				if it.ToolCallID == "" {
					return nil, fmt.Errorf(
						"await_external_tools: missing tool_call_id for external tool %q",
						it.Name,
					)
				}
				if _, dup := awaitExpectedIDs[it.ToolCallID]; dup {
					return nil, fmt.Errorf(
						"await_external_tools: duplicate awaited tool_call_id %q",
						it.ToolCallID,
					)
				}
				awaitExpectedIDs[it.ToolCallID] = struct{}{}
				awaitCalls = append(awaitCalls, planner.ToolRequest{
					Name:       it.Name,
					ToolCallID: it.ToolCallID,
					Payload:    it.Payload,
				})
			}
			declaredCalls = make([]planner.ToolRequest, 0, len(allowed)+len(awaitCalls))
			declaredCalls = append(declaredCalls, allowed...)
			declaredCalls = append(declaredCalls, awaitCalls...)
		}
	}

	r.recordAssistantTurn(base, st.Transcript, declaredCalls, st.Ledger)

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
	vals, err = mergeToolResultsByCallID(allowed, vals, deniedResults)
	if err != nil {
		return nil, err
	}
	lastToolResults := vals
	st.ToolEvents = append(st.ToolEvents, cloneToolResults(vals)...)
	if result.Await == nil {
		r.appendUserToolResults(base, allowed, vals, st.Ledger, artifactsModeByCallID)
	}
	if timedOut {
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonTimeBudget, hardDeadline)
		return out, err
	}

	st.Caps.RemainingToolCalls = decrementCap(st.Caps.RemainingToolCalls, len(allowed))
	if failures(vals) > 0 {
		st.Caps.RemainingConsecutiveFailedToolCalls = decrementCap(
			st.Caps.RemainingConsecutiveFailedToolCalls,
			failures(vals),
		)
		if st.Caps.MaxConsecutiveFailedToolCalls > 0 && st.Caps.RemainingConsecutiveFailedToolCalls <= 0 {
			out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, st.ToolEvents, st.AggUsage, st.NextAttempt, turnID, planner.TerminationReasonFailureCap, hardDeadline)
			return out, err
		}
	} else if st.Caps.MaxConsecutiveFailedToolCalls > 0 {
		st.Caps.RemainingConsecutiveFailedToolCalls = st.Caps.MaxConsecutiveFailedToolCalls
	}

	if result.Await != nil {
		out, err := r.handleAwaitAfterTools(wfCtx, reg, input, base, result.Await, declaredCalls, awaitExpectedIDs, artifactsModeByCallID, vals, st, resumeOpts, ctrl, budgetDeadline, hardDeadline, turnID)
		if err != nil {
			return nil, err
		}
		return out, nil
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

	resumeReq, err := r.buildNextResumeRequest(input.AgentID, base, lastToolResults, &st.NextAttempt)
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
