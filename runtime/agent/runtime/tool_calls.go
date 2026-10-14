// Package runtime dispatches accepted tool calls and retains their scheduling
// and result records. Workflows use those same records for normal collection,
// suspension and cancellation, so interrupted work keeps its original identity.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	goa "goa.design/goa/v3/pkg"
)

type (
	// futureInfo bundles a Future with its associated tool call metadata for parallel execution.
	// When tools are launched asynchronously via ExecuteToolActivityAsync, we need to track the
	// future handle alongside the original call details and start time so we can correlate
	// results and measure duration when collecting completed activities.
	futureInfo struct {
		// future is the typed engine Future for this tool call.
		future engine.Future[*ToolOutput]
		// pollTimer waits between Task reads without retaining an activity worker.
		pollTimer       engine.Future[time.Time]
		pollRemainingMs int64
		task            *taskExecution
		// call is the original tool request that was submitted for execution.
		call ToolCall
		// startTime records when the activity was scheduled, used to calculate tool duration.
		startTime time.Time
	}

	// agentChildFutureInfo bundles a child workflow handle with its associated
	// agent-as-tool call metadata so the runtime can fan in results after
	// concurrent child execution.
	agentChildFutureInfo struct {
		// handle is the child workflow handle returned by StartChildWorkflow.
		handle engine.ChildWorkflowHandle
		// call is the original agent-as-tool request submitted for execution.
		call ToolCall
		// cfg carries the agent-tool configuration used to adapt RunOutput.
		cfg *AgentToolConfig
		// nestedRun describes the nested agent run context (run IDs, parents).
		nestedRun run.Context
		// startTime records when the child workflow was started.
		startTime time.Time
	}

	toolScheduleState struct {
		callRunID        string
		published        bool
		queue            string
		expectedChildren int
	}

	// toolCallBatch carries the in-flight execution state for a batch of tool calls.
	//
	// The batch is constructed during dispatch (scheduling activities, starting agent
	// child workflows, and executing inline toolsets) and then consumed during
	// collection to merge results deterministically in the original call order.
	toolCallBatch struct {
		calls []ToolCall

		futures      []futureInfo
		childFutures []agentChildFutureInfo
		inlineByID   map[string]*ToolExecutionResult
		scheduleByID map[string]toolScheduleState

		discoveredIDs []string
	}

	// toolBatchExec bundles the common execution context shared by the helpers in this file.
	//
	// This exists to keep function signatures and call sites small and readable:
	// the batch execution flow is conceptually a single operation, but it needs a
	// lot of shared metadata (run IDs, timers) to be
	// propagated consistently to hooks and result contracts.
	toolBatchExec struct {
		r     *Runtime
		owner *workflowLoop
		batch *toolCallBatch

		activityName   string
		toolActOptions engine.ActivityOptions

		runID        string
		agentID      agent.Ident
		sessionID    string
		turnID       string
		runCtx       *run.Context
		historyEndID string

		expectedChildren int
		parentTracker    *childTracker
		finishBy         time.Time
		taskStates       map[string]*taskExecution
		ownedTasks       map[string]futureInfo
	}
)

const canceledByTimeBudgetMessage = "canceled: time budget reached"

// collectToolCallIDs returns the tool call IDs in the same order as calls.
func collectToolCallIDs(calls []ToolCall) []string {
	ids := make([]string, 0, len(calls))
	for _, call := range calls {
		ids = append(ids, call.ToolCallID)
	}
	return ids
}

func (e *toolBatchExec) normalizeToolCall(call ToolCall) ToolCall {
	if e.runCtx != nil {
		call.TextOnly = e.runCtx.TextOnly
	}
	if call.RunID == "" {
		call.RunID = e.runID
	}
	call.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(e.agentID))}
	if call.SessionID == "" {
		call.SessionID = e.sessionID
	}
	if len(call.Labels) == 0 && e.runCtx != nil && len(e.runCtx.Labels) > 0 {
		call.Labels = cloneLabels(e.runCtx.Labels)
	}
	if call.TurnID == "" {
		call.TurnID = e.turnID
	}
	if e.parentTracker != nil && call.ParentToolCallID == "" {
		call.ParentToolCallID = e.parentTracker.parentToolCallID
	}
	if call.ParentToolCallID == "" && e.runCtx != nil && e.runCtx.ParentToolCallID != "" {
		call.ParentToolCallID = e.runCtx.ParentToolCallID
	}
	return call
}

