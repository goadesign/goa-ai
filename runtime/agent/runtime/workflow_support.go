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
	"strings"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// finalizeWithPlanner asks the planner to finish after budgeted work is
// forbidden. The planner returns either a final response or terminal
// bookkeeping calls.
func (r *Runtime) finalizeWithPlanner(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	allToolResults []*planner.ToolResult,
	allToolOutputs []*planner.ToolOutput,
	aggUsage model.TokenUsage,
	nextAttempt int,
	turnID string,
	recovery []*planner.ToolOutput,
	reason planner.TerminationReason,
	hardDeadline time.Time,
) (*RunOutput, error) {
	if base == nil {
		return nil, errors.New("base plan input is required")
	}
	ctx := wfCtx.Context()
	// Transition to synthesizing phase while we obtain a final answer without
	// scheduling additional budgeted tools.
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
		hint = "FINALIZE NOW: time budget reached.\n\n- Provide the best possible final answer using ONLY the information already available in the conversation and tool results.\n- Do NOT call any tools.\n- Do NOT say you will call tools or that you will \"try\" another approach.\n- If additional tool calls would be needed, explain what you would have retrieved and how it would change the answer, then provide the best provisional answer."
	case planner.TerminationReasonToolCap:
		hint = "FINALIZE NOW: tool budget exhausted.\n\n- Provide the best possible final answer using ONLY the information already available in the conversation and tool results.\n- Do NOT call any tools.\n- Do NOT say you will call tools.\n- If further tool calls would be needed, describe them briefly and provide the best provisional answer."
	case planner.TerminationReasonFailureCap:
		hint = "FINALIZE NOW: too many tool failures.\n\n- Provide the best possible final answer using ONLY the information already available in the conversation and tool results.\n- Do NOT call any tools.\n- Do NOT say you will call tools.\n- If tools failed due to invalid arguments, summarize the failure and provide a corrected plan/payload shape (without actually calling tools), then provide the best provisional answer."
	case planner.TerminationReasonToolFailure:
		hint = "FINALIZE NOW: a tool could not complete the requested work.\n\n- Do not retry the failed operation or gather more information.\n- Use only the information already available in the conversation and tool results.\n- Provide the best final result possible, clearly stating what could not be completed.\n- If this workflow requires one final submission action, use only that action."
	default:
		hint = "FINALIZE NOW.\n\n- Provide the best possible final answer using ONLY the information already available in the conversation and tool results.\n- Do NOT call any tools.\n- Do NOT say you will call tools.\n- If more work is needed, describe it succinctly and provide the best provisional answer."
	}
	messages, err := model.CloneMessages(base.Messages)
	if err != nil {
		return nil, err
	}
	if err := transcript.ValidatePlannerTranscript(messages); err != nil {
		return nil, fmt.Errorf("cannot finalize invalid planner transcript: %w", err)
	}

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
	encodedToolOutputs, err := encodePlannerToolOutputs(allToolOutputs)
	if err != nil {
		return nil, err
	}
	req := PlanActivityInput{
		AgentID:             input.AgentID,
		RunID:               base.RunContext.RunID,
		Messages:            messages,
		RunContext:          resumeCtx,
		Policy:              clonePolicyOverrides(input.Policy),
		ToolOutputs:         encodedToolOutputs,
		RecoveryToolCallIDs: recoveryToolCallIDs(recovery),
		Finalize:            &planner.Termination{Reason: reason, Message: hint},
	}
	if err := enforcePlanActivityInputBudget(req); err != nil {
		return nil, err
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
		case planner.TerminationReasonToolFailure:
			return "tool required finalization"
		default:
			return "finalization failed"
		}
	}()

	// Apply run-level Plan timeout override to Resume if present.
	resumeOpts := reg.ResumeActivityOptions
	if input.Policy != nil && input.Policy.PlanTimeout > 0 {
		resumeOpts.StartToCloseTimeout = input.Policy.PlanTimeout
	}
	output, err := r.runPlanActivity(wfCtx, reg.ResumeActivityName, resumeOpts, req, hardDeadline)
	if err != nil {
		// Surface the termination reason prominently; include underlying error for observability.
		return nil, fmt.Errorf("%s: %w", reasonText, err)
	}
	if output == nil || output.Result == nil {
		return nil, errors.New(reasonText)
	}
	if err := validateFinalizationPlanResult(output.Result); err != nil {
		return nil, fmt.Errorf("%s: %w", reasonText, err)
	}
	aggUsage = addTokenUsage(aggUsage, output.Usage)
	if isFinalizationTerminalToolPlan(output.Result) {
		out, err := r.finishFinalizationTerminalToolCalls(
			wfCtx,
			reg,
			input,
			base,
			output,
			allToolResults,
			allToolOutputs,
			aggUsage,
			nextAttempt,
			turnID,
			hardDeadline,
		)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", reasonText, err)
		}
		return out, nil
	}
	if err := r.appendSelectedModelResponse(
		ctx,
		input.AgentID,
		base,
		turnID,
		output.Result,
		output.Transcript,
	); err != nil {
		return nil, fmt.Errorf("%s: %w", reasonText, err)
	}
	out, err := r.materializeTerminalPlannerResult(ctx, input, base, turnID, terminalPlannerState{
		result:     output.Result,
		toolEvents: allToolResults,
		usage:      aggUsage,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", reasonText, err)
	}
	return out, nil
}

