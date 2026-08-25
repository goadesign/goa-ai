package runtime

// workflow_step.go defines the explicit unit of progress for the durable
// workflow loop.
//
// Contract:
// - One planner result is normalized into exactly one step program.
// - Tool execution and await draining append concrete call/result records to a
//   step batch.
// - Post-step policy is evaluated once from the accumulated batch, so resume,
//   finish, and finalization decisions do not diverge across tool and await
//   paths.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	stepKind uint8

	stepProgram struct {
		result        *PlanResult
		calls         []ToolCall
		allowed       []ToolCall
		immediate     []ToolCall
		confirmations []confirmationAwait
		awaitItems    []planner.AwaitItem
		budgetCost    int
		admitted      bool
		kind          stepKind
	}

	stepToolRecord struct {
		call             ToolCall
		result           *planner.ToolResult
		callRunID        string
		resultRunID      string
		resultJSON       rawjson.Message
		resultPublished  bool
		resultRecord     *RecordActivityInput
		scheduleRequired bool
		scheduleQueue    string
		scheduleExact    bool
		expectedChildren int
		duration         time.Duration
		clarification    *ToolClarification
		childSuspension  *api.RunSuspension
		requiresResume   bool
	}

	stepBatch struct {
		program       stepProgram
		records       []stepToolRecord
		recorded      int
		budgetCost    int
		timedOut      bool
		awaited       bool
		confirmations int
		awaitItems    int
		finalize      *stepFinalization
		suspension    *RunOutput
		pending       []checkpointPendingInput
		// resumePlannerAfterPending marks a generated clarification emitted
		// after this batch's caps and failure streak were already applied.
		resumePlannerAfterPending bool
	}

	stepFinalization struct {
		reason planner.TerminationReason
	}
)

const (
	stepKindTerminal stepKind = iota + 1
	stepKindAwait
	stepKindTools
	stepKindToolTerminal
)

// String returns the stable diagnostic label for a workflow step kind.
func (k stepKind) String() string {
	switch k {
	case stepKindTerminal:
		return "terminal"
	case stepKindAwait:
		return "await"
	case stepKindTools:
		return "tools"
	case stepKindToolTerminal:
		return "tool_terminal"
	default:
		panic(fmt.Sprintf("runtime: unknown workflow step kind %d", k))
	}
}

// normalizeStep converts a planner result into the runtime's single-step
// execution model.
func (r *Runtime) normalizeStep(result *PlanResult) (stepProgram, error) {
	if result == nil {
		return stepProgram{}, planner.NewOutputContractError(errors.New("workflow step received nil PlanResult"))
	}
	terminalPayloads := 0
	if result.FinalResponse != nil {
		terminalPayloads++
	}
	if result.FinalToolResult != nil {
		terminalPayloads++
	}
	if terminalPayloads > 1 {
		return stepProgram{}, planner.NewOutputContractError(
			errors.New("workflow step received both FinalResponse and FinalToolResult"),
		)
	}

	hasCalls := len(result.ToolCalls) > 0
	hasTerminal := terminalPayloads == 1
	hasAwait := result.Await != nil
	var awaitItems []planner.AwaitItem
	if hasAwait {
		awaitItems = result.Await.Items
		if len(awaitItems) == 0 {
			return stepProgram{}, planner.NewOutputContractError(errors.New("workflow step received empty await"))
		}
		if err := validateAwaitItems(awaitItems); err != nil {
			return stepProgram{}, planner.NewOutputContractError(err)
		}
		if err := r.validateAwaitTools(awaitItems); err != nil {
			return stepProgram{}, planner.NewOutputContractError(err)
		}
	}
	if !hasCalls && !hasTerminal && !hasAwait {
		return stepProgram{}, planner.NewOutputContractError(errors.New("workflow step received empty PlanResult"))
	}
	if result.SynthesizeAfterTools && (!hasCalls || hasTerminal || hasAwait) {
		return stepProgram{}, planner.NewOutputContractError(
			errors.New("workflow step synthesis-after-tools requires only tool calls"),
		)
	}
	if result.SynthesizeAfterTools {
		if err := r.validateSynthesisAfterTools(result.ToolCalls); err != nil {
			return stepProgram{}, planner.NewOutputContractError(err)
		}
	}

	if hasTerminal && hasAwait {
		return stepProgram{}, planner.NewOutputContractError(
			errors.New("workflow step cannot combine terminal payload and await"),
		)
	}
	if hasTerminal && !hasCalls {
		return stepProgram{
			result: result,
			kind:   stepKindTerminal,
		}, nil
	}
	if hasTerminal {
		if err := r.validateToolTerminalProgram(result.ToolCalls); err != nil {
			return stepProgram{}, planner.NewOutputContractError(err)
		}
		return stepProgram{
			result: result,
			calls:  result.ToolCalls,
			kind:   stepKindToolTerminal,
		}, nil
	}
	if hasCalls {
		return stepProgram{
			result:     result,
			calls:      result.ToolCalls,
			awaitItems: awaitItems,
			kind:       stepKindTools,
		}, nil
	}
	return stepProgram{
		result:     result,
		awaitItems: awaitItems,
		kind:       stepKindAwait,
	}, nil
}

// normalizePlanResultContract validates the complete runtime-owned structure of
// one planner result before transcript persistence or step selection.
func (r *Runtime) normalizePlanResultContract(result *PlanResult) (stepProgram, error) {
	program, err := r.normalizeStep(result)
	if err != nil {
		return stepProgram{}, err
	}
	if err := validatePlanResultToolCallIDs(result); err != nil {
		return stepProgram{}, planner.NewOutputContractError(err)
	}
	plannerResult := plannerResultValidationProjection(result)
	if err := validatePlannerResultPayloads(plannerResult); err != nil {
		return stepProgram{}, planner.NewOutputContractError(err)
	}
	return program, nil
}