func parentToolCallID(call ToolCall, runCtx *run.Context) string {
	if call.ParentToolCallID != "" {
		return call.ParentToolCallID
	}
	if runCtx != nil {
		return runCtx.ParentToolCallID
	}
	return ""
}

// toolFailureFromExecutionError classifies activity and child-workflow errors
// at the boundary that observed them. Once this failure crosses into the
// planner, unchanged execution is never repeated.
func toolFailureFromExecutionError(err error, message string) *planner.ToolFailure {
	kind := planner.FailureInternal
	action := planner.RecoveryFinish
	var svcErr *goa.ServiceError
	switch {
	case errors.As(err, &svcErr) && svcErr.Name == "service_unavailable":
		kind = planner.FailureUnavailable
		action = planner.RecoveryReplan
	case errors.Is(err, context.DeadlineExceeded) || temporalerrors.IsNativeTimeout(err):
		kind = planner.FailureTimeout
	}
	return &planner.ToolFailure{
		Kind:  kind,
		Error: planner.NewToolErrorWithCause(message, err),
		Recovery: planner.RecoveryDirective{
			Action: action,
		},
	}
}

// synthesizeToolError converts an ordinary tool execution error into a
// ToolResult and publishes the corresponding ToolResultReceived event.
func (e *toolBatchExec) synthesizeToolError(ctx context.Context, call ToolCall, err error, errMsg string, duration time.Duration) (*ToolExecutionResult, error) {
	spec, ok, specErr := lookupCallSpec(call, e.r.toolSpec)
	if specErr != nil {
		return nil, specErr
	}
	if !ok {
		return e.synthesizeUnknownToolResult(ctx, call, duration)
	}
	failure := toolFailureFromExecutionError(err, errMsg)
	if failure.Kind == planner.FailureTimeout && spec.ReplanOnTimeout {
		failure.Recovery.Action = planner.RecoveryReplan
	}
	toolRes := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure:    failure,
	}
	result := Executed(toolRes)
	result.duration = duration
	resultJSON, err := e.r.materializeToolResult(ctx, call, toolRes)
	if err != nil {
		return result, err
	}
	result.resultRecord, err = e.publishToolResultReceived(ctx, call, result, resultJSON, duration)
	if err != nil {
		return result, err
	}
	result.resultPublished = true
	return result, nil
}

// synthesizeUnknownToolResult converts an unregistered tool call into a tool error result.
//
// Provider adapters may surface hallucinated tool names (for example, when a model
// echoes a tool it saw in prior context but that was not advertised in the current
// request). This must not fail the workflow: the runtime returns an invalid-call
// failure that requires the planner to choose an advertised capability.
func (e *toolBatchExec) synthesizeUnknownToolResult(ctx context.Context, call ToolCall, duration time.Duration) (*ToolExecutionResult, error) {
	toolErr := planner.NewToolError(fmt.Sprintf("unknown tool %q", call.Name))
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureInvalidCall,
			Error: toolErr,
			Recovery: planner.RecoveryDirective{
				Action: planner.RecoveryReplan,
			},
		},
	}
	result := Executed(tr)
	result.duration = duration
	var err error
	result.resultRecord, err = e.publishToolResultReceived(ctx, call, result, nil, duration)
	if err != nil {
		return result, err
	}
	result.resultPublished = true
	return result, nil
}

// synthesizeCanceledExecution records a completed tool handshake for work the
// runtime stopped waiting on because the finalization window was reached.
func (e *toolBatchExec) synthesizeCanceledExecution(ctx context.Context, call ToolCall, duration time.Duration) (*ToolExecutionResult, error) {
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure: &planner.ToolFailure{
			Kind:  planner.FailureTimeout,
			Error: planner.NewToolError(canceledByTimeBudgetMessage),
			Recovery: planner.RecoveryDirective{
				Action: planner.RecoveryFinish,
			},
		},
	}
	result := Executed(tr)
	result.duration = duration
	var resultJSON rawjson.Message
	_, ok, err := lookupCallSpec(call, e.r.toolSpec)
	if err != nil {
		return nil, err
	}
	if ok {
		encoded, err := e.r.materializeToolResult(ctx, call, tr)
		if err != nil {
			return result, err
		}
		resultJSON = encoded
	}
	result.resultRecord, err = e.publishToolResultReceived(ctx, call, result, resultJSON, duration)
	if err != nil {
		return result, err
	}
	result.resultPublished = true
	return result, nil
}