// validateFinalizationPlanResult enforces the finalizer's closed result union:
// one terminal payload, or terminal bookkeeping calls to execute.
func validateFinalizationPlanResult(result *planner.PlanResult) error {
	if result == nil {
		return errors.New("finalization planner returned nil result")
	}
	if result.Await != nil {
		return errors.New("finalization planner cannot await external input")
	}
	if result.SynthesizeAfterTools {
		return errors.New("finalization planner cannot request another synthesis turn")
	}
	if len(result.ToolCalls) > 0 {
		if result.FinalResponse != nil || result.FinalToolResult != nil {
			return errors.New("finalization planner cannot combine tool calls with a terminal payload")
		}
		return nil
	}
	return validateTerminalPlanResult(result)
}

// isFinalizationTerminalToolPlan reports whether a finalization planner turn
// requested a terminal tool as the terminal action instead of returning text.
func isFinalizationTerminalToolPlan(result *planner.PlanResult) bool {
	return result != nil &&
		len(result.ToolCalls) > 0 &&
		result.FinalResponse == nil &&
		result.FinalToolResult == nil &&
		result.Await == nil
}

// finishFinalizationTerminalToolCalls executes terminal bookkeeping tools
// returned from a finalization turn and materializes the run directly from their
// durable side effects.
func (r *Runtime) finishFinalizationTerminalToolCalls(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	output *PlanActivityOutput,
	allToolResults []*planner.ToolResult,
	allToolOutputs []*planner.ToolOutput,
	aggUsage model.TokenUsage,
	nextAttempt int,
	turnID string,
	hardDeadline time.Time,
) (*RunOutput, error) {
	if output == nil || output.Result == nil {
		return nil, errors.New("finalization terminal tool plan is missing planner output")
	}
	if err := r.validateFinalizationTerminalToolCalls(output.Result.ToolCalls); err != nil {
		return nil, err
	}
	if !hardDeadline.IsZero() && !wfCtx.Now().Before(hardDeadline) {
		return nil, fmt.Errorf("finalization terminal tool step timed out: %w", context.DeadlineExceeded)
	}
	execBase := *base
	// The finalizer uses the next attempt for deterministic tool IDs while the
	// caller retains ownership of the canonical transcript.
	defer func() {
		base.Messages = execBase.Messages
	}()
	execBase.RunContext = base.RunContext
	execBase.RunContext.Attempt = nextAttempt
	toolOpts := reg.ExecuteToolActivityOptions
	if toolOpts.StartToCloseTimeout == 0 {
		toolOpts.StartToCloseTimeout = defaultExecuteToolActivityTimeout
	}
	st := newRunLoopState(output.Result, output.Transcript, aggUsage, policy.CapsState{}, nextAttempt)
	st.ToolEvents = cloneToolResults(allToolResults)
	st.ToolOutputs = append([]*planner.ToolOutput(nil), allToolOutputs...)
	loop := newWorkflowLoop(
		r,
		wfCtx,
		reg,
		input,
		&execBase,
		st,
		turnID,
		nil,
		nil,
		runDeadlines{
			Budget: hardDeadline,
			Hard:   hardDeadline,
		},
		reg.ResumeActivityOptions,
		toolOpts,
	)
	program := stepProgram{
		result: output.Result,
		calls:  output.Result.ToolCalls,
		kind:   stepKindTools,
	}
	program.calls = r.prepareAllowedCallsMetadata(input.AgentID, &execBase, program.calls, nil)
	program.result.ToolCalls = program.calls
	if err := validatePlanResultToolCallIDs(program.result); err != nil {
		return nil, err
	}
	if err := loop.prepareToolStep(&program); err != nil {
		return nil, err
	}
	if err := r.validateFinalizationTerminalToolCalls(program.allowed); err != nil {
		return nil, err
	}
	if err := loop.commitSelectedModelResponse(program.result); err != nil {
		return nil, err
	}
	batch := stepBatch{program: program}
	confirmations, items, err := loop.executeToolStep(program, &batch)
	if err != nil {
		return nil, loop.failCommittedStep(&batch, err)
	}
	if err := loop.recordUnrecordedStepToolResults(&batch); err != nil {
		return nil, err
	}
	if batch.finalize != nil {
		return nil, fmt.Errorf("finalization terminal tool step requested nested finalization: %s", batch.finalize.reason)
	}
	if batch.timedOut {
		return nil, fmt.Errorf("finalization terminal tool step timed out: %w", context.DeadlineExceeded)
	}
	if len(confirmations) > 0 || len(items) > 0 {
		return nil, errors.New("finalization terminal tool step cannot pause")
	}
	if err := r.validateFinalizationTerminalToolRecords(batch.records); err != nil {
		return nil, err
	}
	return r.finishAfterTerminalToolCalls(wfCtx.Context(), input, &execBase, st)
}