// plannerResultValidationProjection rebuilds the planner-owned fields that
// cross the validation boundary. Provider correlation stays separate from the
// runtime execution ID assigned after the planner activity returns.
func plannerResultValidationProjection(result *PlanResult) *planner.PlanResult {
	plannerResult := &planner.PlanResult{
		ToolCalls:            make([]planner.ToolRequest, len(result.ToolCalls)),
		SynthesizeAfterTools: result.SynthesizeAfterTools,
		FinalResponse:        result.FinalResponse,
		FinalToolResult:      result.FinalToolResult,
		Await:                result.Await,
		ExpectedChildren:     result.ExpectedChildren,
		Notes:                result.Notes,
	}
	for index, call := range result.ToolCalls {
		plannerResult.ToolCalls[index] = planner.ToolRequest{
			Name:            call.TranscriptName(),
			Payload:         call.TranscriptPayload(),
			ModelToolCallID: call.ModelToolCallID,
		}
	}
	return plannerResult
}

// normalizePlanResultForExecution validates generated tool payload contracts
// before planner events are published or the workflow accepts tool work.
func (r *Runtime) normalizePlanResultForExecution(ctx context.Context, result *PlanResult) (stepProgram, error) {
	program, err := r.normalizePlanResultContract(result)
	if err != nil {
		return stepProgram{}, err
	}
	for index, call := range result.ToolCalls {
		if err := validatePlannerToolPayloadWithCodec(ctx, r, nil, call.Name, call.Payload); err != nil {
			return stepProgram{}, planner.NewOutputContractError(
				fmt.Errorf("workflow step tool call %d payload: %w", index, err),
			)
		}
	}
	var awaitItems []planner.AwaitItem
	if result.Await != nil {
		awaitItems = result.Await.Items
	}
	for index, call := range awaitToolRequests(awaitItems) {
		if err := validatePlannerToolPayloadWithCodec(ctx, r, nil, call.Name, call.Payload); err != nil {
			return stepProgram{}, planner.NewOutputContractError(
				fmt.Errorf("workflow step await tool call %d payload: %w", index, err),
			)
		}
	}
	return program, nil
}

// validateAwaitTools requires every tool named by an await request to exist and
// remain available after the user responds.
func (r *Runtime) validateAwaitTools(items []planner.AwaitItem) error {
	for _, call := range awaitToolRequests(items) {
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			return fmt.Errorf("planner await references unknown tool %q", call.Name)
		}
		if spec.TerminalRun {
			return fmt.Errorf("terminal tool %q cannot be owned by planner await work", call.Name)
		}
	}
	return nil
}

// validateAwaitItems proves the await union and its correlation identifiers
// before any provider-facing tool use is committed.
func validateAwaitItems(items []planner.AwaitItem) error {
	for i, item := range items {
		variants := 0
		if item.Clarification != nil {
			variants++
		}
		if item.ToolClarification != nil {
			variants++
		}
		if item.Questions != nil {
			variants++
		}
		if item.ExternalTools != nil {
			variants++
		}
		if variants != 1 {
			return fmt.Errorf("await item %d must set exactly one payload", i)
		}
		switch item.Kind {
		case planner.AwaitItemKindClarification:
			if item.Clarification == nil || item.Clarification.ID == "" || item.Clarification.Question == "" {
				return fmt.Errorf("await clarification item %d requires id and question", i)
			}
		case planner.AwaitItemKindToolClarification:
			q := item.ToolClarification
			if q == nil || q.ID == "" || q.ToolName == "" || q.ModelToolCallID == "" || q.Question == "" {
				return fmt.Errorf("await tool clarification item %d requires id, tool, model_tool_call_id, and question", i)
			}
		case planner.AwaitItemKindQuestions:
			q := item.Questions
			if q == nil || q.ID == "" || q.ToolName == "" || q.ModelToolCallID == "" || len(q.Questions) == 0 {
				return fmt.Errorf("await questions item %d requires id, tool, model_tool_call_id, and questions", i)
			}
		case planner.AwaitItemKindExternalTools:
			e := item.ExternalTools
			if e == nil || e.ID == "" || len(e.Items) == 0 {
				return fmt.Errorf("await external tools item %d requires id and tool calls", i)
			}
			for j, call := range e.Items {
				if call.Name == "" || call.ModelToolCallID == "" {
					return fmt.Errorf("await external tools item %d call %d requires tool and model_tool_call_id", i, j)
				}
			}
		default:
			return fmt.Errorf("await item %d has unknown kind %q", i, item.Kind)
		}
	}
	return nil
}

// awaitToolRequests returns the compiled calls embedded in an await barrier.
// Each call keeps runtime identity for records and provider identity for
// transcript reconstruction; plain clarification awaits do not create calls.
func awaitToolRequests(items []planner.AwaitItem) []ToolCall {
	var calls []ToolCall
	for _, item := range items {
		switch item.Kind {
		case planner.AwaitItemKindClarification:
		case planner.AwaitItemKindToolClarification:
			q := item.ToolClarification
			calls = append(calls, ToolCall{
				Name:            q.ToolName,
				ToolCallID:      q.ToolCallID,
				ModelToolCallID: q.ModelToolCallID,
				Payload:         append(rawjson.Message(nil), q.Payload...),
				ModelPayload:    append(rawjson.Message(nil), q.Payload...),
			})
		case planner.AwaitItemKindQuestions:
			q := item.Questions
			calls = append(calls, ToolCall{
				Name:            q.ToolName,
				ToolCallID:      q.ToolCallID,
				ModelToolCallID: q.ModelToolCallID,
				Payload:         append(rawjson.Message(nil), q.Payload...),
				ModelPayload:    append(rawjson.Message(nil), q.Payload...),
			})
		case planner.AwaitItemKindExternalTools:
			for _, call := range item.ExternalTools.Items {
				calls = append(calls, ToolCall{
					Name:            call.Name,
					ToolCallID:      call.ToolCallID,
					ModelToolCallID: call.ModelToolCallID,
					Payload:         append(rawjson.Message(nil), call.Payload...),
					ModelPayload:    append(rawjson.Message(nil), call.Payload...),
				})
			}
		}
	}
	return calls
}

