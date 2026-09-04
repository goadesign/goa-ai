package runtime

// workflow_suspension.go owns the exact state transferred between workflows
// when an agent needs external input. The serialized checkpoint is private to
// goa-ai: the runtime persists it by run ID, then validates its version and
// reconstructs typed planner/tool state with registered codecs.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	workflowCheckpoint struct {
		Version        string
		AgentID        string
		SessionID      string
		PreviousRunID  string
		PreviousTurnID string
		Policy         *PolicyOverrides
		BaseMessages   []*model.Message
		Context        checkpointRunContext
		State          checkpointRunState
		Batch          checkpointStepBatch
		Pending        []checkpointPendingInput
		RequiredTools  []tools.Ident
		HasBudget      bool
		HasHard        bool
		BudgetLeft     time.Duration
		HardLeft       time.Duration
	}

	checkpointRunContext struct {
		ParentToolCallID string
		ParentRunID      string
		ParentAgentID    agent.Ident
		Tool             tools.Ident
		ToolArgs         rawjson.Message
		Attempt          int
		Labels           map[string]string
		Metadata         map[string]any
		MaxDuration      string
	}

	checkpointRunState struct {
		Caps                   policy.CapsState
		NextAttempt            int
		Usage                  model.TokenUsage
		Transcript             []*model.Message
		ResponseCommitted      bool
		ToolEvents             []*api.ToolEvent
		ToolOutputs            []*planner.ToolOutput
		PendingRecovery        []*planner.ToolOutput
		PendingRecoveryCatalog *RecoveryCatalog `json:",omitempty"` //nolint:tagliatelle // Checkpoints retain Go field names.
	}

	checkpointStepBatch struct {
		Result        *PlanResult
		Calls         []ToolCall
		AwaitItems    []planner.AwaitItem
		Kind          stepKind
		Records       []checkpointToolRecord
		Recorded      int
		BudgetCost    int
		TimedOut      bool
		Confirmations int
		AwaitCount    int
		Finalize      *planner.TerminationReason
		// ResumePlannerAfterPending means the saved batch was fully accounted
		// before a runtime-generated clarification suspended the workflow.
		ResumePlannerAfterPending bool
	}

	checkpointToolRecord struct {
		Call             ToolCall
		Result           *api.ToolEvent
		CallRunID        string
		ResultRunID      string
		ResultJSON       rawjson.Message
		ResultPublished  bool
		ResultRecord     *RecordActivityInput
		ScheduleRequired bool
		ScheduleQueue    string
		ScheduleExact    bool
		ExpectedChildren int
		Duration         time.Duration
		Clarification    *ToolClarification
		ChildSuspension  *api.RunSuspension
		RequiresResume   bool
	}

	checkpointPendingInput struct {
		Confirmation *checkpointConfirmation
		Await        *planner.AwaitItem
		Child        *checkpointChildContinuation
		CallRunID    string
	}

	checkpointChildContinuation struct {
		ToolCallID string
		Suspension *api.RunSuspension
	}

	checkpointConfirmation struct {
		ID               string
		Call             ToolCall
		ExpectedChildren int
		Title            string
		Prompt           string
		DeniedResult     rawjson.Message
	}
)

// suspendRun serializes the current workflow state after all visible await
// events have been recorded. Workflow finalization stores the returned
// checkpoint after it has stopped accepting cancellation requests.
func (l *workflowLoop) suspendRun(batch stepBatch, confirmations []confirmationAwait, items []planner.AwaitItem) (*RunOutput, error) {
	return l.suspendCheckpointRun(batch, confirmations, items, nil)
}

func (l *workflowLoop) suspendPendingRun(batch stepBatch, pending []checkpointPendingInput) (*RunOutput, error) {
	return l.suspendCheckpointRun(batch, nil, nil, pending)
}

func (l *workflowLoop) suspendCheckpointRun(batch stepBatch, confirmations []confirmationAwait, items []planner.AwaitItem, restoredPending []checkpointPendingInput) (*RunOutput, error) {
	if l.input.SessionID == "" {
		return nil, errors.New("sessionless run cannot request external input")
	}
	checkpoint, pending, requiredTools, err := l.buildWorkflowCheckpoint(batch, confirmations, items, restoredPending)
	if err != nil {
		return nil, err
	}
	if err := l.publishPendingInputPrompts(pending); err != nil {
		return nil, err
	}
	payload, err := json.Marshal(checkpoint)
	if err != nil {
		return nil, fmt.Errorf("encode run suspension checkpoint: %w", err)
	}
	digest := sha256.Sum256(payload)
	suspension := &api.RunSuspension{
		ID:            hex.EncodeToString(digest[:16]),
		Version:       api.RunSuspensionVersion,
		Checkpoint:    rawjson.Message(payload),
		Pending:       pending,
		RequiredTools: requiredTools,
	}
	return &RunOutput{
		AgentID:    l.input.AgentID,
		RunID:      l.input.RunID,
		Suspension: suspension,
		Usage:      &l.st.AggUsage,
	}, nil
}

