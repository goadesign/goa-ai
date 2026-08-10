package runtime

import (
	"context"
	"errors"
	"fmt"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
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
		// call is the original tool request that was submitted for execution.
		call planner.ToolRequest
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
		call planner.ToolRequest
		// cfg carries the agent-tool configuration used to adapt RunOutput.
		cfg *AgentToolConfig
		// nestedRun describes the nested agent run context (run IDs, parents).
		nestedRun run.Context
		// startTime records when the child workflow was started.
		startTime time.Time
	}

	toolScheduleState struct {
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
		calls []planner.ToolRequest

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
		r *Runtime

		activityName   string
		toolActOptions engine.ActivityOptions

		runID     string
		agentID   agent.Ident
		sessionID string
		turnID    string
		runCtx    *run.Context
		messages  []*model.Message

		expectedChildren int
		parentTracker    *childTracker
		finishBy         time.Time
	}
)

const canceledByTimeBudgetMessage = "canceled: time budget reached"

// collectToolCallIDs returns the tool call IDs in the same order as calls.
func collectToolCallIDs(calls []planner.ToolRequest) []string {
	ids := make([]string, 0, len(calls))
	for _, call := range calls {
		ids = append(ids, call.ToolCallID)
	}
	return ids
}

func (e *toolBatchExec) normalizeToolCall(call planner.ToolRequest, i int) planner.ToolRequest {
	if call.SessionID == "" {
		call.SessionID = e.sessionID
	}
	if len(call.Labels) == 0 && e.runCtx != nil && len(e.runCtx.Labels) > 0 {
		call.Labels = cloneLabels(e.runCtx.Labels)
	}
	if call.TurnID == "" {
		call.TurnID = e.turnID
	}
	if call.ToolCallID == "" {
		attempt := 0
		if e.runCtx != nil {
			attempt = e.runCtx.Attempt
		}
		call.ToolCallID = generateDeterministicToolCallID(e.runID, call.TurnID, attempt, call.Name, i)
	}
	if e.parentTracker != nil && call.ParentToolCallID == "" {
		call.ParentToolCallID = e.parentTracker.parentToolCallID
	}
	if call.ParentToolCallID == "" && e.runCtx != nil && e.runCtx.ParentToolCallID != "" {
		call.ParentToolCallID = e.runCtx.ParentToolCallID
	}
	return call
}

func parentToolCallID(call planner.ToolRequest, runCtx *run.Context) string {
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
	if errors.As(err, &svcErr) && svcErr.Name == "service_unavailable" {
		kind = planner.FailureUnavailable
		action = planner.RecoveryReplan
	}
	if errors.Is(err, context.DeadlineExceeded) {
		kind = planner.FailureTimeout
		action = planner.RecoveryFinish
	}
	return &planner.ToolFailure{
		Kind:  kind,
		Error: planner.NewToolErrorWithCause(message, err),
		Recovery: planner.RecoveryDirective{
			Action: action,
		},
	}
}

// synthesizeToolError creates a ToolResult from an execution error and publishes
// the corresponding ToolResultReceived event. This is used when activity or
// child workflow execution fails (e.g., timeout) and we want to convert the
// error into a tool result rather than failing the workflow.
func (e *toolBatchExec) synthesizeToolError(ctx context.Context, call planner.ToolRequest, err error, errMsg string, duration time.Duration) (*ToolExecutionResult, error) {
	toolRes := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Failure:    toolFailureFromExecutionError(err, errMsg),
	}
	if _, ok := e.r.toolSpec(call.Name); !ok {
		return e.synthesizeUnknownToolResult(ctx, call, duration)
	}
	result := Executed(toolRes)
	result.duration = duration
	resultJSON, err := e.r.materializeToolResult(ctx, call, toolRes)
	if err != nil {
		return result, err
	}
	result.resultRecord, err = e.publishToolResultReceived(ctx, call, toolRes, resultJSON, duration)
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
func (e *toolBatchExec) synthesizeUnknownToolResult(ctx context.Context, call planner.ToolRequest, duration time.Duration) (*ToolExecutionResult, error) {
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
	result.resultRecord, err = e.publishToolResultReceived(ctx, call, tr, nil, duration)
	if err != nil {
		return result, err
	}
	result.resultPublished = true
	return result, nil
}