// compilePlannerAwaitForRun clones one planner-authored await barrier and
// assigns runtime execution IDs before the workflow commits, publishes, or
// checkpoints it. The index follows ordinary planner calls so every call in one
// result has a distinct replay-stable identity.
func compilePlannerAwaitForRun(runCtx run.Context, firstIndex int, await *planner.Await) (*planner.Await, error) {
	items := make([]planner.AwaitItem, len(await.Items))
	callIndex := firstIndex
	for itemIndex, item := range await.Items {
		switch item.Kind {
		case planner.AwaitItemKindClarification:
			clarification := *item.Clarification
			clarification.MissingFields = append([]string(nil), item.Clarification.MissingFields...)
			clarification.ExampleJSON = append(rawjson.Message(nil), item.Clarification.ExampleJSON...)
			items[itemIndex] = planner.AwaitClarificationItem(&clarification)
		case planner.AwaitItemKindToolClarification:
			source := item.ToolClarification
			if source.ToolCallID != "" {
				return nil, fmt.Errorf("planner await item %d tool clarification already has runtime tool call ID", itemIndex)
			}
			clarification := *source
			clarification.ToolCallID = generateDeterministicToolCallID(
				runCtx.RunID,
				runCtx.TurnID,
				runCtx.Attempt,
				source.ToolName,
				callIndex,
			)
			clarification.Payload = append(rawjson.Message(nil), source.Payload...)
			items[itemIndex] = planner.AwaitToolClarificationItem(&clarification)
			callIndex++
		case planner.AwaitItemKindQuestions:
			source := item.Questions
			if source.ToolCallID != "" {
				return nil, fmt.Errorf("planner await item %d questions already has runtime tool call ID", itemIndex)
			}
			questions := *source
			questions.ToolCallID = generateDeterministicToolCallID(
				runCtx.RunID,
				runCtx.TurnID,
				runCtx.Attempt,
				source.ToolName,
				callIndex,
			)
			questions.Payload = append(rawjson.Message(nil), source.Payload...)
			if source.Title != nil {
				title := *source.Title
				questions.Title = &title
			}
			questions.Questions = cloneAwaitQuestions(source.Questions)
			items[itemIndex] = planner.AwaitQuestionsItem(&questions)
			callIndex++
		case planner.AwaitItemKindExternalTools:
			source := item.ExternalTools
			external := *source
			external.Items = make([]planner.AwaitToolItem, len(source.Items))
			for toolIndex, tool := range source.Items {
				if tool.ToolCallID != "" {
					return nil, fmt.Errorf(
						"planner await item %d external tool %d already has runtime tool call ID",
						itemIndex,
						toolIndex,
					)
				}
				tool.ToolCallID = generateDeterministicToolCallID(
					runCtx.RunID,
					runCtx.TurnID,
					runCtx.Attempt,
					tool.Name,
					callIndex,
				)
				tool.Payload = append(rawjson.Message(nil), tool.Payload...)
				external.Items[toolIndex] = tool
				callIndex++
			}
			items[itemIndex] = planner.AwaitExternalToolsItem(&external)
		default:
			return nil, fmt.Errorf("planner await item %d has unknown kind %q", itemIndex, item.Kind)
		}
	}
	return planner.NewAwait(items...), nil
}

// cloneAwaitQuestions copies the question and option slices so a planner cannot
// mutate the workflow-owned suspension after returning.
func cloneAwaitQuestions(questions []planner.AwaitQuestion) []planner.AwaitQuestion {
	cloned := make([]planner.AwaitQuestion, len(questions))
	for index, question := range questions {
		question.Options = append([]planner.AwaitQuestionOption(nil), question.Options...)
		cloned[index] = question
	}
	return cloned
}

// validateSynthesisAfterTools requires a batch whose existing execution
// classification guarantees a subsequent planner resume.
func (r *Runtime) validateSynthesisAfterTools(calls []ToolCall) error {
	for _, call := range calls {
		spec, ok := r.toolSpec(call.Name)
		if ok && spec.TerminalRun {
			return fmt.Errorf("workflow step synthesis-after-tools cannot include terminal tool %q", call.Name)
		}
	}
	if !r.hasBudgetedToolCalls(calls) {
		return errors.New("workflow step synthesis-after-tools requires at least one budgeted tool")
	}
	return nil
}

// hasBudgetedToolCalls distinguishes active work from runtime bookkeeping,
// which remains a completion obligation after Budget expires.
func (r *Runtime) hasBudgetedToolCalls(calls []ToolCall) bool {
	for _, call := range calls {
		if !r.isBookkeeping(call.Name) {
			return true
		}
	}
	return false
}

func (r *Runtime) hasBookkeepingToolCalls(calls []ToolCall) bool {
	for _, call := range calls {
		if r.isBookkeeping(call.Name) {
			return true
		}
	}
	return false
}