// publishPendingInputPrompts records the complete visible queue under the
// workflow that will own the next response. Nested child prompts are flattened
// in the public queue, so product services never need to inspect child state.
func (l *workflowLoop) publishPendingInputPrompts(pending []*api.PendingInput) error {
	ctx := l.wfCtx.Context()
	for i, input := range pending {
		switch input.Kind {
		case api.PendingInputKindConfirmation:
			confirmation := input.Confirmation
			if confirmation == nil {
				return fmt.Errorf("pending confirmation %d is missing its payload", i)
			}
			if err := l.r.publishHook(ctx, hooks.NewAwaitConfirmationEvent(
				l.input.RunID,
				l.input.AgentID,
				l.input.SessionID,
				confirmation.ID,
				confirmation.Title,
				confirmation.Prompt,
				confirmation.ToolName,
				confirmation.ToolCallID,
				confirmation.Payload,
			), l.turnID); err != nil {
				return err
			}
		case api.PendingInputKindClarification, api.PendingInputKindToolResults:
			if input.Await == nil {
				return fmt.Errorf("pending await %d is missing its payload", i)
			}
			if err := l.r.publishAwaitPrompt(ctx, l.input, l.turnID, *input.Await, i); err != nil {
				return err
			}
		default:
			return fmt.Errorf("pending input %d has unknown kind %q", i, input.Kind)
		}
	}
	return nil
}

// buildWorkflowCheckpoint converts every decoded tool value to canonical JSON
// before the state crosses the workflow boundary.
func (l *workflowLoop) buildWorkflowCheckpoint(batch stepBatch, confirmations []confirmationAwait, items []planner.AwaitItem, restoredPending []checkpointPendingInput) (*workflowCheckpoint, []*api.PendingInput, []tools.Ident, error) {
	ctx := l.wfCtx.Context()
	stateEvents, err := l.r.encodeToolEvents(ctx, l.st.ToolEvents)
	if err != nil {
		return nil, nil, nil, err
	}
	records := make([]checkpointToolRecord, 0, len(batch.records))
	for _, record := range batch.records {
		callRunID, resultRunID, err := checkpointToolRecordRunIDs(record)
		if err != nil {
			return nil, nil, nil, err
		}
		var encodedResult *api.ToolEvent
		if record.childSuspension == nil {
			encoded, err := l.r.encodeToolEvents(ctx, []*planner.ToolResult{record.result})
			if err != nil {
				return nil, nil, nil, err
			}
			encodedResult = encoded[0]
		}
		records = append(records, checkpointToolRecord{
			Call:             record.call,
			Result:           encodedResult,
			CallRunID:        callRunID,
			ResultRunID:      resultRunID,
			ResultJSON:       record.resultJSON,
			ResultPublished:  record.resultPublished,
			ResultRecord:     record.resultRecord,
			ScheduleRequired: record.scheduleRequired,
			ScheduleQueue:    record.scheduleQueue,
			ScheduleExact:    record.scheduleExact,
			ExpectedChildren: record.expectedChildren,
			Duration:         record.duration,
			Clarification:    record.clarification,
			ChildSuspension:  record.childSuspension,
			RequiresResume:   record.requiresResume,
		})
	}

	checkpointPending := restoredPending
	if len(checkpointPending) == 0 {
		checkpointPending = make([]checkpointPendingInput, 0, len(confirmations)+len(batch.pending)+len(items))
		for _, confirmation := range confirmations {
			denied, err := l.r.marshalToolValue(ctx, confirmation.call.Name, confirmation.plan.DeniedResult, nil)
			if err != nil {
				return nil, nil, nil, fmt.Errorf("encode denied result for %s: %w", confirmation.call.Name, err)
			}
			title := confirmation.plan.Title
			if title == "" {
				title = "Confirm command"
			}
			checkpointPending = append(checkpointPending, checkpointPendingInput{
				Confirmation: &checkpointConfirmation{
					ID:               confirmation.awaitID,
					Call:             confirmation.call,
					ExpectedChildren: batch.program.result.ExpectedChildren,
					Title:            title,
					Prompt:           confirmation.plan.Prompt,
					DeniedResult:     rawjson.Message(denied),
				},
			})
		}
		checkpointPending = append(checkpointPending, batch.pending...)
		for i := range items {
			item := items[i]
			checkpointPending = append(checkpointPending, checkpointPendingInput{Await: &item, CallRunID: l.input.RunID})
		}
	}
	publicPending, err := publicPendingInputs(checkpointPending)
	if err != nil {
		return nil, nil, nil, err
	}

	now := l.wfCtx.Now()
	var finalize *planner.TerminationReason
	if batch.finalize != nil {
		reason := batch.finalize.reason
		finalize = &reason
	}
	baseContext := retargetRunContext(l.base.RunContext, l.input)
	pendingRecovery, pendingRecoveryCatalog := toolRecovery(l.st.PendingRecovery)
	if len(correctCallCatalog(pendingRecovery)) > 0 {
		// The checkpoint already contains the failed outputs needed to rebuild
		// these correction tools, so it does not store the same tool list again.
		pendingRecoveryCatalog = nil
	}
	checkpoint := &workflowCheckpoint{
		Version:        api.RunSuspensionVersion,
		AgentID:        string(l.input.AgentID),
		SessionID:      l.input.SessionID,
		PreviousRunID:  l.input.RunID,
		PreviousTurnID: l.input.TurnID,
		Policy:         clonePolicyOverrides(l.input.Policy),
		BaseMessages:   l.base.Messages,
		Context:        checkpointContextFromRun(baseContext),
		State: checkpointRunState{
			Caps:                   l.st.Caps,
			NextAttempt:            l.st.NextAttempt,
			Usage:                  l.st.AggUsage,
			Transcript:             l.st.Transcript,
			ResponseCommitted:      l.st.ResponseCommitted,
			ToolEvents:             stateEvents,
			ToolOutputs:            l.st.ToolOutputs,
			PendingRecovery:        pendingRecovery,
			PendingRecoveryCatalog: pendingRecoveryCatalog,
		},
		Batch: checkpointStepBatch{
			Result:                    batch.program.result,
			Calls:                     batch.program.calls,
			AwaitItems:                batch.program.awaitItems,
			Kind:                      batch.program.kind,
			Records:                   records,
			Recorded:                  batch.recorded,
			BudgetCost:                batch.budgetCost,
			TimedOut:                  batch.timedOut,
			Confirmations:             batch.confirmations,
			AwaitCount:                batch.awaitItems,
			Finalize:                  finalize,
			ResumePlannerAfterPending: batch.resumePlannerAfterPending,
		},
		Pending:    checkpointPending,
		HasBudget:  !l.deadlines.Budget.IsZero(),
		HasHard:    !l.deadlines.Hard.IsZero(),
		BudgetLeft: remainingDuration(l.deadlines.Budget, now),
		HardLeft:   remainingDuration(l.deadlines.Hard, now),
	}
	requiredTools := requiredCheckpointToolNames(checkpoint)
	checkpoint.RequiredTools = requiredTools
	return checkpoint, publicPending, requiredTools, nil
}