// validateFinalizationTerminalToolCalls permits only terminal bookkeeping tools
// during finalization because caps/deadlines have already forbidden new work.
func (r *Runtime) validateFinalizationTerminalToolCalls(calls []planner.ToolRequest) error {
	if len(calls) == 0 {
		return errors.New("finalization terminal tool plan has no tool calls")
	}
	for _, call := range calls {
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			return fmt.Errorf("finalization terminal tool plan cannot call unknown tool %q", call.Name)
		}
		if !spec.Bookkeeping {
			return fmt.Errorf("finalization terminal tool plan cannot call budgeted tool %q", call.Name)
		}
		if !spec.TerminalRun {
			return fmt.Errorf("finalization terminal tool plan cannot call non-terminal tool %q", call.Name)
		}
	}
	return nil
}

// validateFinalizationTerminalToolRecords requires every finalization terminal
// side effect to complete successfully after policy and unavailable-tool rewrites.
func (r *Runtime) validateFinalizationTerminalToolRecords(records []stepToolRecord) error {
	if len(records) == 0 {
		return errors.New("finalization terminal tool step produced no records")
	}
	for _, record := range records {
		if err := validateStepToolRecord("finalization terminal tool step", record); err != nil {
			return err
		}
		if record.pause != nil {
			return fmt.Errorf("finalization terminal tool step cannot pause on tool %q", record.call.Name)
		}
		if record.result.Failure != nil {
			return fmt.Errorf("finalization terminal tool step failed on tool %q: %w", record.call.Name, record.result.Failure.Error)
		}
		spec, ok := r.toolSpec(record.result.Name)
		if !ok {
			return fmt.Errorf("finalization terminal tool step returned unknown tool %q", record.result.Name)
		}
		if !spec.TerminalRun {
			return fmt.Errorf("finalization terminal tool step returned non-terminal tool %q", record.result.Name)
		}
	}
	return nil
}

