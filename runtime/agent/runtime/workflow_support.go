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
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// finalizeRun selects the configured fixed terminal call for a runtime
// limit or asks the planner to finish from saved messages.
func (r *Runtime) finalizeRun(
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
	if completion := completionTool(input); completion != "" {
		return nil, completionToolRequiredError(completion, reason)
	}
	var plans *LimitTerminalPlans
	if input.Policy != nil {
		plans = input.Policy.LimitTerminalPlans
	}
	limitCall, hasLimitCall, err := limitTerminalCall(plans, reason)
	if err != nil {
		return nil, err
	}
	if hasLimitCall {
		return r.finishLimitTerminalCall(
			wfCtx,
			reg,
			input,
			base,
			allToolResults,
			allToolOutputs,
			aggUsage,
			nextAttempt,
			turnID,
			limitCall,
			reason,
			hardDeadline,
		)
	}
	return r.finalizeFromHistory(
		wfCtx,
		reg,
		input,
		base,
		allToolResults,
		allToolOutputs,
		aggUsage,
		nextAttempt,
		turnID,
		recovery,
		reason,
		hardDeadline,
	)
}

// finalizeFromHistory asks the planner to produce the final response from
// saved messages and prior tool outputs.
func (r *Runtime) finalizeFromHistory(
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
	hint, err := finalizationPrompt(reason)
	if err != nil {
		return nil, err
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
			output.Result,
			output.Transcript,
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

// finalizationPrompt returns the instruction used when the planner must finish
// from saved messages after normal work stops.
func finalizationPrompt(reason planner.TerminationReason) (string, error) {
	switch reason {
	case planner.TerminationReasonTimeBudget:
		return "FINALIZE NOW: time budget reached.\n\n- Provide the best possible final answer using ONLY the information already available in the conversation and tool results.\n- Do NOT call any tools.\n- Do NOT say you will call tools or that you will \"try\" another approach.\n- If additional tool calls would be needed, explain what you would have retrieved and how it would change the answer, then provide the best provisional answer.", nil
	case planner.TerminationReasonToolCap:
		return "FINALIZE NOW: tool budget exhausted.\n\n- Provide the best possible final answer using ONLY the information already available in the conversation and tool results.\n- Do NOT call any tools.\n- Do NOT say you will call tools.\n- If further tool calls would be needed, describe them briefly and provide the best provisional answer.", nil
	case planner.TerminationReasonFailureCap:
		return "FINALIZE NOW: too many tool failures.\n\n- Provide the best possible final answer using ONLY the information already available in the conversation and tool results.\n- Do NOT call any tools.\n- Do NOT say you will call tools.\n- If tools failed due to invalid arguments, summarize the failure and provide a corrected plan/payload shape (without actually calling tools), then provide the best provisional answer.", nil
	case planner.TerminationReasonToolFailure:
		return "FINALIZE NOW: a tool could not complete the requested work.\n\n- Do not retry the failed operation or gather more information.\n- Use only the information already available in the conversation and tool results.\n- Provide the best final result possible, clearly stating what could not be completed.\n- If this workflow requires one final submission action, use only that action.", nil
	default:
		return "", fmt.Errorf("unsupported termination reason %q", reason)
	}
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
	result *planner.PlanResult,
	transcript []*model.Message,
	allToolResults []*planner.ToolResult,
	allToolOutputs []*planner.ToolOutput,
	aggUsage model.TokenUsage,
	nextAttempt int,
	turnID string,
	hardDeadline time.Time,
) (*RunOutput, error) {
	if result == nil {
		return nil, errors.New("finalization terminal tool plan is missing planner output")
	}
	if err := r.validateFinalizationTerminalToolCalls(result.ToolCalls); err != nil {
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
	st := newRunLoopState(result, transcript, aggUsage, policy.CapsState{}, nextAttempt)
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
		runDeadlines{
			Budget: hardDeadline,
			Hard:   hardDeadline,
		},
		reg.ResumeActivityOptions,
		toolOpts,
	)
	program := stepProgram{
		result: result,
		calls:  result.ToolCalls,
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
		return nil, errors.New("finalization terminal tool step cannot request clarification")
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
		if record.clarification != nil {
			return fmt.Errorf("finalization terminal tool step cannot request clarification from tool %q", record.call.Name)
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

// applyMissingFieldsPolicy inspects generated validation issues for missing
// required fields and applies the agent RunPolicy.OnMissingFields behavior:
//
//   - MissingFieldsFinalize: immediately request a terminal planner result
//     from the planner. Returns a non-nil RunOutput to short-circuit the loop.
//   - MissingFieldsAwaitClarification: return one typed await item so the workflow
//     can publish the request and end with a continuation checkpoint.
//   - MissingFieldsResume (or unspecified): do nothing; the planner will see the
//     correction directive and may choose how to proceed. Returns handled=false.
//
// The function returns:
//   - out: non-nil only when finalization occurred
//   - await: non-nil only when the workflow must suspend for clarification
//   - err: any error encountered while applying the configured policy
func (r *Runtime) applyMissingFieldsPolicy(
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
	deadlines *runDeadlines,
) (*RunOutput, *planner.AwaitItem, error) {
	if reg.Policy.OnMissingFields == "" {
		return nil, nil, nil
	}
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
		return nil, nil, nil
	}
	switch reg.Policy.OnMissingFields {
	case MissingFieldsFinalize:
		out, err := r.finalizeRun(
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
			planner.TerminationReasonToolFailure,
			deadlines.Hard,
		)
		return out, nil, err
	case MissingFieldsAwaitClarification:
		// Generate deterministic await ID for correlation safety.
		awaitID := generateDeterministicAwaitID(base.RunContext.RunID, base.RunContext.TurnID, triggerTool, triggerCall)
		question, err := r.missingFieldsQuestion(triggerTool, missing)
		if err != nil {
			return nil, nil, err
		}
		item := planner.AwaitClarificationItem(&planner.AwaitClarification{
			ID:             awaitID,
			Question:       question,
			MissingFields:  missing,
			RestrictToTool: triggerTool,
			ExampleJSON:    example,
		})
		return nil, &item, nil
	case MissingFieldsResume:
		return nil, nil, nil
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