// synthesizeCanceledExecution records a completed tool handshake for work the
// runtime stopped waiting on because the finalization window was reached.
func (e *toolBatchExec) synthesizeCanceledExecution(ctx context.Context, call planner.ToolRequest, duration time.Duration) (*ToolExecutionResult, error) {
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
	if _, ok := e.r.toolSpec(call.Name); ok {
		encoded, err := e.r.materializeToolResult(ctx, call, tr)
		if err != nil {
			return result, err
		}
		resultJSON = encoded
	}
	var err error
	result.resultRecord, err = e.publishToolResultReceived(ctx, call, tr, resultJSON, duration)
	if err != nil {
		return result, err
	}
	result.resultPublished = true
	return result, nil
}

func (r *Runtime) enforceToolResultContracts(spec tools.ToolSpec, call planner.ToolRequest, tr *planner.ToolResult) error {
	return validateToolResultContract(spec, call, tr)
}

func (e *toolBatchExec) publishToolResultReceived(
	ctx context.Context,
	call planner.ToolRequest,
	tr *planner.ToolResult,
	resultJSON rawjson.Message,
	duration time.Duration,
) (*RecordActivityInput, error) {
	parentID := parentToolCallID(call, e.runCtx)
	resultBytes := tr.ResultBytes
	if !tr.ResultOmitted {
		resultBytes = len(resultJSON)
	}
	preview, err := formatToolResultPreviewForCall(ctx, e.r, &call, tr)
	if err != nil {
		return nil, err
	}
	ev := hooks.NewToolResultReceivedEvent(
		e.runID,
		e.agentID,
		e.sessionID,
		call.Name,
		call.ToolCallID,
		parentID,
		resultJSON,
		resultBytes,
		tr.ResultOmitted,
		tr.ResultOmittedReason,
		tr.ServerData,
		preview,
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

func (e *toolBatchExec) publishToolCallScheduled(ctx context.Context, call planner.ToolRequest, queue string) error {
	ev := hooks.NewToolCallScheduledEvent(e.runID, e.agentID, e.sessionID, call.Name, call.ToolCallID, call.Payload, queue, call.ParentToolCallID, e.expectedChildren)
	ev.ContinuationRootToolCallID = call.ContinuationRootToolCallID
	return e.r.publishHook(ctx, ev, e.turnID)
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

func (e *toolBatchExec) dispatchToolCalls(wfCtx engine.WorkflowContext, calls []planner.ToolRequest) (*toolCallBatch, error) {
	ctx := wfCtx.Context()

	b := &toolCallBatch{
		calls:         calls,
		futures:       make([]futureInfo, 0, len(calls)),
		childFutures:  make([]agentChildFutureInfo, 0, len(calls)),
		inlineByID:    make(map[string]*ToolExecutionResult, len(calls)),
		scheduleByID:  make(map[string]toolScheduleState, len(calls)),
		discoveredIDs: make([]string, 0, len(calls)),
	}
	var executionErr error

	for i, call := range calls {
		call = e.normalizeToolCall(call, i)
		b.calls[i] = call

		spec, hasSpec := e.r.toolSpec(call.Name)
		if !hasSpec {
			state := toolScheduleState{expectedChildren: e.expectedChildren}
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
		e.r.mu.RLock()
		ts, hasTS := e.r.toolsets[spec.Toolset]
		e.r.mu.RUnlock()

		queue := ""
		if hasTS && !ts.Inline {
			queue = e.toolActOptions.Queue
			if queue == "" {
				queue = ts.TaskQueue
			}
		}
		state := toolScheduleState{
			queue:            queue,
			expectedChildren: e.expectedChildren,
		}
		if err := e.publishToolCallScheduled(ctx, call, queue); err != nil {
			executionErr = errors.Join(executionErr, err)
			b.scheduleByID[call.ToolCallID] = state
			continue
		}
		state.published = true
		b.scheduleByID[call.ToolCallID] = state

		// Inline toolsets execute within the workflow loop. Their generated codec
		// validates the exact planner-authored payload before typed mapping.
		if hasTS && ts.Inline {
			// Agent-as-tool: start child workflows concurrently and fan in results later.
			if spec.IsAgentTool {
				messages, nestedRunCtx, err := e.r.buildAgentChildRequest(ctx, ts.AgentTool, &call, e.messages, e.runCtx)
				if err != nil {
					tr, err := e.r.agentToolRequestFailureResult(call, err)
					if err != nil {
						executionErr = errors.Join(executionErr, err)
						continue
					}
					result := Executed(tr)
					b.inlineByID[call.ToolCallID] = result
					result.resultRecord, err = e.publishToolResultReceived(ctx, call, tr, nil, 0)
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
				if err := e.r.publishHook(wfCtx.Context(), hooks.NewChildRunLinkedEvent(call.RunID, call.AgentID, call.SessionID, call.Name, call.ToolCallID, nestedRunCtx.RunID, ts.AgentTool.AgentID), ""); err != nil {
					executionErr = errors.Join(executionErr, err)
					continue
				}
				route := ts.AgentTool.Route
				if route.ID == "" || route.WorkflowName == "" || route.DefaultTaskQueue == "" {
					executionErr = errors.Join(
						executionErr,
						fmt.Errorf("agent tool route is incomplete for %s", call.Name),
					)
					continue
				}
				input := RunInput{
					AgentID:          route.ID,
					RunID:            nestedRunCtx.RunID,
					SessionID:        nestedRunCtx.SessionID,
					TurnID:           nestedRunCtx.TurnID,
					ParentToolCallID: nestedRunCtx.ParentToolCallID,
					ParentRunID:      nestedRunCtx.ParentRunID,
					ParentAgentID:    nestedRunCtx.ParentAgentID,
					Tool:             nestedRunCtx.Tool,
					ToolArgs:         nestedRunCtx.ToolArgs,
					Labels:           nestedRunCtx.Labels,
					Messages:         messages,
				}
				handle, err := wfCtx.StartChildWorkflow(wfCtx.Context(), engine.ChildWorkflowRequest{ID: input.RunID, Workflow: route.WorkflowName, TaskQueue: route.DefaultTaskQueue, Input: &input})
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
					cfg:       ts.AgentTool,
					nestedRun: nestedRunCtx,
					startTime: wfCtx.Now(),
				})
				if e.parentTracker != nil {
					b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
				}
				continue
			}

			start := wfCtx.Now()
			ctxInline := engine.WithWorkflowContext(ctx, wfCtx)
			execResult, err := ts.Execute(ctxInline, &call)
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
			result, resultJSON, pause, err := e.r.materializeToolExecutionResult(ctx, call, execResult)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			b.inlineByID[call.ToolCallID] = &ToolExecutionResult{
				ToolResult: result,
				Pause:      pause,
				duration:   duration,
			}
			outcome := b.inlineByID[call.ToolCallID]
			outcome.resultRecord, err = e.publishToolResultReceived(ctx, call, result, resultJSON, duration)
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

		// Activity path (service-backed tools).
		toolInput := ToolInput{
			AgentID:          e.agentID,
			RunID:            e.runID,
			ToolsetName:      spec.Toolset,
			ToolName:         call.Name,
			ToolCallID:       call.ToolCallID,
			Payload:          call.Payload,
			SessionID:        call.SessionID,
			Labels:           cloneLabels(call.Labels),
			TurnID:           call.TurnID,
			ParentToolCallID: call.ParentToolCallID,
		}
		callOpts := computeToolActivityOptions(wfCtx, e.toolActOptions, e.finishBy)
		if callOpts.Queue == "" && hasTS && !ts.Inline && ts.TaskQueue != "" {
			callOpts.Queue = ts.TaskQueue
		}
		future, err := wfCtx.ExecuteToolActivityAsync(engine.ToolActivityCall{
			Name:    e.activityName,
			Input:   &toolInput,
			Options: callOpts,
		})
		if err != nil {
			executionErr = errors.Join(
				executionErr,
				fmt.Errorf("failed to schedule tool %q: %w", call.Name, err),
			)
			continue
		}
		b.futures = append(b.futures, futureInfo{
			future:    future,
			call:      call,
			startTime: wfCtx.Now(),
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
	ev := hooks.NewToolCallUpdatedEvent(e.runCtx.ParentRunID, e.runCtx.ParentAgentID, e.sessionID, e.parentTracker.parentToolCallID, e.parentTracker.currentTotal())
	if err := e.r.publishHook(ctx, ev, e.turnID); err != nil {
		return err
	}
	e.parentTracker.markUpdated()
	return nil
}

func (e *toolBatchExec) collectActivityResultsAsComplete(wfCtx engine.WorkflowContext, futures []futureInfo, finalizeTimer engine.Future[time.Time]) (map[string]*ToolExecutionResult, []futureInfo, bool, error) {
	ctx := wfCtx.Context()
	activityByID := make(map[string]*ToolExecutionResult, len(futures))
	pending := append([]futureInfo(nil), futures...)
	var executionErr error
	for len(pending) > 0 {
		if err := wfCtx.Await(func() bool {
			if finalizeTimer != nil && finalizeTimer.IsReady() {
				return true
			}
			for _, info := range pending {
				if info.future.IsReady() {
					return true
				}
			}
			return false
		}); err != nil {
			return activityByID, pending, false, err
		}

		i := 0
		for i < len(pending) {
			info := pending[i]
			if !info.future.IsReady() {
				i++
				continue
			}
			pending[i] = pending[len(pending)-1]
			pending = pending[:len(pending)-1]

			out, err := info.future.Get(ctx)
			if err != nil {
				duration := wfCtx.Now().Sub(info.startTime)
				result, synthErr := e.synthesizeToolError(ctx, info.call, err, "tool activity failed", duration)
				if result != nil {
					activityByID[info.call.ToolCallID] = result
				}
				if synthErr != nil {
					executionErr = errors.Join(executionErr, synthErr)
				}
				continue
			}
			if out == nil {
				executionErr = errors.Join(
					executionErr,
					fmt.Errorf("tool %q returned nil output", info.call.Name),
				)
				continue
			}

			execResult, err := e.executionFromActivityOutput(ctx, info, out, wfCtx.Now().Sub(info.startTime))
			if execResult != nil {
				activityByID[info.call.ToolCallID] = execResult
			}
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
		}
		if finalizeTimer != nil && finalizeTimer.IsReady() && len(pending) > 0 {
			return activityByID, pending, true, executionErr
		}
	}
	return activityByID, nil, false, executionErr
}

// executionFromActivityOutput decodes and validates one activity result, then
// publishes the canonical result event for the tool call.
func (e *toolBatchExec) executionFromActivityOutput(ctx context.Context, info futureInfo, out *ToolOutput, duration time.Duration) (*ToolExecutionResult, error) {
	spec, ok := e.r.toolSpec(info.call.Name)
	if !ok {
		return e.synthesizeUnknownToolResult(ctx, info.call, duration)
	}

	var decoded any
	if out.Failure == nil && hasNonNullJSON(out.Payload.RawMessage()) {
		v, err := e.r.unmarshalToolValue(ctx, info.call.Name, out.Payload.RawMessage(), false)
		if err != nil {
			return nil, fmt.Errorf("tool %q result decode failed (tool_call_id=%s): %w", info.call.Name, info.call.ToolCallID, err)
		}
		decoded = v
	}

	toolRes := &planner.ToolResult{
		Name:       info.call.Name,
		Result:     decoded,
		Bounds:     out.Bounds,
		ServerData: out.ServerData,
		ToolCallID: info.call.ToolCallID,
		Telemetry:  out.Telemetry,
	}
	toolRes.Failure = out.Failure
	if err := e.r.enforceToolResultContracts(spec, info.call, toolRes); err != nil {
		return nil, err
	}
	if err := validateToolPauseContract(info.call, toolRes, out.Pause); err != nil {
		return nil, err
	}
	result := &ToolExecutionResult{
		ToolResult: toolRes,
		Pause:      out.Pause,
		duration:   duration,
	}
	var err error
	result.resultRecord, err = e.publishToolResultReceived(ctx, info.call, toolRes, out.Payload, duration)
	if err != nil {
		return result, err
	}
	result.resultPublished = true
	return result, nil
}

func (e *toolBatchExec) collectAgentChildResults(wfCtx engine.WorkflowContext, children []agentChildFutureInfo, finalizeTimer engine.Future[time.Time]) (map[string]*ToolExecutionResult, []agentChildFutureInfo, bool, error) {
	ctx := wfCtx.Context()
	if len(children) == 0 {
		return map[string]*ToolExecutionResult{}, nil, false, nil
	}

	out := make(map[string]*ToolExecutionResult, len(children))
	pending := append([]agentChildFutureInfo(nil), children...)
	var executionErr error
	for len(pending) > 0 {
		if err := wfCtx.Await(func() bool {
			if finalizeTimer != nil && finalizeTimer.IsReady() {
				return true
			}
			for _, info := range pending {
				if info.handle.IsReady() {
					return true
				}
			}
			return false
		}); err != nil {
			return out, pending, false, err
		}

		i := 0
		for i < len(pending) {
			info := pending[i]
			if !info.handle.IsReady() {
				i++
				continue
			}
			pending[i] = pending[len(pending)-1]
			pending = pending[:len(pending)-1]

			outPtr, err := info.handle.Get(wfCtx.Context())
			if err != nil {
				duration := wfCtx.Now().Sub(info.startTime)
				result, synthErr := e.synthesizeToolError(ctx, info.call, err, "agent tool execution failed", duration)
				if result != nil {
					out[info.call.ToolCallID] = result
				}
				if synthErr != nil {
					executionErr = errors.Join(executionErr, synthErr)
				}
				continue
			}
			tr, err := e.r.adaptAgentChildOutput(ctx, info.cfg, &info.call, info.nestedRun, outPtr)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			duration := wfCtx.Now().Sub(info.startTime)
			result := Executed(tr)
			result.duration = duration
			out[info.call.ToolCallID] = result
			_, ok := e.r.toolSpec(info.call.Name)
			if !ok {
				result, synthErr := e.synthesizeUnknownToolResult(ctx, info.call, duration)
				if result != nil {
					out[info.call.ToolCallID] = result
				}
				if synthErr != nil {
					executionErr = errors.Join(executionErr, synthErr)
				}
				continue
			}
			resultJSON, err := e.r.materializeToolResult(ctx, info.call, tr)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
				continue
			}
			result.resultRecord, err = e.publishToolResultReceived(ctx, info.call, tr, resultJSON, duration)
			if err != nil {
				executionErr = errors.Join(executionErr, err)
			} else {
				out[info.call.ToolCallID].resultPublished = true
			}
		}
		if finalizeTimer != nil && finalizeTimer.IsReady() && len(pending) > 0 {
			return out, pending, true, executionErr
		}
	}
	return out, nil, false, executionErr
}

func mergeToolExecutionsInCallOrder(calls []planner.ToolRequest, activityByID, inlineByID map[string]*ToolExecutionResult) ([]*ToolExecutionResult, error) {
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
func availableToolExecutionsInCallOrder(calls []planner.ToolRequest, activityByID, inlineByID map[string]*ToolExecutionResult) []*ToolExecutionResult {
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
func (r *Runtime) executeToolCalls(wfCtx engine.WorkflowContext, activityName string, toolActOptions engine.ActivityOptions, agentID agent.Ident, runCtx *run.Context, messages []*model.Message, calls []planner.ToolRequest, expectedChildren int, parentTracker *childTracker, finishBy time.Time) ([]*ToolExecutionResult, bool, error) {
	if runCtx == nil {
		return nil, false, fmt.Errorf("missing run context")
	}
	exec := &toolBatchExec{
		r:                r,
		activityName:     activityName,
		toolActOptions:   toolActOptions,
		runID:            runCtx.RunID,
		agentID:          agentID,
		sessionID:        runCtx.SessionID,
		turnID:           runCtx.TurnID,
		runCtx:           runCtx,
		messages:         messages,
		expectedChildren: expectedChildren,
		parentTracker:    parentTracker,
		finishBy:         finishBy,
	}

	ctx := wfCtx.Context()
	if !finishBy.IsZero() && !wfCtx.Now().Before(finishBy) {
		results := make([]*ToolExecutionResult, 0, len(calls))
		var executionErr error
		for i, call := range calls {
			call = exec.normalizeToolCall(call, i)
			queue := ""
			spec, ok := r.toolSpec(call.Name)
			if ok {
				r.mu.RLock()
				ts, hasTS := r.toolsets[spec.Toolset]
				r.mu.RUnlock()
				if hasTS && !ts.Inline {
					queue = toolActOptions.Queue
					if queue == "" {
						queue = ts.TaskQueue
					}
				}
			}
			schedulePublished := true
			if err := exec.publishToolCallScheduled(ctx, call, queue); err != nil {
				executionErr = errors.Join(executionErr, err)
				schedulePublished = false
			}

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
	if !finishBy.IsZero() {
		d := finishBy.Sub(wfCtx.Now())
		t, err := wfCtx.NewTimer(ctx, d)
		if err != nil {
			return nil, false, err
		}
		finalizeTimer = t
	}

	batch, executionErr := exec.dispatchToolCalls(execWfCtx, calls)
	if err := exec.maybePublishChildTrackerUpdate(ctx, batch.discoveredIDs); err != nil {
		executionErr = errors.Join(executionErr, err)
	}

	activityByID, pendingActs, timedOutActs, err := exec.collectActivityResultsAsComplete(wfCtx, batch.futures, finalizeTimer)
	if err != nil {
		executionErr = errors.Join(executionErr, err)
	}

	childByID, pendingChildren, timedOutChildren, err := exec.collectAgentChildResults(wfCtx, batch.childFutures, finalizeTimer)
	if err != nil {
		executionErr = errors.Join(executionErr, err)
	}
	if executionErr != nil {
		cancelExecOnce()
	}

	timedOut := timedOutActs || timedOutChildren
	if timedOut {
		cancelExecOnce()
	}

	for id, tr := range childByID {
		batch.inlineByID[id] = tr
	}

	if timedOut {
		for _, info := range pendingChildren {
			if info.handle != nil {
				if err := info.handle.Cancel(ctx); err != nil {
					executionErr = errors.Join(executionErr, err)
				}
			}
		}

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

// completeToolScheduleState attaches the canonical schedule envelope to every
// outcome and materializes failures for calls that dispatch could not start.
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
		if result == nil && executionErr != nil {
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