func pendingKindForAwait(kind planner.AwaitItemKind) (api.PendingInputKind, error) {
	switch kind {
	case planner.AwaitItemKindClarification, planner.AwaitItemKindToolClarification:
		return api.PendingInputKindClarification, nil
	case planner.AwaitItemKindQuestions, planner.AwaitItemKindExternalTools:
		return api.PendingInputKindToolResults, nil
	default:
		return "", fmt.Errorf("unknown saved await item kind %q", kind)
	}
}

func remainingDuration(deadline, now time.Time) time.Duration {
	if deadline.IsZero() {
		return 0
	}
	return max(deadline.Sub(now), 0)
}

// checkpointToolRecordRunIDs returns the exact logs containing a tool's call
// and result events. Prepared result records retain the immutable run selected
// when the event was created, including results written before suspension.
func checkpointToolRecordRunIDs(record stepToolRecord) (string, string, error) {
	callRunID := record.callRunID
	if callRunID == "" {
		callRunID = record.call.RunID
	}
	resultRunID := record.resultRunID
	if resultRunID == "" && record.resultRecord != nil {
		resultRunID = record.resultRecord.RunID
	}
	if callRunID == "" {
		return "", "", fmt.Errorf("checkpoint tool record %s is missing call run id", record.call.ToolCallID)
	}
	if record.resultPublished && resultRunID == "" {
		return "", "", fmt.Errorf("checkpoint tool record %s is missing result run id", record.call.ToolCallID)
	}
	return callRunID, resultRunID, nil
}