// runStep executes one normalized planner result and applies one post-step
// transition.
func (l *workflowLoop) runStep(program stepProgram) (*RunOutput, error) {
	if program.result.Await != nil {
		compiled, err := compilePlannerAwaitForRun(
			l.base.RunContext,
			len(program.calls),
			program.result.Await,
		)
		if err != nil {
			return nil, planner.NewOutputContractError(err)
		}
		program.result.Await = compiled
		program.awaitItems = compiled.Items
	}
	if err := validateRecoveryCatalog(l.st.PendingRecovery, l.st.PendingRecoveryCatalog, program.result); err != nil {
		return nil, planner.NewOutputContractError(err)
	}
	if program.result.Await == nil {
		l.st.PendingRecovery = nil
		l.st.PendingRecoveryCatalog = nil
	}
	if len(program.calls) > 0 {
		program.calls = l.r.prepareAllowedCallsMetadata(
			l.input.AgentID,
			l.base,
			program.calls,
			l.parentTracker,
		)
		program.result.ToolCalls = program.calls
	}
	if program.kind == stepKindTools &&
		l.r.hasBudgetedToolCalls(program.calls) &&
		!l.r.hasBookkeepingToolCalls(program.calls) &&
		l.deadlines.shouldFinalize(l.wfCtx.Now()) {
		return l.finalizeStep(planner.TerminationReasonTimeBudget)
	}
	if len(program.calls) > 0 {
		if err := l.prepareToolStep(&program); err != nil {
			return nil, err
		}
	}
	if err := l.commitSelectedModelResponse(program.result); err != nil {
		return nil, err
	}
	if program.kind == stepKindTerminal {
		return l.r.finishCurrentPlanResult(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID)
	}

	batch, err := l.executeStepProgram(program)
	if err != nil {
		return nil, err
	}
	if batch.suspension != nil {
		return batch.suspension, nil
	}
	return l.advanceStep(batch)
}

// validatePlanResultToolCallIDs proves that every model-facing call has one
// stable identity before the selected response is accepted or effects begin.
func validatePlanResultToolCallIDs(result *PlanResult) error {
	seen := make(map[string]struct{})
	for _, call := range runtimePlanResultModelToolCalls(result) {
		if call.id == "" {
			return fmt.Errorf("workflow step tool %q is missing tool_call_id", call.name)
		}
		if _, exists := seen[call.id]; exists {
			return fmt.Errorf("workflow step contains duplicate tool_call_id %s", call.id)
		}
		seen[call.id] = struct{}{}
	}
	return nil
}

// validateToolTerminalProgram enforces the only legal terminal-with-tools
// shape: non-resuming bookkeeping side effects followed by a terminal planner
// payload in the same step.
func (r *Runtime) validateToolTerminalProgram(calls []ToolCall) error {
	for _, call := range calls {
		spec, ok := r.toolSpec(call.Name)
		if !ok {
			return fmt.Errorf("workflow step terminal payload cannot accompany unknown tool %q", call.Name)
		}
		if !spec.Bookkeeping {
			return fmt.Errorf("workflow step terminal payload cannot accompany budgeted tool %q", call.Name)
		}
		if spec.TerminalRun {
			return fmt.Errorf("workflow step terminal payload cannot accompany terminal tool %q", call.Name)
		}
	}
	return nil
}

// executeStepProgram runs all immediate effects for one planner result and any
// await work that must be drained before the planner can be resumed.
func (l *workflowLoop) executeStepProgram(program stepProgram) (stepBatch, error) {
	batch := stepBatch{program: program}
	if len(program.calls) > 0 {
		confirmations, items, err := l.executeToolStep(program, &batch)
		if err != nil {
			return stepBatch{}, l.failCommittedStep(&batch, err)
		}
		if err := validateCompletionToolRecords(batch.records, completionTool(l.input)); err != nil {
			return stepBatch{}, l.failCommittedStep(&batch, err)
		}
		if batch.finalize != nil {
			awaitCalls := awaitToolRequests(program.awaitItems)
			awaitCalls = l.r.prepareAllowedCallsMetadata(
				l.input.AgentID,
				l.base,
				awaitCalls,
				l.parentTracker,
			)
			failures, resultErr := stepToolRecordsAfterExecution(
				awaitCalls,
				nil,
				errors.New("tool call was not executed because the run tool-call cap was exhausted"),
			)
			if resultErr != nil {
				return stepBatch{}, resultErr
			}
			batch.records = append(batch.records, failures...)
			l.prepareRecoveryRecords(&batch)
			return batch, nil
		}
		confirmations, expiredRecords, expired, err := l.resolveExpiredConfirmations(
			confirmations,
			program.result.ExpectedChildren,
		)
		batch.records = append(batch.records, expiredRecords...)
		batch.timedOut = batch.timedOut || expired
		if err != nil {
			return stepBatch{}, l.failCommittedStep(&batch, err)
		}
		// Tool-authored clarifications occur after their tool result and must expose that
		// result before the user's answer. Planner-authored awaits remain behind
		// the step barrier so all tool uses receive one correlated result message.
		if len(confirmations) == 0 && len(program.awaitItems) == 0 && len(items) > 0 {
			// A suspended child has no tool result yet, so the step barrier must
			// remain intact until every pending child and tool-owned request has
			// been answered.
			if len(batch.pending) == 0 {
				if err := l.recordUnrecordedStepToolResults(&batch); err != nil {
					return stepBatch{}, err
				}
			}
		}
		if len(confirmations) > 0 || len(items) > 0 || len(batch.pending) > 0 {
			if err := l.handleAwaitQueue(
				confirmations,
				items,
				&batch,
			); err != nil {
				return stepBatch{}, l.failCommittedStep(&batch, err)
			}
		}
		return batch, nil
	}

	if len(program.awaitItems) == 0 {
		return stepBatch{}, errors.New("workflow step has neither terminal payload nor executable work")
	}
	if err := l.handleAwaitQueue(
		nil,
		program.awaitItems,
		&batch,
	); err != nil {
		return stepBatch{}, l.failCommittedStep(&batch, err)
	}
	return batch, nil
}