// canonicalizeAndValidateWorkflowToolResult takes ownership of a failed result
// before the workflow records or exposes it. For correct-call recovery, it
// replaces activity or executor evidence with the retained model input and the
// registered example, then validates the complete model-facing result. Only
// calls with a model correlation ID may expose correction evidence because
// runtime-authored calls can contain private execution fields in Payload.
func canonicalizeAndValidateWorkflowToolResult(spec tools.ToolSpec, call ToolCall, tr *planner.ToolResult) error {
	if call.TextOnly {
		spec = spec.ForTextOnly()
	}
	if tr != nil && tr.Failure != nil {
		tr.Failure = planner.CloneToolFailure(tr.Failure)
	}
	if tr != nil && tr.Failure != nil && tr.Failure.Recovery.Action == planner.RecoveryCorrectCall {
		if call.ModelToolCallID == "" {
			return fmt.Errorf(
				"tool %q result is invalid: correct-call recovery requires a model-authored call (tool_call_id=%s)",
				call.Name,
				call.ToolCallID,
			)
		}
		tr.Failure.Recovery.PriorInput = append(rawjson.Message(nil), call.TranscriptPayload()...)
		tr.Failure.Recovery.ExampleJSON = append(rawjson.Message(nil), spec.Payload.ExampleJSON...)
	}
	return validateToolResultContract(spec, call, tr)
}

func (e *toolBatchExec) publishToolResultReceived(
	ctx context.Context,
	call ToolCall,
	execution *ToolExecutionResult,
	resultJSON rawjson.Message,
	duration time.Duration,
) (*RecordActivityInput, error) {
	tr := execution.ToolResult
	// Only a complete, sole action can choose its result before reporting.
	// Timeout groups are smaller than planner steps, so inspect the whole step.
	if e.owner != nil && tr.Failure == nil && execution.Clarification == nil && execution.childSuspension == nil && execution.mcpPending == nil && execution.task == nil {
		plan := e.owner.st.Result
		if len(plan.ToolCalls) == 1 && plan.Await == nil && !plan.SynthesizeAfterTools && plan.ToolCalls[0].ToolCallID == call.ToolCallID && plan.ToolCalls[0].Name == call.Name {
			if e.owner.unfinishedBatch != nil && e.owner.unfinishedBatch.program.kind == stepKindToolTerminal {
				records, err := stepToolRecordsFromExecutions([]ToolCall{call}, []*ToolExecutionResult{execution})
				if err != nil {
					return nil, err
				}
				completed := *e.owner.unfinishedBatch
				completed.records = records
				if err := e.owner.retainCompletedToolTerminalResult(completed); err != nil {
					return nil, err
				}
			}
			spec, _, err := lookupCallSpec(call, e.r.toolSpec)
			if err != nil {
				return nil, err
			}
			if spec.TerminalRun || call.Name == completionTool(e.owner.input) {
				event := &api.ToolEvent{Name: call.Name, ToolCallID: call.ToolCallID, Result: resultJSON, Blocks: tr.Blocks, Bounds: tr.Bounds}
				if err := e.r.retainCompletionToolResult(e.owner.base, e.agentID, event); err != nil {
					return nil, err
				}
			}
		}
	}
	schedule, ok := e.batch.scheduleByID[call.ToolCallID]
	if !ok || schedule.callRunID == "" {
		return nil, fmt.Errorf("tool result %q has no scheduling owner", call.ToolCallID)
	}
	parentID := parentToolCallID(call, e.runCtx)
	preview, err := formatToolResultPreviewForCall(ctx, e.r, &call, tr)
	if err != nil {
		return nil, err
	}
	ev := hooks.NewToolResultReceivedEvent(
		e.runID,
		genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(e.agentID))},
		e.sessionID,
		schedule.callRunID,
		call.Name,
		call.ToolCallID,
		parentID,
		resultJSON,
		tr.ServerData,
		tr.Blocks, preview,
		tr.Bounds,
		duration,
		tr.Telemetry,
		tr.Failure,
	)
	record, err := prepareHookRecordInput(ctx, ev, e.turnID)
	if err != nil {
		return nil, err
	}
	return record, e.r.publishPreparedHook(ctx, record, engine.ActivityOptions{})
}

func (e *toolBatchExec) publishToolCallScheduled(ctx context.Context, call ToolCall, queue string) error {
	ev := newToolCallScheduledEvent(
		e.runID,
		call.Subject,
		e.sessionID,
		call,
		queue,
		call.ParentToolCallID,
		e.expectedChildren,
	)
	return e.r.publishHook(ctx, ev, e.turnID)
}

