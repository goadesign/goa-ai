package runtime

// workflow_support.go contains the workflow-only helper methods used by the plan/tool loop.
//
// Contract:
// - These helpers are deterministic and replay-safe: timeouts use workflow time.
// - Callers should only invoke them from within workflow execution (e.g. ExecuteWorkflow/runLoop).
// - The helpers publish lifecycle events via hooks so streams can close deterministically.

import (
	"context"
	"errors"
	"fmt"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

// finalizeWithPlanner asks the planner for a tool-free final response and returns it as RunOutput.
func (r *Runtime) finalizeWithPlanner(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	allToolResults []*planner.ToolResult,
	aggUsage model.TokenUsage,
	nextAttempt int,
	turnID string,
	reason planner.TerminationReason,
	hardDeadline time.Time,
) (*RunOutput, error) {
	if base == nil {
		return nil, errors.New("base plan input is required")
	}
	ctx := wfCtx.Context()
	// Transition to synthesizing phase while we obtain a final answer without
	// scheduling additional tools.
	if err := r.publishHook(
		ctx,
		hooks.NewRunPhaseChangedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			run.PhaseSynthesizing,
		),
		turnID,
	); err != nil {
		return nil, err
	}
	// Prepare a brief message to steer planners that incorporate system messages.
	var hint string
	switch reason {
	case planner.TerminationReasonTimeBudget:
		hint = "Time budget reached. Provide the best possible final answer now. Do not call any tools."
	case planner.TerminationReasonToolCap:
		hint = "Tool budget exhausted. Provide the best possible final answer now. Do not call any tools."
	case planner.TerminationReasonFailureCap:
		hint = "Too many tool failures. Provide the best possible final answer now. Do not call any tools."
	default:
		hint = "Provide the best possible final answer now. Do not call any tools."
	}
	messages := base.Messages
	if hint != "" {
		messages = append(messages, &model.Message{
			Role:  model.ConversationRoleSystem,
			Parts: []model.Part{model.TextPart{Text: hint}},
		})
	}
	resumeCtx := base.RunContext
	resumeCtx.Attempt = nextAttempt
	// Signal zero remaining duration for any prompt engineering that uses MaxDuration.
	resumeCtx.MaxDuration = "0s"
	req := PlanActivityInput{
		AgentID:     input.AgentID,
		RunID:       base.RunContext.RunID,
		Messages:    messages,
		RunContext:  resumeCtx,
		ToolResults: allToolResults,
		Finalize:    &planner.Termination{Reason: reason, Message: hint},
	}
	// Emit a pause/resume pair to indicate a finalization turn began.
	if err := r.publishHook(
		ctx,
		hooks.NewRunPausedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			"finalize",
			"runtime",
			map[string]string{"reason": string(reason)},
			nil,
		),
		turnID,
	); err != nil {
		return nil, err
	}
	if err := r.publishHook(
		ctx,
		hooks.NewRunResumedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			"finalize",
			base.RunContext.RunID,
			nil,
			0,
		),
		turnID,
	); err != nil {
		return nil, err
	}

	// Human‑readable reason strings for error contexts when finalization fails.
	reasonText := func() string {
		switch reason {
		case planner.TerminationReasonTimeBudget:
			return "time budget exceeded"
		case planner.TerminationReasonToolCap:
			return "tool call cap exceeded"
		case planner.TerminationReasonFailureCap:
			return "consecutive failed tool call cap exceeded"
		default:
			return "finalization failed"
		}
	}()

	// Apply run-level Plan timeout override to Resume if present.
	resumeOpts := reg.ResumeActivityOptions
	if input.Policy != nil && input.Policy.PlanTimeout > 0 {
		resumeOpts.Timeout = input.Policy.PlanTimeout
	}
	output, err := r.runPlanActivity(wfCtx, reg.ResumeActivityName, resumeOpts, req, hardDeadline)
	if err != nil {
		// Surface the termination reason prominently; include underlying error for observability.
		return nil, fmt.Errorf("%s: %w", reasonText, err)
	}
	if output == nil || output.Result == nil || output.Result.FinalResponse == nil {
		return nil, fmt.Errorf("%s", reasonText)
	}
	aggUsage = addTokenUsage(aggUsage, output.Usage)
	finalMsg := output.Result.FinalResponse.Message
	if output.Result.Streamed && agentMessageText(finalMsg) == "" {
		if text := transcriptText(output.Transcript); text != "" {
			finalMsg = newTextAgentMessage(model.ConversationRoleAssistant, text)
		}
	}
	if !output.Result.Streamed {
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
	for _, note := range output.Result.Notes {
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
	notes := make([]*planner.PlannerAnnotation, len(output.Result.Notes))
	for i := range output.Result.Notes {
		notes[i] = &output.Result.Notes[i]
	}
	return &RunOutput{
		AgentID:    input.AgentID,
		RunID:      base.RunContext.RunID,
		Final:      finalMsg,
		ToolEvents: allToolResults,
		Notes:      notes,
		Usage:      &aggUsage,
	}, nil
}

// handleInterrupts drains pause signals and blocks until a resume signal arrives.
// When hardDeadline is reached, it returns nil so the caller can finalize cleanly.
func (r *Runtime) handleInterrupts(
	wfCtx engine.WorkflowContext,
	input *RunInput,
	base *planner.PlanInput,
	turnID string,
	ctrl *interrupt.Controller,
	nextAttempt *int,
	hardDeadline time.Time,
) error {
	if ctrl == nil {
		return nil
	}
	ctx := wfCtx.Context()
	for {
		req, ok := ctrl.PollPause()
		if !ok {
			break
		}
		if err := r.publishHook(
			ctx,
			hooks.NewRunPausedEvent(
				input.RunID,
				input.AgentID,
				input.SessionID,
				req.Reason,
				req.RequestedBy,
				req.Labels,
				req.Metadata,
			),
			turnID,
		); err != nil {
			return err
		}

		timeout, ok := timeoutUntil(hardDeadline, wfCtx.Now())
		if !ok {
			return nil
		}
		resumeReq, err := ctrl.WaitResume(ctx, timeout)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
		if len(resumeReq.Messages) > 0 {
			base.Messages = append(base.Messages, resumeReq.Messages...)
		}
		base.RunContext.Attempt = *nextAttempt
		*nextAttempt++
		if err := r.publishHook(
			ctx,
			hooks.NewRunResumedEvent(
				input.RunID,
				input.AgentID,
				input.SessionID,
				resumeReq.Notes,
				resumeReq.RequestedBy,
				resumeReq.Labels,
				len(resumeReq.Messages),
			),
			turnID,
		); err != nil {
			return err
		}
	}
	return nil
}

// handleMissingFieldsPolicy inspects tool results for a RetryHint indicating missing
// required fields and applies the agent RunPolicy.OnMissingFields behavior:
//
//   - MissingFieldsFinalize: immediately finalize by requesting a tool-free final answer
//     from the planner. Returns a non-nil RunOutput to short-circuit the loop.
//   - MissingFieldsAwaitClarification: when durable (interrupt controller present), emit
//     an await_clarification event and pause the run. On resume, appends the user answer
//     as a message to the base PlanInput so the next turn can proceed. Returns handled=true.
//   - MissingFieldsResume (or unspecified): do nothing; the planner will see RetryHints
//     and may choose how to proceed. Returns handled=false.
//
// The function returns:
//   - out: non-nil only when finalization occurred
//   - handled: true when a pause/resume cycle was performed
//   - err: any error encountered while pausing/resuming
func (r *Runtime) handleMissingFieldsPolicy(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	results []*planner.ToolResult,
	allResults []*planner.ToolResult,
	aggUsage model.TokenUsage,
	nextAttempt *int,
	turnID string,
	ctrl *interrupt.Controller,
	hardDeadline time.Time,
) (*RunOutput, error) {
	if ctrl == nil || reg.Policy.OnMissingFields == "" {
		return nil, nil
	}
	ctx := wfCtx.Context()
	// Find first result with missing-fields hint and capture tool context.
	var (
		mf          *planner.RetryHint
		triggerTool tools.Ident
		triggerCall string
	)
	for _, tr := range results {
		if tr == nil || tr.RetryHint == nil {
			continue
		}
		if tr.RetryHint.Reason == planner.RetryReasonMissingFields || len(tr.RetryHint.MissingFields) > 0 {
			mf = tr.RetryHint
			triggerTool = tr.Name
			triggerCall = tr.ToolCallID
			break
		}
	}
	if mf == nil {
		return nil, nil
	}
	switch reg.Policy.OnMissingFields {
	case MissingFieldsFinalize:
		out, err := r.finalizeWithPlanner(wfCtx, reg, input, base, allResults, aggUsage, *nextAttempt, turnID, planner.TerminationReasonFailureCap, time.Time{})
		return out, err
	case MissingFieldsAwaitClarification:
		// Generate deterministic await ID for correlation safety.
		awaitID := generateDeterministicAwaitID(base.RunContext.RunID, base.RunContext.TurnID, triggerTool, triggerCall)
		var restrict tools.Ident
		if mf.RestrictToTool {
			restrict = mf.Tool
		}
		if err := r.publishHook(ctx, hooks.NewAwaitClarificationEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			awaitID,
			mf.ClarifyingQuestion,
			mf.MissingFields,
			restrict,
			mf.ExampleInput,
		), turnID); err != nil {
			return nil, err
		}
		if err := r.publishHook(
			ctx,
			hooks.NewRunPausedEvent(
				base.RunContext.RunID,
				input.AgentID,
				base.RunContext.SessionID,
				"await_clarification",
				"runtime",
				nil,
				nil,
			),
			turnID,
		); err != nil {
			return nil, err
		}
		timeout, ok := timeoutUntil(hardDeadline, wfCtx.Now())
		if !ok {
			return r.finalizeWithPlanner(wfCtx, reg, input, base, allResults, aggUsage, *nextAttempt, turnID, planner.TerminationReasonTimeBudget, hardDeadline)
		}
		ans, err := ctrl.WaitProvideClarification(ctx, timeout)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return r.finalizeWithPlanner(wfCtx, reg, input, base, allResults, aggUsage, *nextAttempt, turnID, planner.TerminationReasonTimeBudget, hardDeadline)
			}
			return nil, err
		}
		// Validate correlation when ID is present on the answer.
		if ans.ID != "" && ans.ID != awaitID {
			return nil, fmt.Errorf("unexpected await ID for clarification")
		}
		if ans.Answer != "" {
			base.Messages = append(base.Messages, &model.Message{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: ans.Answer}},
			})
		}
		if err := r.publishHook(ctx, hooks.NewRunResumedEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			"clarification_provided",
			input.RunID,
			ans.Labels,
			1,
		), turnID); err != nil {
			return nil, err
		}
		return nil, nil
	case MissingFieldsResume:
		return nil, nil
	default:
		return nil, nil
	}
}