// handleInterrupts drains pause signals and blocks until a resume signal arrives.
// When budgetDeadline is reached, it returns nil so the caller can finalize cleanly.
func (r *Runtime) handleInterrupts(
	wfCtx engine.WorkflowContext,
	input *RunInput,
	base *planner.PlanInput,
	turnID string,
	ctrl *interrupt.Controller,
	nextAttempt *int,
	budgetDeadline time.Time,
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
		if req == nil {
			return errors.New("pause: received nil pause request")
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

		timeout, ok := timeoutUntil(budgetDeadline, wfCtx.Now())
		if !ok {
			if err := r.publishHook(
				ctx,
				hooks.NewRunResumedEvent(
					input.RunID,
					input.AgentID,
					input.SessionID,
					"deadline_exceeded",
					"runtime",
					map[string]string{"resumed_by": "deadline_exceeded"},
					0,
				),
				turnID,
			); err != nil {
				return err
			}
			return nil
		}
		resumeReq, err := ctrl.WaitResume(ctx, timeout)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				if err := r.publishHook(
					ctx,
					hooks.NewRunResumedEvent(
						input.RunID,
						input.AgentID,
						input.SessionID,
						"deadline_exceeded",
						"runtime",
						map[string]string{"resumed_by": "deadline_exceeded"},
						0,
					),
					turnID,
				); err != nil {
					return err
				}
				return nil
			}
			if err2 := r.publishHook(
				ctx,
				hooks.NewRunResumedEvent(
					input.RunID,
					input.AgentID,
					input.SessionID,
					"resume_error",
					"runtime",
					map[string]string{"resumed_by": "resume_error"},
					0,
				),
				turnID,
			); err2 != nil {
				return err2
			}
			return err
		}
		if resumeReq == nil {
			return errors.New("resume: received nil resume request")
		}
		if len(resumeReq.Messages) > 0 {
			if err := r.appendTranscriptMessages(ctx, input.AgentID, base, turnID, resumeReq.Messages); err != nil {
				return err
			}
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

// handleMissingFieldsPolicy inspects generated validation issues for missing
// required fields and applies the agent RunPolicy.OnMissingFields behavior:
//
//   - MissingFieldsFinalize: immediately request a terminal planner result
//     from the planner. Returns a non-nil RunOutput to short-circuit the loop.
//   - MissingFieldsAwaitClarification: when durable (interrupt controller present), emit
//     an await_clarification event, pause the run, and wait indefinitely for operator input.
//     On resume, append the user answer to base PlanInput so the next turn can proceed.
//   - MissingFieldsResume (or unspecified): do nothing; the planner will see the
//     correction directive and may choose how to proceed. Returns handled=false.
//
// The function returns:
//   - out: non-nil only when finalization occurred
//   - err: any error encountered while pausing/resuming
func (r *Runtime) handleMissingFieldsPolicy(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	results []*planner.ToolResult,
	allResults []*planner.ToolResult,
	allToolOutputs []*planner.ToolOutput,
	aggUsage model.TokenUsage,
	nextAttempt *int,
	turnID string,
	ctrl *interrupt.Controller,
	deadlines *runDeadlines,
) (*RunOutput, error) {
	if ctrl == nil || reg.Policy.OnMissingFields == "" {
		return nil, nil
	}
	ctx := wfCtx.Context()
	// Find the first same-tool correction with generated missing-field issues.
	var (
		missing     []string
		example     rawjson.Message
		triggerTool tools.Ident
		triggerCall string
	)
	for _, tr := range results {
		if tr == nil ||
			tr.Failure == nil ||
			tr.Failure.Recovery.Action != planner.RecoveryCorrectCall {
			continue
		}
		for _, issue := range tr.Failure.Recovery.Issues {
			if issue.Constraint == "missing_field" {
				missing = append(missing, issue.Field)
			}
		}
		if len(missing) > 0 {
			example = tr.Failure.Recovery.ExampleJSON
			triggerTool = tr.Name
			triggerCall = tr.ToolCallID
			break
		}
	}
	if len(missing) == 0 {
		return nil, nil
	}
	switch reg.Policy.OnMissingFields {
	case MissingFieldsFinalize:
		return r.finalizeWithPlanner(
			wfCtx,
			reg,
			input,
			base,
			allResults,
			allToolOutputs,
			aggUsage,
			*nextAttempt,
			turnID,
			nil,
			planner.TerminationReasonFailureCap,
			deadlines.Hard,
		)
	case MissingFieldsAwaitClarification:
		// Generate deterministic await ID for correlation safety.
		awaitID := generateDeterministicAwaitID(base.RunContext.RunID, base.RunContext.TurnID, triggerTool, triggerCall)
		question, err := r.missingFieldsQuestion(triggerTool, missing)
		if err != nil {
			return nil, err
		}
		if err := r.publishHook(ctx, hooks.NewAwaitClarificationEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			awaitID,
			question,
			missing,
			triggerTool,
			example,
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
		waitStartedAt := wfCtx.Now()
		ans, err := ctrl.WaitProvideClarification(ctx, 0)
		if deadlines != nil {
			if delta := wfCtx.Now().Sub(waitStartedAt); delta > 0 {
				// Awaiting clarification is external wait time; it must not consume run budget.
				// Extend both deadlines so only active planner/tool execution counts.
				deadlines.pause(delta)
			}
		}
		if err != nil {
			if err2 := r.publishHook(
				ctx,
				hooks.NewRunResumedEvent(
					base.RunContext.RunID,
					input.AgentID,
					base.RunContext.SessionID,
					"clarification_error",
					"runtime",
					map[string]string{
						"resumed_by": "clarification_error",
						"await_id":   awaitID,
					},
					0,
				),
				turnID,
			); err2 != nil {
				return nil, err2
			}
			return nil, err
		}
		if ans == nil {
			return nil, errors.New("await_clarification: received nil clarification answer")
		}
		// Validate correlation when ID is present on the answer.
		if ans.ID != "" && ans.ID != awaitID {
			return nil, fmt.Errorf("unexpected await ID for clarification")
		}
		if ans.Answer != "" {
			if err := r.appendTranscriptMessages(ctx, input.AgentID, base, turnID, []*model.Message{{
				Role:  model.ConversationRoleUser,
				Parts: []model.Part{model.TextPart{Text: ans.Answer}},
			}}); err != nil {
				return nil, err
			}
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
	}
	panic("unreachable missing-fields action")
}

// missingFieldsQuestion renders a user-facing clarification request from the
// generated missing-field issues and descriptions owned by the tool payload
// contract.
func (r *Runtime) missingFieldsQuestion(tool tools.Ident, fields []string) (string, error) {
	spec, ok := r.toolSpec(tool)
	if !ok {
		return "", fmt.Errorf("missing ToolSpec for clarification tool %q", tool)
	}
	var question strings.Builder
	question.WriteString("I need a little more information before I can continue:")
	for _, field := range fields {
		description, ok := spec.Payload.FieldDescriptions[field]
		if !ok || description == "" {
			return "", fmt.Errorf("missing generated description for clarification field %q on tool %q", field, tool)
		}
		question.WriteString("\n\n- `")
		question.WriteString(field)
		question.WriteString("`: ")
		question.WriteString(description)
	}
	return question.String(), nil
}

// errRunSessionEnded terminates the run loop when a planner activity observes
// that the run's durable session was ended mid-run. It wraps context.Canceled
// so the terminal mapping (isRunCancellationError) classifies the run as
// canceled; the refusing planner activity recorded
// CancellationReasonSessionEnded on the run, so the terminal RunCompleted
// event carries the canonical reason.
var errRunSessionEnded = fmt.Errorf("run session ended: %w", context.Canceled)

// runPlanActivity schedules a plan/resume activity with the configured options.
func (r *Runtime) runPlanActivity(
	wfCtx engine.WorkflowContext,
	activityName string,
	options engine.ActivityOptions,
	input PlanActivityInput,
	deadline time.Time,
) (*PlanActivityOutput, error) {
	if activityName == "" {
		return nil, errors.New("plan activity not registered")
	}
	callOpts := options
	// Schedule-to-close owns the complete queue, retry, and backoff lifetime.
	// Queue and attempt bounds retain their distinct failure semantics.
	if !deadline.IsZero() {
		rem := deadline.Sub(wfCtx.Now())
		if rem <= 0 {
			return nil, fmt.Errorf(
				"plan activity %q deadline exceeded: %w: %w",
				activityName,
				engine.ErrPlannerActivityDeadlineExceeded,
				context.DeadlineExceeded,
			)
		}
		callOpts.ScheduleToCloseTimeout = rem
	}

	out, err := wfCtx.ExecutePlannerActivity(engine.PlannerActivityCall{
		Name:    activityName,
		Input:   &input,
		Options: callOpts,
	})
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, fmt.Errorf("runPlanActivity received nil PlanActivityOutput")
	}
	if out.SessionEnded {
		return nil, errRunSessionEnded
	}
	if out.Result == nil {
		return nil, fmt.Errorf("runPlanActivity received nil PlanResult")
	}
	if len(out.Result.ToolCalls) == 0 &&
		out.Result.FinalResponse == nil &&
		out.Result.FinalToolResult == nil &&
		out.Result.Await == nil {
		return nil, fmt.Errorf("runPlanActivity received PlanResult with no ToolCalls, FinalResponse, FinalToolResult, or Await")
	}
	r.logger.Info(wfCtx.Context(),
		"runPlanActivity received PlanResult",
		"tool_calls",
		len(out.Result.ToolCalls),
		"final_response",
		out.Result.FinalResponse != nil,
		"final_tool_result",
		out.Result.FinalToolResult != nil,
		"await",
		out.Result.Await != nil,
	)
	return out, nil
}