// newToolCallScheduledEvent projects one complete planner request into its
// durable scheduled-call record. Every workflow path uses this constructor so
// runtime-owned correlation metadata cannot be dropped.
func newToolCallScheduledEvent(
	runID string,
	subject genrunidentity.RunSubject,
	sessionID string,
	call ToolCall,
	queue string,
	parentToolCallID string,
	expectedChildren int,
) *hooks.ToolCallScheduledEvent {
	event := hooks.NewToolCallScheduledEvent(
		runID,
		subject,
		sessionID,
		call.Name,
		call.ToolCallID,
		call.Payload,
		queue,
		parentToolCallID,
		expectedChildren,
	)
	event.ModelToolCallID = call.ModelToolCallID
	event.Registry = call.Registry.Clone()
	event.ContinuationRootToolCallID = call.ContinuationRootToolCallID
	return event
}

func computeToolActivityOptions(wfCtx engine.WorkflowContext, base engine.ActivityOptions, finishBy time.Time) engine.ActivityOptions {
	callOpts := base
	startToClose := base.StartToCloseTimeout
	scheduleToStart := base.ScheduleToStartTimeout
	if !finishBy.IsZero() {
		now := wfCtx.Now()
		if rem := finishBy.Sub(now); rem > 0 {
			if startToClose == 0 || startToClose > rem {
				startToClose = rem
			}
			if scheduleToStart == 0 || scheduleToStart > rem {
				scheduleToStart = rem
			}
		}
	}
	callOpts.StartToCloseTimeout = startToClose
	callOpts.ScheduleToStartTimeout = scheduleToStart
	return callOpts
}

// newToolCallBatch retains the run that owns each accepted scheduling event.
// A resumed execution reads that owner from its saved step; the original call
// still supplies the unchanged identity sent to the tool provider.
func (e *toolBatchExec) newToolCallBatch(calls []ToolCall) (*toolCallBatch, error) {
	b := &toolCallBatch{
		calls:         calls,
		futures:       make([]futureInfo, 0, len(calls)),
		childFutures:  make([]agentChildFutureInfo, 0, len(calls)),
		inlineByID:    make(map[string]*ToolExecutionResult, len(calls)),
		scheduleByID:  make(map[string]toolScheduleState, len(calls)),
		discoveredIDs: make([]string, 0, len(calls)),
	}
	for _, call := range calls {
		callRunID := e.runID
		if call.ExecutionContinuation != nil {
			if e.owner == nil || e.owner.unfinishedBatch == nil {
				return nil, fmt.Errorf("continued tool %q has no saved step", call.ToolCallID)
			}
			callRunID = ""
			for _, record := range e.owner.unfinishedBatch.records {
				if record.call.ToolCallID == call.ToolCallID {
					callRunID = record.callRunID
					break
				}
			}
			if callRunID == "" {
				return nil, fmt.Errorf("continued tool %q has no saved scheduling owner", call.ToolCallID)
			}
		}
		b.scheduleByID[call.ToolCallID] = toolScheduleState{callRunID: callRunID}
	}
	e.batch = b
	if e.owner != nil {
		e.owner.activeToolBatches = append(e.owner.activeToolBatches, e)
	}
	return b, nil
}