func requiredCheckpointToolNames(checkpoint *workflowCheckpoint) []tools.Ident {
	set := make(map[tools.Ident]struct{})
	if checkpoint.Policy != nil {
		if checkpoint.Policy.CompletionTool != "" {
			set[checkpoint.Policy.CompletionTool] = struct{}{}
		}
		if checkpoint.Policy.LimitTerminalPlans != nil {
			plans := checkpoint.Policy.LimitTerminalPlans
			set[plans.TimeBudget.Name] = struct{}{}
			set[plans.ToolCallCap.Name] = struct{}{}
			set[plans.RecoveryCap.Name] = struct{}{}
		}
	}
	for _, output := range checkpoint.State.ToolOutputs {
		set[output.Name] = struct{}{}
	}
	for _, output := range checkpoint.State.PendingRecovery {
		set[output.Name] = struct{}{}
	}
	for _, event := range checkpoint.State.ToolEvents {
		set[event.Name] = struct{}{}
	}
	for _, call := range checkpoint.Batch.Calls {
		set[call.Name] = struct{}{}
	}
	for _, record := range checkpoint.Batch.Records {
		set[record.Call.Name] = struct{}{}
	}
	for _, pending := range checkpoint.Pending {
		if pending.Confirmation != nil {
			set[pending.Confirmation.Call.Name] = struct{}{}
		}
		if pending.Await != nil {
			for _, call := range awaitToolRequests([]planner.AwaitItem{*pending.Await}) {
				set[call.Name] = struct{}{}
			}
		}
	}
	out := make([]tools.Ident, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// decodeCheckpointToolEvent restores one runtime-produced tool result with the
// registered result codec.
func (r *Runtime) decodeCheckpointToolEvent(event *api.ToolEvent) (*planner.ToolResult, error) {
	return decodeCheckpointToolEventWithSpecs(event, r.toolSpec)
}

func decodeCheckpointToolEventWithSpecs(event *api.ToolEvent, lookup toolSpecLookup) (*planner.ToolResult, error) {
	if event == nil {
		return nil, errors.New("run suspension contains nil tool event")
	}
	var spec *tools.ToolSpec
	if event.Failure == nil {
		registered, ok := lookup(event.Name)
		if !ok {
			return nil, fmt.Errorf("suspended tool result references unregistered tool %q", event.Name)
		}
		spec = &registered
	}
	decoded, err := validatePersistedToolResult(
		spec,
		ToolCall{Name: event.Name, ToolCallID: event.ToolCallID},
		event.Result,
		event.ServerData,
		event.Bounds,
		event.Failure,
	)
	if err != nil {
		return nil, fmt.Errorf("decode suspended tool result for %s: %w", event.Name, err)
	}
	result := &planner.ToolResult{
		Name:          event.Name,
		ServerData:    append(rawjson.Message(nil), event.ServerData...),
		Bounds:        event.Bounds,
		Failure:       planner.CloneToolFailure(event.Failure),
		Telemetry:     event.Telemetry,
		ToolCallID:    event.ToolCallID,
		ChildrenCount: event.ChildrenCount,
		RunLink:       event.RunLink,
	}
	if event.Failure == nil {
		result.Result = decoded
	}
	return result, nil
}

// resumeSuspendedWorkflow consumes one exact pending response after
// ExecuteWorkflow has restored and validated the checkpoint-owned input.
func (r *Runtime) resumeSuspendedWorkflow(wfCtx engine.WorkflowContext, reg AgentRegistration, input *RunInput, checkpoint *workflowCheckpoint) (*RunOutput, error) {
	base := &planner.PlanInput{
		Messages:   checkpoint.BaseMessages,
		RunContext: restoreCheckpointRunContext(checkpoint.Context, input),
	}
	state, err := r.restoreCheckpointState(checkpoint.State)
	if err != nil {
		return nil, err
	}
	batch, err := r.restoreCheckpointBatch(checkpoint.Batch, input, &base.RunContext)
	if err != nil {
		return nil, err
	}
	state.Result = batch.program.result
	parentTracker := (*childTracker)(nil)
	if base.RunContext.ParentToolCallID != "" {
		parentTracker = newChildTracker(base.RunContext.ParentToolCallID)
	}
	deadlines := runDeadlines{}
	now := wfCtx.Now()
	if checkpoint.HasBudget {
		deadlines.Budget = now.Add(checkpoint.BudgetLeft)
	}
	if checkpoint.HasHard {
		deadlines.Hard = now.Add(checkpoint.HardLeft)
	}
	resumeOpts := reg.ResumeActivityOptions
	if input.Policy != nil && input.Policy.PlanTimeout > 0 {
		resumeOpts.StartToCloseTimeout = input.Policy.PlanTimeout
	}
	toolOpts := reg.ExecuteToolActivityOptions
	if input.Policy != nil && input.Policy.ToolTimeout > 0 {
		toolOpts.StartToCloseTimeout = input.Policy.ToolTimeout
	}
	if toolOpts.StartToCloseTimeout == 0 {
		toolOpts.StartToCloseTimeout = defaultExecuteToolActivityTimeout
	}
	loop := newWorkflowLoop(r, wfCtx, reg, input, base, state, input.TurnID, parentTracker, deadlines, resumeOpts, toolOpts)

	pending := r.restorePendingInputs(checkpoint.Pending, input, &base.RunContext)
	if err := loop.consumePendingInput(&batch, &pending, input.Continuation.Response); err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return loop.suspendPendingRun(batch, pending)
	}
	if batch.resumePlannerAfterPending {
		recovery, _ := toolRecovery(state.PendingRecovery)
		out, err := loop.resumePlanner(recovery, false, nil, nil)
		if err != nil || out != nil {
			return out, err
		}
		return loop.run()
	}
	out, err := loop.advanceStep(batch)
	if err != nil || out != nil {
		return out, err
	}
	return loop.run()
}

func restoreContinuationRunInput(input *RunInput, checkpoint *workflowCheckpoint) error {
	if err := validateContinuationIdentity(input, checkpoint); err != nil {
		return err
	}

	input.ParentRunID = checkpoint.Context.ParentRunID
	input.ParentAgentID = checkpoint.Context.ParentAgentID
	input.ParentToolCallID = checkpoint.Context.ParentToolCallID
	input.Tool = checkpoint.Context.Tool
	input.ToolArgs = append(rawjson.Message(nil), checkpoint.Context.ToolArgs...)
	input.Labels = cloneLabels(checkpoint.Context.Labels)
	input.Metadata = cloneMetadata(checkpoint.Context.Metadata)
	input.Policy = clonePolicyOverrides(checkpoint.Policy)
	return nil
}

// validateContinuationIdentity ensures the new workflow continues the same
// registered agent and session before any saved state is trusted or restored.
func validateContinuationIdentity(input *RunInput, checkpoint *workflowCheckpoint) error {
	if checkpoint.AgentID != string(input.AgentID) {
		return fmt.Errorf("run continuation agent mismatch: checkpoint=%q input=%q", checkpoint.AgentID, input.AgentID)
	}
	if checkpoint.SessionID != input.SessionID {
		return fmt.Errorf("run continuation session mismatch: checkpoint=%q input=%q", checkpoint.SessionID, input.SessionID)
	}
	if checkpoint.PreviousRunID == input.RunID {
		return fmt.Errorf("run continuation requires a new run id distinct from %q", input.RunID)
	}
	if checkpoint.PreviousTurnID != "" && checkpoint.PreviousTurnID == input.TurnID {
		return fmt.Errorf("run continuation requires a new turn id distinct from %q", input.TurnID)
	}
	return nil
}

func retargetRunContext(previous run.Context, input *RunInput) run.Context {
	previous.RunID = input.RunID
	previous.SessionID = input.SessionID
	previous.TurnID = input.TurnID
	previous.ParentToolCallID = input.ParentToolCallID
	previous.ParentRunID = input.ParentRunID
	previous.ParentAgentID = input.ParentAgentID
	previous.Tool = input.Tool
	previous.ToolArgs = input.ToolArgs
	previous.Labels = input.Labels
	previous.Metadata = input.Metadata
	return previous
}

// checkpointContextFromRun keeps only execution facts that remain valid when a
// continuation starts a new run and turn.
func checkpointContextFromRun(context run.Context) checkpointRunContext {
	return checkpointRunContext{
		ParentToolCallID: context.ParentToolCallID,
		ParentRunID:      context.ParentRunID,
		ParentAgentID:    context.ParentAgentID,
		Tool:             context.Tool,
		ToolArgs:         append(rawjson.Message(nil), context.ToolArgs...),
		Attempt:          context.Attempt,
		Labels:           cloneLabels(context.Labels),
		Metadata:         cloneMetadata(context.Metadata),
		MaxDuration:      context.MaxDuration,
	}
}

// restoreCheckpointRunContext combines saved stable facts with the new run and
// turn identity supplied by the continuation request.
func restoreCheckpointRunContext(saved checkpointRunContext, input *RunInput) run.Context {
	return run.Context{
		RunID:            input.RunID,
		ParentToolCallID: saved.ParentToolCallID,
		ParentRunID:      saved.ParentRunID,
		ParentAgentID:    saved.ParentAgentID,
		SessionID:        input.SessionID,
		TurnID:           input.TurnID,
		Tool:             saved.Tool,
		ToolArgs:         append(rawjson.Message(nil), saved.ToolArgs...),
		Attempt:          saved.Attempt,
		Labels:           cloneLabels(saved.Labels),
		Metadata:         cloneMetadata(saved.Metadata),
		MaxDuration:      saved.MaxDuration,
	}
}

// restoreCheckpointState decodes saved tool results and reconstructs pending
// recovery state from the current checkpoint contract.
func (r *Runtime) restoreCheckpointState(
	checkpoint checkpointRunState,
) (*runLoopState, error) {
	toolEvents := make([]*planner.ToolResult, 0, len(checkpoint.ToolEvents))
	for _, event := range checkpoint.ToolEvents {
		decoded, err := r.decodeCheckpointToolEvent(event)
		if err != nil {
			return nil, err
		}
		toolEvents = append(toolEvents, decoded)
	}
	state := &runLoopState{
		Caps:              checkpoint.Caps,
		NextAttempt:       checkpoint.NextAttempt,
		AggUsage:          checkpoint.Usage,
		Transcript:        checkpoint.Transcript,
		ResponseCommitted: checkpoint.ResponseCommitted,
		ToolEvents:        toolEvents,
		ToolOutputs:       checkpoint.ToolOutputs,
	}
	if len(checkpoint.PendingRecovery) > 0 {
		recoveryCatalog := checkpoint.PendingRecoveryCatalog
		exactTools := correctCallCatalog(checkpoint.PendingRecovery)
		if len(exactTools) > 0 {
			recoveryCatalog = &RecoveryCatalog{Tools: exactTools}
		}
		state.PendingRecovery = pendingToolRecovery{
			outputs: checkpoint.PendingRecovery,
			catalog: recoveryCatalog,
		}
	}
	return state, nil
}

func (r *Runtime) restoreCheckpointBatch(checkpoint checkpointStepBatch, input *RunInput, runContext *run.Context) (stepBatch, error) {
	result := checkpoint.Result
	retargetPlanResult(result, input, runContext)
	records := make([]stepToolRecord, 0, len(checkpoint.Records))
	for _, record := range checkpoint.Records {
		var decoded *planner.ToolResult
		if record.ChildSuspension == nil {
			var err error
			decoded, err = r.decodeCheckpointToolEvent(record.Result)
			if err != nil {
				return stepBatch{}, err
			}
		} else {
			decoded = &planner.ToolResult{Name: record.Call.Name, ToolCallID: record.Call.ToolCallID}
		}
		records = append(records, stepToolRecord{
			call:             record.Call,
			result:           decoded,
			callRunID:        record.CallRunID,
			resultRunID:      record.ResultRunID,
			resultJSON:       record.ResultJSON,
			resultPublished:  record.ResultPublished,
			resultRecord:     record.ResultRecord,
			scheduleRequired: record.ScheduleRequired,
			scheduleQueue:    record.ScheduleQueue,
			scheduleExact:    record.ScheduleExact,
			expectedChildren: record.ExpectedChildren,
			duration:         record.Duration,
			clarification:    record.Clarification,
			childSuspension:  record.ChildSuspension,
			requiresResume:   record.RequiresResume,
		})
	}
	var finalize *stepFinalization
	if checkpoint.Finalize != nil {
		finalize = &stepFinalization{reason: *checkpoint.Finalize}
	}
	return stepBatch{
		program: stepProgram{
			result:     result,
			calls:      retargetToolRequests(checkpoint.Calls, input, runContext),
			awaitItems: checkpoint.AwaitItems,
			kind:       checkpoint.Kind,
		},
		records:                   records,
		recorded:                  checkpoint.Recorded,
		budgetCost:                checkpoint.BudgetCost,
		timedOut:                  checkpoint.TimedOut,
		awaited:                   true,
		confirmations:             checkpoint.Confirmations,
		awaitItems:                checkpoint.AwaitCount,
		finalize:                  finalize,
		resumePlannerAfterPending: checkpoint.ResumePlannerAfterPending,
	}, nil
}

func retargetPlanResult(result *PlanResult, input *RunInput, runContext *run.Context) {
	result.ToolCalls = retargetToolRequests(result.ToolCalls, input, runContext)
}

func retargetToolRequests(calls []ToolCall, input *RunInput, runContext *run.Context) []ToolCall {
	retargeted := make([]ToolCall, len(calls))
	for i, call := range calls {
		retargeted[i] = retargetToolRequest(call, input, runContext)
	}
	return retargeted
}

func retargetToolRequest(call ToolCall, input *RunInput, runContext *run.Context) ToolCall {
	call.AgentID = input.AgentID
	call.RunID = input.RunID
	call.SessionID = input.SessionID
	call.TurnID = input.TurnID
	call.ParentToolCallID = runContext.ParentToolCallID
	call.Labels = input.Labels
	return call
}

func (r *Runtime) restorePendingInputs(checkpoint []checkpointPendingInput, input *RunInput, runContext *run.Context) []checkpointPendingInput {
	pending := make([]checkpointPendingInput, len(checkpoint))
	for i, item := range checkpoint {
		pending[i] = item
		if item.Confirmation == nil {
			continue
		}
		confirmation := *item.Confirmation
		confirmation.Call = retargetToolRequest(confirmation.Call, input, runContext)
		pending[i].Confirmation = &confirmation
	}
	return pending
}

func publicPendingInputs(pending []checkpointPendingInput) ([]*api.PendingInput, error) {
	items := make([]*api.PendingInput, 0, len(pending))
	for i, item := range pending {
		variants := 0
		if item.Child != nil {
			variants++
		}
		if item.Confirmation != nil {
			variants++
		}
		if item.Await != nil {
			variants++
		}
		if variants != 1 {
			return nil, fmt.Errorf("run suspension pending input %d has %d variants", i, variants)
		}
		if item.Child != nil {
			if item.Child.Suspension == nil || len(item.Child.Suspension.Pending) == 0 {
				return nil, fmt.Errorf("run suspension pending child %d has no visible request", i)
			}
			childPending := item.Child.Suspension.Pending[0]
			if err := validatePendingInput(childPending); err != nil {
				return nil, fmt.Errorf("run suspension pending child %d: %w", i, err)
			}
			items = append(items, childPending)
			continue
		}
		if item.Confirmation != nil {
			pending := &api.PendingInput{
				Kind: api.PendingInputKindConfirmation,
				Confirmation: &api.PendingConfirmation{
					ID:         item.Confirmation.ID,
					Title:      item.Confirmation.Title,
					Prompt:     item.Confirmation.Prompt,
					ToolName:   item.Confirmation.Call.Name,
					ToolCallID: item.Confirmation.Call.ToolCallID,
					Payload:    append(rawjson.Message(nil), item.Confirmation.Call.Payload...),
				},
			}
			if err := validatePendingInput(pending); err != nil {
				return nil, fmt.Errorf("run suspension pending input %d: %w", i, err)
			}
			items = append(items, pending)
			continue
		}
		kind, err := pendingKindForAwait(item.Await.Kind)
		if err != nil {
			return nil, fmt.Errorf("run suspension pending input %d: %w", i, err)
		}
		pending := &api.PendingInput{
			Kind:  kind,
			Await: item.Await,
		}
		if err := validatePendingInput(pending); err != nil {
			return nil, fmt.Errorf("run suspension pending input %d: %w", i, err)
		}
		items = append(items, pending)
	}
	return items, nil
}

// consumePendingInput applies one response to the first pending request. It
// never searches later requests or accepts an ID from a different queue item.
func (l *workflowLoop) consumePendingInput(batch *stepBatch, pending *[]checkpointPendingInput, response *api.PendingInputResponse) error {
	if len(*pending) == 0 {
		return errors.New("run continuation has no pending input")
	}
	current := (*pending)[0]
	generated, err := l.consumeCheckpointInput(batch, current, response)
	if err != nil {
		return err
	}
	*pending = append((*pending)[1:], generated...)
	return nil
}

func (l *workflowLoop) consumeCheckpointInput(batch *stepBatch, pending checkpointPendingInput, response *api.PendingInputResponse) ([]checkpointPendingInput, error) {
	if response == nil {
		return nil, errors.New("run continuation response is required")
	}
	if pending.Child != nil {
		return l.applyChildContinuation(batch, pending.Child, response)
	}
	if pending.Confirmation != nil {
		if response.Confirmation == nil || response.Clarification != nil || response.ToolResults != nil {
			return nil, errors.New("run continuation requires a confirmation response")
		}
		records, clarificationItems, timedOut, err := l.applyConfirmationDecision(response.Confirmation, pending.Confirmation)
		batch.records = append(batch.records, records...)
		batch.timedOut = batch.timedOut || timedOut
		if err != nil {
			return nil, err
		}
		generated := make([]checkpointPendingInput, 0, len(clarificationItems))
		for i := range clarificationItems {
			item := clarificationItems[i]
			if err := l.r.publishAwaitToolUses(l.wfCtx.Context(), l.input, l.base, l.turnID, item, i); err != nil {
				return nil, err
			}
			generated = append(generated, checkpointPendingInput{Await: &item})
		}
		return generated, nil
	}
	if pending.Await == nil {
		return nil, errors.New("run continuation contains an empty pending request")
	}
	records, err := l.applyAwaitResponse(*pending.Await, pending.CallRunID, response)
	batch.records = append(batch.records, records...)
	return nil, err
}

func (l *workflowLoop) applyConfirmationDecision(decision *api.ConfirmationDecision, pending *checkpointConfirmation) ([]stepToolRecord, []planner.AwaitItem, bool, error) {
	if decision.ID != pending.ID {
		return nil, nil, false, fmt.Errorf("confirmation response id %q does not match pending id %q", decision.ID, pending.ID)
	}
	deniedResult, err := l.r.unmarshalToolValue(l.wfCtx.Context(), pending.Call.Name, pending.DeniedResult.RawMessage(), false)
	if err != nil {
		return nil, nil, false, fmt.Errorf("decode denied result for %s: %w", pending.Call.Name, err)
	}
	plan := &confirmationPlan{Title: pending.Title, Prompt: pending.Prompt, DeniedResult: deniedResult}
	return l.r.resolveConfirmationDecision(l.wfCtx, l.reg, l.input, l.base, l.toolOpts, pending.Call, pending.ID, plan, decision, pending.ExpectedChildren, l.parentTracker, l.turnID, &l.deadlines)
}

func (l *workflowLoop) applyAwaitResponse(item planner.AwaitItem, callRunID string, response *api.PendingInputResponse) ([]stepToolRecord, error) {
	switch item.Kind {
	case planner.AwaitItemKindClarification, planner.AwaitItemKindToolClarification:
		if response.Clarification == nil || response.Confirmation != nil || response.ToolResults != nil {
			return nil, errors.New("run continuation requires a clarification response")
		}
		return l.r.consumeClarificationResponse(l.wfCtx.Context(), l.input, l.base, l.parentTracker, l.turnID, item, callRunID, response.Clarification)
	case planner.AwaitItemKindQuestions, planner.AwaitItemKindExternalTools:
		if response.ToolResults == nil || response.Clarification != nil || response.Confirmation != nil {
			return nil, errors.New("run continuation requires a tool-results response")
		}
		return l.r.consumeToolResultsResponse(l.wfCtx.Context(), l.input, l.base, l.parentTracker, l.turnID, item, callRunID, response.ToolResults)
	default:
		return nil, fmt.Errorf("unknown await item kind %q", item.Kind)
	}
}

// applyChildContinuation starts the suspended agent tool on a new child
// workflow. The parent tool call remains open until the child produces its
// final result; another child suspension replaces the pending request exactly.
func (l *workflowLoop) applyChildContinuation(batch *stepBatch, pending *checkpointChildContinuation, response *api.PendingInputResponse) ([]checkpointPendingInput, error) {
	recordIndex := -1
	for i := range batch.records {
		if batch.records[i].call.ToolCallID == pending.ToolCallID {
			recordIndex = i
			break
		}
	}
	if recordIndex < 0 {
		return nil, fmt.Errorf("child continuation references unknown tool_call_id %s", pending.ToolCallID)
	}
	record := &batch.records[recordIndex]
	if record.childSuspension == nil || record.childSuspension.ID != pending.Suspension.ID {
		return nil, fmt.Errorf("child continuation does not match tool_call_id %s", pending.ToolCallID)
	}

	_, specOK := l.r.toolSpec(record.call.Name)
	_, toolset, toolsetOK := l.r.toolsetForTool(record.call.Name)
	if !specOK || !toolsetOK || toolset.AgentTool == nil {
		return nil, fmt.Errorf("child continuation tool %q is not a registered agent tool", record.call.Name)
	}
	cfg := toolset.AgentTool

	currentCall := retargetToolRequest(record.call, l.input, &l.base.RunContext)
	nested := run.Context{
		Tool:             currentCall.Name,
		RunID:            NestedRunIDForToolCall(currentCall.RunID, currentCall.Name, currentCall.ToolCallID),
		SessionID:        currentCall.SessionID,
		TurnID:           currentCall.TurnID,
		ParentToolCallID: currentCall.ToolCallID,
		ParentRunID:      currentCall.RunID,
		ParentAgentID:    currentCall.AgentID,
		ToolArgs:         append(rawjson.Message(nil), currentCall.Payload...),
		Labels:           cloneLabels(currentCall.Labels),
	}
	childInput, err := agentChildRunInput(cfg.Definition, agentChildRequest{runContext: nested})
	if err != nil {
		return nil, err
	}
	childInput.Continuation = &api.RunContinuationInput{
		Suspension: pending.Suspension,
		Response:   response,
	}
	route := cfg.Definition.route
	handle, err := l.wfCtx.StartChildWorkflow(l.wfCtx.Context(), engine.ChildWorkflowRequest{
		ID:        childInput.RunID,
		Workflow:  route.WorkflowName,
		TaskQueue: route.DefaultTaskQueue,
		Input:     childInput,
	})
	if err != nil {
		return nil, err
	}
	out, err := handle.Get(l.wfCtx.Detached().Context())
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errors.New("child continuation returned no output")
	}
	if err := validateWorkflowOutput(out, route.ID, childInput.RunID); err != nil {
		return nil, err
	}
	if out.Suspension != nil {
		record.childSuspension = out.Suspension
		return []checkpointPendingInput{{Child: &checkpointChildContinuation{
			ToolCallID: record.call.ToolCallID,
			Suspension: out.Suspension,
		}}}, nil
	}
	result, err := l.r.adaptAgentChildOutput(cfg, &currentCall, nested, out)
	if err != nil {
		return nil, err
	}
	record.result = result
	record.childSuspension = nil
	record.requiresResume = true
	return nil, nil
}