// failCommittedStep closes every provider-correlated call in a committed step
// before returning the error that interrupted execution.
func (l *workflowLoop) failCommittedStep(batch *stepBatch, stepErr error) error {
	recorded := make(map[string]struct{}, len(batch.records))
	for _, record := range batch.records {
		recorded[record.call.ToolCallID] = struct{}{}
	}
	allCalls := append([]ToolCall(nil), batch.program.calls...)
	awaitCalls := awaitToolRequests(batch.program.awaitItems)
	awaitCalls = l.r.prepareAllowedCallsMetadata(
		l.input.AgentID,
		l.base,
		awaitCalls,
		l.parentTracker,
	)
	allCalls = append(allCalls, awaitCalls...)
	unresolved := make([]ToolCall, 0, len(allCalls))
	for _, call := range allCalls {
		if _, ok := recorded[call.ToolCallID]; !ok {
			unresolved = append(unresolved, call)
		}
	}
	failures, resultErr := stepToolRecordsAfterExecution(unresolved, nil, stepErr)
	batch.records = append(batch.records, failures...)
	l.prepareRecoveryRecords(batch)
	return l.recordToolResultsBeforeError(batch, errors.Join(stepErr, resultErr))
}

// prepareRecoveryRecords restores the exact schedule metadata needed to retry
// lifecycle publication with the call's stable event key.
func (l *workflowLoop) prepareRecoveryRecords(batch *stepBatch) {
	awaitIDs := make(map[string]struct{})
	for _, call := range awaitToolRequests(batch.program.awaitItems) {
		awaitIDs[call.ToolCallID] = struct{}{}
	}
	for i := range batch.records {
		record := &batch.records[i]
		if !record.scheduleRequired || record.scheduleExact {
			continue
		}
		if _, ok := awaitIDs[record.call.ToolCallID]; ok {
			record.scheduleExact = true
			continue
		}
		record.scheduleQueue = l.toolOpts.Queue
		if record.scheduleQueue == "" {
			if spec, ok := l.r.toolSpec(record.call.Name); ok {
				l.r.mu.RLock()
				toolset, exists := l.r.toolsets[spec.Toolset]
				l.r.mu.RUnlock()
				if exists {
					record.scheduleQueue = toolset.TaskQueue
				}
			}
		}
		record.expectedChildren = batch.program.result.ExpectedChildren
		record.scheduleExact = true
	}
}

// advanceStep applies all post-step policy and either completes the run or
// advances state to the next planner result.
func (l *workflowLoop) advanceStep(batch stepBatch) (*RunOutput, error) {
	if err := l.recordUnrecordedStepToolResults(&batch); err != nil {
		return nil, err
	}
	if batch.finalize != nil {
		return l.finalizeStep(batch.finalize.reason)
	}
	if batch.timedOut {
		return l.finalizeStep(planner.TerminationReasonTimeBudget)
	}

	if batch.program.kind == stepKindToolTerminal {
		if err := validateToolTerminalBatch(batch.records); err != nil {
			return nil, err
		}
		return l.r.finishCurrentPlanResult(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID)
	}

	l.st.Caps.RemainingToolCalls = decrementCap(l.st.Caps.RemainingToolCalls, batch.budgetCost)
	completed, err := completionToolSucceeded(batch.records, completionTool(l.input))
	if err != nil {
		return nil, err
	}
	if completed {
		return l.r.finishAfterSuccessfulToolCompletion(l.wfCtx.Context(), l.input, l.base, l.st)
	}

	resolution, err := l.r.classifyStep(batch)
	if err != nil {
		return nil, err
	}
	switch resolution {
	case stepTransitionFinishTerminal:
		return l.r.finishAfterSuccessfulToolCompletion(l.wfCtx.Context(), l.input, l.base, l.st)
	case stepTransitionFinishCurrent:
		return l.r.finishCurrentPlanResult(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID)
	case stepTransitionResume:
	}

	results := batch.results()
	// The failure streak counts planner decision points whose budgeted work
	// failed outright: any budgeted success resets the streak, an all-failure
	// batch consumes one unit regardless of its parallel width, and
	// bookkeeping results never move the counter. One exploratory batch that
	// partially fails is progress, not thrash.
	progress, failed := l.r.budgetedBatchOutcome(batch.records)
	if applyFailureStreak(&l.st.Caps, progress, failed) {
		return l.finalizeStep(planner.TerminationReasonFailureCap)
	}

	if out, await, err := l.r.applyMissingFieldsPolicy(
		l.wfCtx,
		l.reg,
		l.input,
		l.base,
		results,
		l.st.ToolEvents,
		l.st.ToolOutputs,
		l.st.AggUsage,
		&l.st.NextAttempt,
		l.turnID,
		&l.deadlines,
	); err != nil {
		return nil, err
	} else if out != nil {
		return out, nil
	} else if await != nil {
		if err := l.r.publishAwaitToolUses(l.wfCtx.Context(), l.input, l.base, l.turnID, *await, 0); err != nil {
			return nil, err
		}
		l.st.PendingRecovery = append(pendingRecoveryOutputs(batch.records), l.st.PendingRecovery...)
		batch.awaited = true
		batch.awaitItems++
		batch.resumePlannerAfterPending = true
		return l.suspendRun(batch, nil, []planner.AwaitItem{*await})
	}

	action, failed := dominantRecoveryAction(batch.records)
	recovery := pendingRecoveryOutputs(batch.records)
	if failed && action == planner.RecoveryFinish {
		continuations, err := l.r.availableContinuationActions(l.input.AgentID, l.st.ToolOutputs)
		if err != nil {
			return nil, err
		}
		if len(continuations) == 0 {
			return l.finalizeRecoveryStep(dominantRecoveryOutputs(batch.records))
		}
		// A successful sibling query still owns its unfinished pages. Preserve
		// the finish failure as exact recovery evidence while the next planner
		// turn chooses whether to continue those already-started queries.
		recovery = dominantRecoveryOutputs(batch.records)
	}
	pendingRecovery := slices.Concat(recovery, l.st.PendingRecovery)
	synthesisOnly := !failed && batch.program.result.SynthesizeAfterTools
	if out, err := l.resumePlanner(pendingRecovery, synthesisOnly); err != nil || out != nil {
		return out, err
	}
	return nil, nil
}