// runPlanActivity schedules a plan/resume activity with the configured options.
func (r *Runtime) runPlanActivity(
	wfCtx engine.WorkflowContext,
	activityName string,
	options engine.ActivityOptions,
	input PlanActivityInput,
	hardDeadline time.Time,
) (*PlanActivityOutput, error) {
	if activityName == "" {
		return nil, errors.New("plan activity not registered")
	}
	callOpts := options
	// Apply timeout: start with configured, then cap to remaining time to hard deadline.
	timeout := options.Timeout
	if !hardDeadline.IsZero() {
		now := wfCtx.Now()
		if rem := hardDeadline.Sub(now); rem > 0 {
			if timeout == 0 || timeout > rem {
				timeout = rem
			}
		}
	}
	callOpts.Timeout = timeout

	out, err := wfCtx.ExecutePlannerActivity(wfCtx.Context(), engine.PlannerActivityCall{
		Name:    activityName,
		Input:   &input,
		Options: callOpts,
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("CRITICAL: runPlanActivity received nil PlanActivityOutput")
	}
	if out.Result == nil {
		return nil, fmt.Errorf("CRITICAL: runPlanActivity received nil PlanResult")
	}
	if len(out.Result.ToolCalls) == 0 && out.Result.FinalResponse == nil && out.Result.Await == nil {
		return nil, fmt.Errorf("CRITICAL: runPlanActivity received PlanResult with no ToolCalls, FinalResponse, or Await")
	}
	r.logger.Info(wfCtx.Context(),
		"runPlanActivity received PlanResult",
		"tool_calls",
		len(out.Result.ToolCalls),
		"final_response",
		out.Result.FinalResponse != nil,
		"await",
		out.Result.Await != nil,
	)
	return out, nil
}