func (e *toolBatchExec) dispatchToolCalls(wfCtx engine.WorkflowContext, calls []ToolCall) (*toolCallBatch, error) {
	ctx := wfCtx.Context()
	b, err := e.newToolCallBatch(calls)
	if err != nil {
		return nil, err
	}
	var executionErr error

	for i, call := range calls {
		call = e.normalizeToolCall(call)
		b.calls[i] = call

		spec, hasSpec, err := lookupCallSpec(call, e.r.toolSpec)
		if err != nil {
			executionErr = errors.Join(executionErr, err)
			continue
		}
		if !hasSpec {
			state := b.scheduleByID[call.ToolCallID]
			state.expectedChildren = e.expectedChildren
			if err := e.publishToolCallScheduled(ctx, call, ""); err != nil {
				executionErr = errors.Join(executionErr, err)
				b.scheduleByID[call.ToolCallID] = state
				continue
			}
			state.published = true
			b.scheduleByID[call.ToolCallID] = state
			result, err := e.synthesizeUnknownToolResult(ctx, call, 0)
			if result != nil {
				b.inlineByID[call.ToolCallID] = result
			}
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			if e.parentTracker != nil {
				b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
			}
			continue
		}
		if call.TextOnly {
			call.Payload, err = prepareTextOnlyExecutionPayload(spec, call.Payload)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			b.calls[i] = call
		}
		var ts ToolsetRegistration
		var hasTS bool
		if call.Registry == nil {
			_, ts, hasTS = e.r.toolsetForTool(call.Name)
		}

		queue := ""
		if !spec.IsAgentTool && (call.Registry != nil || hasTS && !ts.Inline) {
			queue = e.toolActOptions.Queue
			if queue == "" {
				queue = ts.TaskQueue
			}
		}
		state := b.scheduleByID[call.ToolCallID]
		state.queue = queue
		state.expectedChildren = e.expectedChildren
		if call.ExecutionContinuation == nil {
			if err := e.publishToolCallScheduled(ctx, call, queue); err != nil {
				executionErr = errors.Join(executionErr, err)
				b.scheduleByID[call.ToolCallID] = state
				continue
			}
		}
		state.published = true
		b.scheduleByID[call.ToolCallID] = state

		if spec.IsAgentTool {
			cfg, err := e.r.selectedAgentToolConfig(call)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			request, stopped, err := e.r.prepareAgentChild(wfCtx, call, e.historyEndID, *e.runCtx)
			if err != nil {
				tr, err := agentToolRequestFailureResult(call, err)
				if err != nil {
					executionErr = errors.Join(executionErr, err)
					continue
				}
				if err := canonicalizeAndValidateWorkflowToolResult(spec, call, tr); err != nil {
					executionErr = errors.Join(executionErr, err)
					continue
				}
				result := Executed(tr)
				b.inlineByID[call.ToolCallID] = result
				result.resultRecord, err = e.publishToolResultReceived(ctx, call, result, nil, 0)
				if err != nil {
					executionErr = errors.Join(executionErr, err)
				} else {
					b.inlineByID[call.ToolCallID].resultPublished = true
				}
				if e.parentTracker != nil {
					b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
				}
				continue
			}
			if stopped != nil {
				// A stopped preparation has no child workflow or tool result.
				// Use the same cleanup as a canceled child; the parent retains
				// its own saved cancellation reason rather than the child's.
				return b, errors.Join(executionErr, context.Canceled)
			}
			input, err := agentChildRunInput(cfg.Definition, request)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			route := cfg.Definition.route
			handle, err := wfCtx.StartChildWorkflow(wfCtx.Context(), engine.ChildWorkflowRequest{ID: input.RunID, Workflow: route.WorkflowName, TaskQueue: route.DefaultTaskQueue, Input: input})
			if err != nil {
				executionErr = errors.Join(
					executionErr,
					fmt.Errorf("failed to start agent child workflow for %s: %w", call.Name, err),
				)
				continue
			}
			b.childFutures = append(b.childFutures, agentChildFutureInfo{
				handle:    handle,
				call:      call,
				cfg:       cfg,
				nestedRun: request.runContext,
				startTime: wfCtx.Now(),
			})
			if e.parentTracker != nil {
				b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
			}
			continue
		}

		// Inline service tools execute within the workflow loop.
		if hasTS && ts.Inline {
			start := wfCtx.Now()
			ctxInline := engine.WithWorkflowContext(run.WithTextOnlyContext(ctx, call.TextOnly), wfCtx)
			executorCall := cloneToolCall(call)
			execResult, err := ts.Execute(ctxInline, &executorCall)
			if err != nil {
				executionErr = errors.Join(
					executionErr,
					fmt.Errorf("inline tool %q failed: %w", call.Name, err),
				)
				continue
			}
			if execResult == nil {
				executionErr = errors.Join(
					executionErr,
					fmt.Errorf("inline tool %q returned nil execution result", call.Name),
				)
				continue
			}
			duration := wfCtx.Now().Sub(start)
			result, resultJSON, clarification, err := e.r.materializeToolExecutionResult(ctxInline, call, execResult)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			b.inlineByID[call.ToolCallID] = &ToolExecutionResult{
				ToolResult:    result,
				Clarification: clarification,
				duration:      duration,
			}
			outcome := b.inlineByID[call.ToolCallID]
			outcome.resultRecord, err = e.publishToolResultReceived(ctx, call, outcome, resultJSON, duration)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
			} else {
				outcome.resultPublished = true
			}
			if e.parentTracker != nil {
				b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
			}
			continue
		}

		// The same activity route performs original calls and every later Task
		// operation. Only the saved operation and sequence differ.
		future, err := e.scheduleToolActivity(wfCtx, call)
		if err != nil {
			executionErr = errors.Join(executionErr, fmt.Errorf("failed to schedule tool %q: %w", call.Name, err))
			continue
		}
		b.futures = append(b.futures, futureInfo{
			future: future, call: call, startTime: wfCtx.Now(), task: cloneTaskExecution(e.taskStates[call.ToolCallID]),
		})
		if e.parentTracker != nil {
			b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
		}
	}

	return b, executionErr
}