// resumePlanner executes the next planner turn after one fully-accounted step.
// Callers provide the exact failed outputs that remain eligible for correction.
func (l *workflowLoop) resumePlanner(pendingRecovery []*planner.ToolOutput, synthesisOnly bool) (*RunOutput, error) {
	resumeReq, err := l.r.buildNextResumeRequest(
		l.input.AgentID,
		l.base,
		l.input.Policy,
		l.st.ToolOutputs,
		pendingRecovery,
		synthesisOnly,
		&l.st.NextAttempt,
	)
	if err != nil {
		return nil, err
	}
	resOutput, err := l.r.runPlanActivity(l.wfCtx, l.reg.ResumeActivityName, l.resumeOpts, resumeReq, l.deadlines.Budget)
	if err != nil {
		if errors.Is(err, engine.ErrPlannerActivityDeadlineExceeded) &&
			!l.deadlines.Budget.IsZero() {
			return l.finalizeStep(planner.TerminationReasonTimeBudget)
		}
		return nil, err
	}
	if resOutput == nil || resOutput.Result == nil {
		return nil, errors.New("plan activity returned nil result on resume")
	}
	l.st.AggUsage, err = addTokenUsage(l.st.AggUsage, resOutput.Usage)
	if err != nil {
		return nil, fmt.Errorf("aggregate run usage: %w", err)
	}
	l.st.Result = resOutput.Result
	l.st.Transcript = resOutput.Transcript
	l.st.ResponseCommitted = false
	l.st.PendingRecovery = pendingRecovery
	l.st.PendingRecoveryCatalog = resOutput.RecoveryCatalog
	return nil, nil
}

// validateRecoveryCatalog checks the exact tool list shown during a recovery
// turn. Ordinary turns have neither pending recovery failures nor a recovery
// catalog. Recovery turns must have both.
func validateRecoveryCatalog(
	pendingRecovery []*planner.ToolOutput,
	catalog *RecoveryCatalog,
	result *PlanResult,
) error {
	if len(pendingRecovery) == 0 {
		if catalog != nil {
			return errors.New("planner result has a recovery catalog without pending recovery failures")
		}
		return nil
	}
	if catalog == nil {
		return errors.New("planner result with pending recovery failures requires a recovery catalog")
	}
	allowed := make(map[tools.Ident]struct{}, len(catalog.Tools))
	for _, tool := range catalog.Tools {
		allowed[tool] = struct{}{}
	}
	calls := append([]ToolCall(nil), result.ToolCalls...)
	if result.Await != nil {
		calls = append(calls, awaitToolRequests(result.Await.Items)...)
	}
	for _, call := range calls {
		name := call.TranscriptName()
		if _, ok := allowed[name]; !ok {
			return fmt.Errorf("planner called tool %q outside the advertised recovery catalog", name)
		}
	}
	return nil
}

// recoveryUnavailableTools returns authored tools excluded from the next
// planner turn. A finish failure closes all new domain work. The finalization
// turn retains terminal tools so the planner can persist the run's required
// result, while an earlier continuation turn exposes only runtime-generated
// actions for successful queries that already started. Replan excludes only
// rejected tools, and same-tool payload correction keeps the failed tool
// available.
func (r *Runtime) recoveryUnavailableTools(
	agentID agent.Ident,
	outputs []*planner.ToolOutput,
	finalizing bool,
) []tools.Ident {
	for _, output := range outputs {
		if output.Failure == nil || output.Failure.Recovery.Action != planner.RecoveryFinish {
			continue
		}
		var unavailable []tools.Ident
		for _, spec := range r.ToolSpecsForAgent(agentID) {
			if isDedicatedContinuationSpec(spec) || finalizing && spec.TerminalRun {
				continue
			}
			unavailable = append(unavailable, spec.Name)
		}
		return unavailable
	}

	correctable := make(map[tools.Ident]struct{})
	for _, output := range outputs {
		if output.Failure != nil && output.Failure.Recovery.Action == planner.RecoveryCorrectCall {
			correctable[output.Name] = struct{}{}
		}
	}
	seen := make(map[tools.Ident]struct{})
	var unavailable []tools.Ident
	for _, output := range outputs {
		// ToolUnavailable is the hidden result identity for any unknown model
		// name, not an advertised capability the model can repeat.
		if output.Name != tools.ToolUnavailable &&
			output.Failure != nil &&
			output.Failure.Recovery.Action == planner.RecoveryReplan {
			if _, canCorrect := correctable[output.Name]; canCorrect {
				continue
			}
			if _, exists := seen[output.Name]; !exists {
				seen[output.Name] = struct{}{}
				unavailable = append(unavailable, output.Name)
			}
		}
	}
	return unavailable
}