func (e *toolBatchExec) maybePublishChildTrackerUpdate(ctx context.Context, discoveredIDs []string) error {
	if e.parentTracker == nil || !e.parentTracker.registerDiscovered(discoveredIDs) || !e.parentTracker.needsUpdate() {
		return nil
	}
	if e.runCtx == nil || e.runCtx.ParentRunID == "" || e.runCtx.ParentAgentID == "" {
		return fmt.Errorf("nested tool tracker requires parent run context")
	}
	ev := hooks.NewToolCallUpdatedEvent(e.runCtx.ParentRunID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(e.runCtx.ParentAgentID))}, e.sessionID, e.parentTracker.parentToolCallID, e.parentTracker.currentTotal())
	if err := e.r.publishHook(ctx, ev, e.turnID); err != nil {
		return err
	}
	e.parentTracker.markUpdated()
	return nil
}

func mergeToolExecutionsInCallOrder(calls []ToolCall, activityByID, inlineByID map[string]*ToolExecutionResult) ([]*ToolExecutionResult, error) {
	results := make([]*ToolExecutionResult, 0, len(calls))
	for _, call := range calls {
		if ar, ok := activityByID[call.ToolCallID]; ok {
			results = append(results, ar)
			continue
		}
		if ir, ok := inlineByID[call.ToolCallID]; ok {
			results = append(results, ir)
			continue
		}
		return nil, fmt.Errorf("missing tool result for %q (%s)", call.Name, call.ToolCallID)
	}
	return results, nil
}

// availableToolExecutionsInCallOrder preserves every concrete outcome collected
// before an execution-layer error, leaving unresolved calls to the step boundary.
func availableToolExecutionsInCallOrder(calls []ToolCall, activityByID, inlineByID map[string]*ToolExecutionResult) []*ToolExecutionResult {
	results := make([]*ToolExecutionResult, 0, len(activityByID)+len(inlineByID))
	for _, call := range calls {
		if result, ok := activityByID[call.ToolCallID]; ok {
			results = append(results, result)
			continue
		}
		if result, ok := inlineByID[call.ToolCallID]; ok {
			results = append(results, result)
		}
	}
	return results
}