// pendingRecoveryOutputs captures only next-turn tool recovery transitions from
// the batch that just completed.
func pendingRecoveryOutputs(records []stepToolRecord) []*planner.ToolOutput {
	action, failed := dominantRecoveryAction(records)
	if !failed || action == planner.RecoveryFinish {
		return nil
	}
	var outputs []*planner.ToolOutput
	for _, record := range records {
		if record.result == nil ||
			record.result.Failure == nil ||
			!record.result.Failure.AllowsToolTurn() {
			continue
		}
		outputs = append(outputs, &planner.ToolOutput{
			Name:            record.call.Name,
			ToolCallID:      record.call.ToolCallID,
			ModelToolCallID: record.call.ModelToolCallID,
			Payload:         append(rawjson.Message(nil), record.call.Payload...),
			Failure:         record.result.Failure,
		})
	}
	return outputs
}

// dominantRecoveryOutputs selects the failed calls whose recovery action owns
// the next transition. Finish failures use this projection to correlate the
// terminal finalization turn with its exact causes.
func dominantRecoveryOutputs(records []stepToolRecord) []*planner.ToolOutput {
	action, failed := dominantRecoveryAction(records)
	if !failed {
		return nil
	}
	var outputs []*planner.ToolOutput
	for _, record := range records {
		if record.result == nil ||
			record.result.Failure == nil ||
			record.result.Failure.Recovery.Action != action {
			continue
		}
		outputs = append(outputs, &planner.ToolOutput{
			Name:            record.call.Name,
			ToolCallID:      record.call.ToolCallID,
			ModelToolCallID: record.call.ModelToolCallID,
			Payload:         append(rawjson.Message(nil), record.call.Payload...),
			Failure:         record.result.Failure,
		})
	}
	return outputs
}

// recordUnrecordedStepToolResults persists each concrete tool result exactly
// once, preserving transcript order when a tool result creates await work.
func (l *workflowLoop) recordUnrecordedStepToolResults(batch *stepBatch) error {
	if batch == nil {
		return errors.New("workflow step missing batch")
	}
	if batch.recorded > len(batch.records) {
		panic("runtime: recorded step tool result count exceeds batch records")
	}
	records := batch.records[batch.recorded:]
	ordered, err := canonicalStepToolRecords(batch.program.calls, records)
	if err != nil {
		return err
	}
	copy(records, ordered)
	if err := l.r.recordStepToolResults(l.wfCtx.Context(), l.input, l.base, l.st, l.turnID, records); err != nil {
		return err
	}
	batch.recorded = len(batch.records)
	return nil
}

// dominantRecoveryAction combines parallel failures without allowing one result
// to weaken another: finish dominates correction, and correction dominates
// replanning.
func dominantRecoveryAction(records []stepToolRecord) (planner.RecoveryAction, bool) {
	var action planner.RecoveryAction
	for _, record := range records {
		if record.result == nil || record.result.Failure == nil {
			continue
		}
		action = strongerRecoveryAction(action, record.result.Failure.Recovery.Action)
		if action == planner.RecoveryFinish {
			return action, true
		}
	}
	return action, action != ""
}

// finalizeStep closes a run through its configured fixed call or saved-message
// planner turn after budgeted tool work is forbidden.
func (l *workflowLoop) finalizeStep(reason planner.TerminationReason) (*RunOutput, error) {
	return l.r.finalizeRun(
		l.wfCtx,
		l.reg,
		l.input,
		l.base,
		l.st.ToolEvents,
		l.st.ToolOutputs,
		l.st.AggUsage,
		l.st.NextAttempt,
		l.turnID,
		nil,
		reason,
		l.deadlines.Hard,
	)
}

// finalizeRecoveryStep asks the planner to end domain work after a tool's
// finish directive while preserving the exact failed calls as guidance.
func (l *workflowLoop) finalizeRecoveryStep(recovery []*planner.ToolOutput) (*RunOutput, error) {
	return l.r.finalizeRun(
		l.wfCtx,
		l.reg,
		l.input,
		l.base,
		l.st.ToolEvents,
		l.st.ToolOutputs,
		l.st.AggUsage,
		l.st.NextAttempt,
		l.turnID,
		recovery,
		planner.TerminationReasonToolFailure,
		l.deadlines.Hard,
	)
}

// validateToolTerminalBatch verifies that all bookkeeping side effects in a
// tool-terminal step completed successfully and without runtime-owned awaits.
func validateToolTerminalBatch(records []stepToolRecord) error {
	for _, record := range records {
		if record.clarification != nil {
			return fmt.Errorf("workflow step terminal payload cannot accompany clarification from tool %q", record.call.Name)
		}
		if record.result == nil {
			return fmt.Errorf("workflow step terminal payload missing result for tool %q", record.call.Name)
		}
		if record.result.Failure != nil {
			return fmt.Errorf("workflow step terminal payload cannot accompany failed tool %q: %w", record.call.Name, record.result.Failure.Error)
		}
	}
	return nil
}

// results returns the concrete tool results produced during this step.
func (b stepBatch) results() []*planner.ToolResult {
	return stepToolResults(b.records)
}

// stepToolResults returns the result side of paired step records.
func stepToolResults(records []stepToolRecord) []*planner.ToolResult {
	if len(records) == 0 {
		return nil
	}
	results := make([]*planner.ToolResult, 0, len(records))
	for _, record := range records {
		results = append(results, record.result)
	}
	return results
}