// executeToolCalls schedules tool execution (inline, activity, and agent-as-tool child workflows)
// and collects results.
//
// The runtime publishes ToolCallScheduled events in call order, then publishes
// ToolResultReceived events as individual tool executions complete (not necessarily in
// call order). The returned results slice is always merged deterministically in the
// original call order so downstream planner/finalizer behavior remains stable.
//
// expectedChildren indicates how many child tools are expected to be discovered dynamically
// by the tools in this batch (0 if not tracked).
func (r *Runtime) executeToolCalls(owner *workflowLoop, wfCtx engine.WorkflowContext, activityName string, toolActOptions engine.ActivityOptions, agentID agent.Ident, runCtx *run.Context, historyEndID string, calls []ToolCall, expectedChildren int, parentTracker *childTracker, finishBy time.Time, taskStates map[string]*taskExecution) (output []*ToolExecutionResult, timedOutResult bool, resultErr error) {
	if runCtx == nil {
		return nil, false, fmt.Errorf("missing run context")
	}
	for _, call := range calls {
		if call.ToolCallID == "" {
			return nil, false, planner.NewOutputContractError(
				fmt.Errorf("tool %q is missing tool_call_id", call.Name),
			)
		}
	}
	// Runtime-owned workflows use one measured clock for the whole batch.
	// Standalone helper callers retain their existing fixed finishBy timer.
	recoveryWorkflow, runtimeOwned := wfCtx.(*providerRecoveryWorkflowContext)
	if runtimeOwned {
		scoped := *recoveryWorkflow
		scoped.limit = &providerRecoveryWaitLimit{deadline: finishBy, elapsed: scoped.actor.elapsed}
		wfCtx = &scoped
	}
	exec := &toolBatchExec{
		r:                r,
		owner:            owner,
		activityName:     activityName,
		toolActOptions:   toolActOptions,
		runID:            runCtx.RunID,
		agentID:          agentID,
		sessionID:        runCtx.SessionID,
		turnID:           runCtx.TurnID,
		runCtx:           runCtx,
		historyEndID:     historyEndID,
		expectedChildren: expectedChildren,
		parentTracker:    parentTracker,
		finishBy:         finishBy,
		taskStates:       taskStates,
		ownedTasks:       make(map[string]futureInfo),
	}
	// Saved Tasks belong to this batch before any scheduling or deadline check.
	// A canceled wait or failed schedule must still deliver their cancellation.
	for _, call := range calls {
		if task := taskStates[call.ToolCallID]; task != nil {
			exec.ownedTasks[call.ToolCallID] = futureInfo{call: exec.normalizeToolCall(call), task: cloneTaskExecution(task)}
		}
	}
	var batch *toolCallBatch
	// Before dispatch there is no batch finalization. Saved Tasks still need
	// cancellation if the deadline or timer setup stops this call.
	defer func() {
		if batch == nil && (resultErr != nil || timedOutResult) {
			resultErr = errors.Join(resultErr, exec.cancelAcceptedTasks(wfCtx))
		}
	}()

	ctx := wfCtx.Context()
	if !finishBy.IsZero() && !wfCtx.Now().Before(finishBy) {
		batch, err := exec.newToolCallBatch(calls)
		if err != nil {
			return nil, false, err
		}
		results := make([]*ToolExecutionResult, 0, len(calls))
		var executionErr error
		for _, call := range calls {
			call = exec.normalizeToolCall(call)
			queue := ""
			_, ok := r.toolSpec(call.Name)
			if ok {
				_, ts, hasTS := r.toolsetForTool(call.Name)
				if hasTS && !ts.Inline {
					queue = toolActOptions.Queue
					if queue == "" {
						queue = ts.TaskQueue
					}
				}
			}
			schedulePublished := true
			if call.ExecutionContinuation == nil {
				if err := exec.publishToolCallScheduled(ctx, call, queue); err != nil {
					executionErr = errors.Join(executionErr, err)
					schedulePublished = false
				}
			}
			state := batch.scheduleByID[call.ToolCallID]
			state.published = schedulePublished
			state.queue = queue
			state.expectedChildren = expectedChildren
			batch.scheduleByID[call.ToolCallID] = state

			result, err := exec.synthesizeCanceledExecution(ctx, call, 0)
			if result != nil {
				result.schedulePublished = schedulePublished
				result.scheduleQueue = queue
				result.expectedChildren = expectedChildren
				results = append(results, result)
			}
			if err != nil {
				executionErr = errors.Join(executionErr, err)
			}
		}
		return results, true, executionErr
	}

	execWfCtx, cancelExec := wfCtx.WithCancel()
	execCanceled := false
	cancelExecOnce := func() {
		if execCanceled {
			return
		}
		execCanceled = true
		if cancelExec != nil {
			cancelExec()
		}
	}
	defer cancelExecOnce()

	var finalizeTimer engine.Future[time.Time]
	if !runtimeOwned && !finishBy.IsZero() {
		d := finishBy.Sub(wfCtx.Now())
		t, err := wfCtx.NewTimer(ctx, d)
		if err != nil {
			return nil, false, err
		}
		finalizeTimer = t
	}

	batch, executionErr := exec.dispatchToolCalls(execWfCtx, calls)
	if batch == nil {
		return nil, false, executionErr
	}
	defer func() {
		if resultErr != nil || timedOutResult {
			resultErr = errors.Join(resultErr, exec.retainReadyChildResults(wfCtx.Detached()))
			retained := make(map[string]*ToolExecutionResult, len(output))
			for _, result := range output {
				id, err := executionToolCallID(result)
				if err != nil {
					resultErr = errors.Join(resultErr, err)
					continue
				}
				retained[id] = result
			}
			for id, result := range batch.inlineByID {
				retained[id] = result
			}
			completeToolScheduleState(batch, retained, nil)
			output = availableToolExecutionsInCallOrder(batch.calls, retained, batch.inlineByID)
		}
	}()
	if err := exec.maybePublishChildTrackerUpdate(ctx, batch.discoveredIDs); err != nil {
		executionErr = errors.Join(executionErr, err)
	}

	activityByID := make(map[string]*ToolExecutionResult)
	var pendingActs []futureInfo
	pendingChildren := batch.childFutures
	var timedOut bool
	if !isRunCancellationError(executionErr) {
		var err error
		activityByID, pendingActs, timedOut, err = exec.collectActivityResultsAsComplete(wfCtx, batch.futures, finalizeTimer)
		executionErr = errors.Join(executionErr, err)
	}
	if !isRunCancellationError(executionErr) {
		_, remainingChildren, childrenTimedOut, err := exec.collectAgentChildResults(wfCtx, batch.childFutures, batch.inlineByID, finalizeTimer)
		pendingChildren = remainingChildren
		timedOut = timedOut || childrenTimedOut
		executionErr = errors.Join(executionErr, err)
	}
	if isRunCancellationError(executionErr) {
		timedOut = false
	}

	// Every dispatched batch settles its live children and polling Tasks before
	// returning. Input requests retain their Tasks for workflow cleanup, and
	// cleanup errors remain part of the returned failure.
	if executionErr != nil || timedOut {
		cancelExecOnce()
		executionErr = errors.Join(
			executionErr,
			exec.cancelAndWaitForAgentChildren(wfCtx, pendingChildren),
			exec.cancelAcceptedTasks(wfCtx),
		)
	}

	if timedOut {
		// Synthesize tool results for in-flight activities/children so the planner sees a
		// complete tool_use → tool_result handshake even when we stop waiting to finalize.
		for _, info := range pendingActs {
			if info.call.ToolCallID == "" {
				continue
			}
			if _, ok := activityByID[info.call.ToolCallID]; ok {
				continue
			}
			result, err := exec.synthesizeCanceledExecution(ctx, info.call, wfCtx.Now().Sub(info.startTime))
			if result != nil {
				activityByID[info.call.ToolCallID] = result
			}
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
		}

		for _, info := range pendingChildren {
			if info.call.ToolCallID == "" {
				continue
			}
			if _, ok := batch.inlineByID[info.call.ToolCallID]; ok {
				continue
			}
			result, err := exec.synthesizeCanceledExecution(ctx, info.call, wfCtx.Now().Sub(info.startTime))
			if result != nil {
				batch.inlineByID[info.call.ToolCallID] = result
			}
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
		}
	}

	completeToolScheduleState(batch, activityByID, executionErr)
	if executionErr != nil {
		return availableToolExecutionsInCallOrder(batch.calls, activityByID, batch.inlineByID), timedOut, executionErr
	}
	merged, err := mergeToolExecutionsInCallOrder(batch.calls, activityByID, batch.inlineByID)
	if err != nil {
		return nil, false, err
	}
	return merged, timedOut, nil
}

// cancelAndWaitForAgentChildren keeps the parent alive until every child it
// started has finished cancellation. Child workflows use that time to store
// their terminal state before the parent can store its own terminal state.
func (e *toolBatchExec) cancelAndWaitForAgentChildren(wfCtx engine.WorkflowContext, children []agentChildFutureInfo) error {
	if len(children) == 0 {
		return nil
	}
	ctx := wfCtx.Detached().Context()
	var waitErr error
	for _, info := range children {
		if err := info.handle.Cancel(ctx); err != nil {
			waitErr = errors.Join(waitErr, err)
		}
	}
	for _, info := range children {
		if _, err := info.handle.Get(ctx); err != nil && !temporalerrors.CancellationOnly(err) {
			waitErr = errors.Join(waitErr, err)
		}
	}
	return waitErr
}

// completeToolScheduleState attaches saved schedule details to received outcomes.
// Missing outcomes become failures only for substantive errors; cancellation
// closes the run without inventing failed tool results.
func completeToolScheduleState(
	batch *toolCallBatch,
	activityByID map[string]*ToolExecutionResult,
	executionErr error,
) {
	for _, call := range batch.calls {
		result := activityByID[call.ToolCallID]
		if result == nil {
			result = batch.inlineByID[call.ToolCallID]
		}
		if result == nil && executionErr != nil && !temporalerrors.CancellationOnly(executionErr) {
			result = Executed(&planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Failure: &planner.ToolFailure{
					Kind: planner.FailureInternal,
					Error: planner.NewToolError(
						"tool execution did not produce a result: " + executionErr.Error(),
					),
					Recovery: planner.RecoveryDirective{Action: planner.RecoveryFinish},
				},
			})
			batch.inlineByID[call.ToolCallID] = result
		}
		if result == nil {
			continue
		}
		state := batch.scheduleByID[call.ToolCallID]
		result.schedulePublished = state.published
		result.scheduleQueue = state.queue
		result.expectedChildren = state.expectedChildren
	}
}