// stepToolRecordsFromExecutions pairs execution outcomes by tool-call identity
// and returns records in canonical call order. Timeout grouping may execute
// calls in a different order than the provider-authored transcript.
func stepToolRecordsFromExecutions(calls []ToolCall, outcomes []*ToolExecutionResult) ([]stepToolRecord, error) {
	if len(calls) != len(outcomes) {
		return nil, fmt.Errorf("workflow step execution mismatch: calls=%d outcomes=%d", len(calls), len(outcomes))
	}
	byID := make(map[string]*ToolExecutionResult, len(outcomes))
	for _, outcome := range outcomes {
		if outcome == nil || outcome.ToolResult == nil {
			return nil, errors.New("workflow step execution returned an empty outcome")
		}
		id := outcome.ToolResult.ToolCallID
		if id == "" {
			return nil, fmt.Errorf("workflow step execution result for %q is missing tool_call_id", outcome.ToolResult.Name)
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("workflow step execution returned duplicate tool_call_id %s", id)
		}
		byID[id] = outcome
	}
	records := make([]stepToolRecord, 0, len(calls))
	for _, call := range calls {
		outcome, ok := byID[call.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("workflow step execution missing result for %q (%s)", call.Name, call.ToolCallID)
		}
		record := stepToolRecord{
			call:             call,
			result:           outcome.ToolResult,
			clarification:    outcome.Clarification,
			childSuspension:  outcome.childSuspension,
			resultPublished:  outcome.resultPublished,
			resultRecord:     outcome.resultRecord,
			scheduleRequired: !outcome.schedulePublished,
			scheduleQueue:    outcome.scheduleQueue,
			scheduleExact:    true,
			expectedChildren: outcome.expectedChildren,
			duration:         outcome.duration,
		}
		if err := validateStepToolRecord("workflow step execution", record); err != nil {
			return nil, err
		}
		records = append(records, record)
		delete(byID, call.ToolCallID)
	}
	if len(byID) > 0 {
		return nil, errors.New("workflow step execution returned results for unknown tool calls")
	}
	return records, nil
}

// stepToolRecordsAfterExecution closes every committed tool call even when the
// execution layer returns only a partial set of concrete outcomes. Concrete
// results are preserved; unresolved calls receive an explicit infrastructure
// failure before the original execution error is returned.
func stepToolRecordsAfterExecution(
	calls []ToolCall,
	outcomes []*ToolExecutionResult,
	executionErr error,
) ([]stepToolRecord, error) {
	callsByID := make(map[string]ToolCall, len(calls))
	for _, call := range calls {
		callsByID[call.ToolCallID] = call
	}
	recordsByID := make(map[string]stepToolRecord, len(outcomes))
	var resultErr error
	for _, outcome := range outcomes {
		if outcome == nil || outcome.ToolResult == nil {
			resultErr = errors.Join(resultErr, errors.New("workflow step execution returned an empty outcome"))
			continue
		}
		id := outcome.ToolResult.ToolCallID
		call, ok := callsByID[id]
		if !ok {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("workflow step execution returned unknown tool_call_id %s", id),
			)
			continue
		}
		if _, exists := recordsByID[id]; exists {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("workflow step execution returned duplicate tool_call_id %s", id),
			)
			continue
		}
		record := stepToolRecord{
			call:             call,
			result:           outcome.ToolResult,
			clarification:    outcome.Clarification,
			childSuspension:  outcome.childSuspension,
			resultPublished:  outcome.resultPublished,
			resultRecord:     outcome.resultRecord,
			scheduleRequired: !outcome.schedulePublished,
			scheduleQueue:    outcome.scheduleQueue,
			scheduleExact:    true,
			expectedChildren: outcome.expectedChildren,
			duration:         outcome.duration,
		}
		if err := validateStepToolRecord("workflow step execution", record); err != nil {
			resultErr = errors.Join(resultErr, err)
			continue
		}
		recordsByID[id] = record
	}
	for _, call := range calls {
		if _, ok := recordsByID[call.ToolCallID]; !ok {
			if executionErr == nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("workflow step execution missing result for %q (%s)", call.Name, call.ToolCallID),
				)
			}
		}
	}
	boundaryErr := errors.Join(executionErr, resultErr)
	records := make([]stepToolRecord, 0, len(calls))
	for _, call := range calls {
		if record, ok := recordsByID[call.ToolCallID]; ok {
			records = append(records, record)
			continue
		}
		records = append(records, stepToolRecord{
			call:             call,
			scheduleRequired: true,
			result: &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Failure: &planner.ToolFailure{
					Kind: planner.FailureInternal,
					Error: planner.NewToolError(
						"tool execution did not produce a result: " + boundaryErr.Error(),
					),
					Recovery: planner.RecoveryDirective{
						Action: planner.RecoveryFinish,
					},
				},
			},
		})
	}
	return records, resultErr
}

// canonicalStepToolRecords restores the provider's call order after calls from
// different deadline or confirmation classes complete independently. Records
// created by planner await items follow in their original await order.
func canonicalStepToolRecords(calls []ToolCall, records []stepToolRecord) ([]stepToolRecord, error) {
	byID := make(map[string]stepToolRecord, len(records))
	for _, record := range records {
		id := record.call.ToolCallID
		if id == "" {
			return nil, fmt.Errorf("workflow step result for %q is missing tool_call_id", record.call.Name)
		}
		if _, exists := byID[id]; exists {
			return nil, fmt.Errorf("workflow step returned duplicate tool_call_id %s", id)
		}
		byID[id] = record
	}
	ordered := make([]stepToolRecord, 0, len(records))
	for _, call := range calls {
		record, ok := byID[call.ToolCallID]
		if !ok {
			continue
		}
		ordered = append(ordered, record)
		delete(byID, call.ToolCallID)
	}
	for _, record := range records {
		if _, ok := byID[record.call.ToolCallID]; !ok {
			continue
		}
		ordered = append(ordered, record)
		delete(byID, record.call.ToolCallID)
	}
	return ordered, nil
}

// toolClarificationsFromRecords extracts current-step user questions in
// canonical call order.
func toolClarificationsFromRecords(records []stepToolRecord) []*ToolClarification {
	if len(records) == 0 {
		return nil
	}
	clarifications := make([]*ToolClarification, 0, len(records))
	for _, record := range records {
		if record.clarification == nil {
			continue
		}
		clarifications = append(clarifications, record.clarification)
	}
	return clarifications
}
