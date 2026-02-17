package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
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

	// toolCallBatch carries the in-flight execution state for a batch of tool calls.
	//
	// The batch is constructed during dispatch (scheduling activities, starting agent
	// child workflows, and executing inline toolsets) and then consumed during
	// collection to merge results deterministically in the original call order.
	toolCallBatch struct {
		calls []planner.ToolRequest

		futures      []futureInfo
		childFutures []agentChildFutureInfo
		inlineByID   map[string]*planner.ToolResult

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

		expectedChildren int
		parentTracker    *childTracker
		finishBy         time.Time
	}
)

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

func retryHintFromExecutionError(tool tools.Ident, err error) *planner.RetryHint {
	var svcErr *goa.ServiceError
	if errors.As(err, &svcErr) && svcErr.Name == "service_unavailable" {
		return &planner.RetryHint{
			Reason: planner.RetryReasonToolUnavailable,
			Tool:   tool,
			Message: "Tool execution failed because the provider is temporarily unavailable. " +
				"Retry the same tool call with the same payload.",
		}
	}
	return nil
}

// synthesizeToolError creates a ToolResult from an execution error and publishes
// the corresponding ToolResultReceived event. This is used when activity or
// child workflow execution fails (e.g., timeout) and we want to convert the
// error into a tool result rather than failing the workflow.
func (e *toolBatchExec) synthesizeToolError(ctx context.Context, call planner.ToolRequest, err error, errMsg string, duration time.Duration) (*planner.ToolResult, error) {
	toolErr := planner.NewToolErrorWithCause(errMsg, err)
	toolRes := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Error:      toolErr,
		RetryHint:  retryHintFromExecutionError(call.Name, err),
	}
	spec, ok := e.r.toolSpec(call.Name)
	if !ok {
		return e.synthesizeUnknownToolResult(ctx, call, duration)
	}
	if err := e.r.enforceToolResultContracts(spec, call, toolErr, toolRes); err != nil {
		return nil, err
	}

	if err := e.publishToolResultReceived(ctx, call, toolRes, nil, duration); err != nil {
		return nil, err
	}
	return toolRes, nil
}

// synthesizeUnknownToolResult converts an unregistered tool call into a tool error result.
//
// Provider adapters may surface hallucinated tool names (for example, when a model
// echoes a tool it saw in prior context but that was not advertised in the current
// request). This must not fail the workflow: the runtime returns a tool result error
// with a RetryHint so the planner can resume and the model can recover.
func (e *toolBatchExec) synthesizeUnknownToolResult(ctx context.Context, call planner.ToolRequest, duration time.Duration) (*planner.ToolResult, error) {
	toolErr := planner.NewToolError(fmt.Sprintf("unknown tool %q", call.Name))
	tr := &planner.ToolResult{
		Name:       call.Name,
		ToolCallID: call.ToolCallID,
		Error:      toolErr,
		RetryHint: &planner.RetryHint{
			Reason:         planner.RetryReasonToolUnavailable,
			Tool:           call.Name,
			RestrictToTool: false,
			Message:        "Tool name is not registered for this run. Choose a tool from the advertised tool list and call it with the exact JSON schema.",
		},
	}
	if err := e.publishToolResultReceived(ctx, call, tr, nil, duration); err != nil {
		return nil, err
	}
	return tr, nil
}

func (r *Runtime) enforceToolResultContracts(spec tools.ToolSpec, call planner.ToolRequest, toolErr *planner.ToolError, tr *planner.ToolResult) error {
	if tr == nil {
		return fmt.Errorf("CRITICAL: nil tool result for %q (%s)", call.Name, call.ToolCallID)
	}
	// Derive Bounds from the decoded result when the result type implements
	// agent.BoundedResult and the executor did not populate Bounds explicitly.
	if tr.Bounds == nil {
		if b := deriveBounds(tr.Result); b != nil {
			tr.Bounds = b
		}
	}
	if spec.BoundedResult && toolErr == nil && tr.Bounds == nil {
		return fmt.Errorf("bounded tool %q returned result without bounds (tool_call_id=%s, type=%T)", call.Name, call.ToolCallID, tr.Result)
	}
	return nil
}

func (e *toolBatchExec) publishToolResultReceived(ctx context.Context, call planner.ToolRequest, tr *planner.ToolResult, resultJSON json.RawMessage, duration time.Duration) error {
	parentID := parentToolCallID(call, e.runCtx)
	ev := hooks.NewToolResultReceivedEvent(
		e.runID,
		e.agentID,
		e.sessionID,
		call.Name,
		call.ToolCallID,
		parentID,
		tr.Result,
		resultJSON,
		tr.ServerData,
		formatResultPreview(call.Name, tr.Result),
		tr.Bounds,
		duration,
		tr.Telemetry,
		tr.RetryHint,
		tr.Error,
	)
	return e.r.publishHook(ctx, ev, e.turnID)
}

func (e *toolBatchExec) publishToolCallScheduled(ctx context.Context, call planner.ToolRequest, queue string) error {
	ev := hooks.NewToolCallScheduledEvent(e.runID, e.agentID, e.sessionID, call.Name, call.ToolCallID, call.Payload, queue, call.ParentToolCallID, e.expectedChildren)
	return e.r.publishHook(ctx, ev, e.turnID)
}

func computeToolActivityOptions(wfCtx engine.WorkflowContext, base engine.ActivityOptions, finishBy time.Time) engine.ActivityOptions {
	callOpts := base
	timeout := base.Timeout
	if !finishBy.IsZero() {
		now := wfCtx.Now()
		if rem := finishBy.Sub(now); rem > 0 {
			if timeout == 0 || timeout > rem {
				timeout = rem
			}
		}
	}
	callOpts.Timeout = timeout
	return callOpts
}

func (e *toolBatchExec) dispatchToolCalls(wfCtx engine.WorkflowContext, calls []planner.ToolRequest) (*toolCallBatch, error) {
	ctx := wfCtx.Context()

	b := &toolCallBatch{
		calls:         calls,
		futures:       make([]futureInfo, 0, len(calls)),
		childFutures:  make([]agentChildFutureInfo, 0, len(calls)),
		inlineByID:    make(map[string]*planner.ToolResult, len(calls)),
		discoveredIDs: make([]string, 0, len(calls)),
	}

	for i, call := range calls {
		call = e.normalizeToolCall(call, i)
		b.calls[i] = call

		spec, hasSpec := e.r.toolSpec(call.Name)
		if !hasSpec {
			if err := e.publishToolCallScheduled(ctx, call, ""); err != nil {
				return nil, err
			}
			tr, err := e.synthesizeUnknownToolResult(ctx, call, 0)
			if err != nil {
				return nil, err
			}
			b.inlineByID[call.ToolCallID] = tr
			if e.parentTracker != nil {
				b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
			}
			continue
		}
		e.r.mu.RLock()
		ts, hasTS := e.r.toolsets[spec.Toolset]
		e.r.mu.RUnlock()

		queue := ""
		if hasTS && ts.TaskQueue != "" {
			queue = ts.TaskQueue
		}
		if err := e.publishToolCallScheduled(ctx, call, queue); err != nil {
			return nil, err
		}

		// Inline toolsets execute within the workflow loop.
		if hasTS && ts.Inline {
			raw := call.Payload
			if ts.PayloadAdapter != nil && len(raw) > 0 {
				meta := ToolCallMeta{
					RunID:            call.RunID,
					SessionID:        call.SessionID,
					TurnID:           call.TurnID,
					ToolCallID:       call.ToolCallID,
					ParentToolCallID: call.ParentToolCallID,
				}
				if adapted, err := ts.PayloadAdapter(ctx, meta, call.Name, raw); err == nil && len(adapted) > 0 {
					raw = adapted
				} else if err != nil {
					return nil, fmt.Errorf("inline payload adapter failed for %s: %w", call.Name, err)
				}
			}
			if len(raw) > 0 {
				call.Payload = raw
				b.calls[i].Payload = raw
			}

			// Agent-as-tool: start child workflows concurrently and fan in results later.
			if spec.IsAgentTool {
				messages, nestedRunCtx, err := e.r.buildAgentChildRequest(ctx, ts.AgentTool, &call)
				if err != nil {
					// Payload decoding / prompt rendering failures are tool-call contract
					// violations (wrong JSON shape, missing required fields, etc.).
					// Convert these into a tool result with a structured RetryHint rather
					// than failing the workflow.
					toolErr := planner.NewToolError(err.Error())
					tr := &planner.ToolResult{
						Name:       call.Name,
						ToolCallID: call.ToolCallID,
						Error:      toolErr,
					}
					var hint *planner.RetryHint
					if _, question, _, ok := buildRetryHintFromValidation(err, call.Name); ok {
						// Keep the retry flow fully model-driven (no await_clarification),
						// but retain the validation-derived question and field anchors.
						hint = &planner.RetryHint{
							Reason:             planner.RetryReasonInvalidArguments,
							Tool:               call.Name,
							RestrictToTool:     true,
							MissingFields:      nil,
							ClarifyingQuestion: question,
						}
					} else {
						var specPtr *tools.ToolSpec
						if s, ok := e.r.toolSpec(call.Name); ok {
							cp := s
							specPtr = &cp
						}
						if h := buildRetryHintFromDecodeError(err, call.Name, specPtr); h != nil {
							h.Reason = planner.RetryReasonInvalidArguments
							h.MissingFields = nil
							h.RestrictToTool = true
							hint = h
						}
					}
					tr.RetryHint = hint
					if err := e.r.enforceToolResultContracts(spec, call, toolErr, tr); err != nil {
						return nil, err
					}
					if err := e.publishToolResultReceived(ctx, call, tr, nil, 0); err != nil {
						return nil, err
					}
					b.inlineByID[call.ToolCallID] = tr
					if e.parentTracker != nil {
						b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
					}
					continue
				}
				if err := e.r.publishHook(wfCtx.Context(), hooks.NewChildRunLinkedEvent(call.RunID, call.AgentID, call.SessionID, call.Name, call.ToolCallID, nestedRunCtx.RunID, ts.AgentTool.AgentID), ""); err != nil {
					return nil, err
				}
				route := ts.AgentTool.Route
				if route.ID == "" || route.WorkflowName == "" || route.DefaultTaskQueue == "" {
					return nil, fmt.Errorf("agent tool route is incomplete for %s", call.Name)
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
					return nil, fmt.Errorf("failed to start agent child workflow for %s: %w", call.Name, err)
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
			ctxInline = withFinalizerInvokerFactory(ctxInline, &finalizerInvokerFactory{
				runtime:         e.r,
				wfCtx:           wfCtx,
				activityName:    e.activityName,
				activityOptions: e.toolActOptions,
				agentID:         e.agentID,
			})
			result, err := ts.Execute(ctxInline, &call)
			if err != nil {
				return nil, fmt.Errorf("inline tool %q failed: %w", call.Name, err)
			}
			if result == nil {
				return nil, fmt.Errorf("inline tool %q returned nil result", call.Name)
			}
			duration := wfCtx.Now().Sub(start)
			var toolErr *planner.ToolError
			if result.Error != nil {
				toolErr = result.Error
			}
			if err := e.r.enforceToolResultContracts(spec, call, toolErr, result); err != nil {
				return nil, err
			}
			var resultJSON json.RawMessage
			if toolErr == nil {
				resultJSON, err = e.r.marshalToolValue(ctx, call.Name, result.Result, false)
				if err != nil {
					return nil, fmt.Errorf("encode %s tool result for streaming: %w", call.Name, err)
				}
			}
			if err := e.publishToolResultReceived(ctx, call, result, resultJSON, duration); err != nil {
				return nil, err
			}
			b.inlineByID[call.ToolCallID] = result
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
			TurnID:           call.TurnID,
			ParentToolCallID: call.ParentToolCallID,
		}
		callOpts := computeToolActivityOptions(wfCtx, e.toolActOptions, e.finishBy)
		if callOpts.Queue == "" && hasTS && !ts.Inline && ts.TaskQueue != "" {
			callOpts.Queue = ts.TaskQueue
		}
		future, err := wfCtx.ExecuteToolActivityAsync(ctx, engine.ToolActivityCall{
			Name:    e.activityName,
			Input:   &toolInput,
			Options: callOpts,
		})
		if err != nil {
			return nil, fmt.Errorf("failed to schedule tool %q: %w", call.Name, err)
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

	return b, nil
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

func (e *toolBatchExec) collectActivityResultsAsComplete(wfCtx engine.WorkflowContext, futures []futureInfo, finalizeTimer engine.Future[time.Time]) (map[string]*planner.ToolResult, []futureInfo, bool, error) {
	ctx := wfCtx.Context()
	activityByID := make(map[string]*planner.ToolResult, len(futures))
	pending := append([]futureInfo(nil), futures...)
	for len(pending) > 0 {
		if err := wfCtx.Await(ctx, func() bool {
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
			return nil, nil, false, err
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
				toolRes, synthErr := e.synthesizeToolError(ctx, info.call, err, "tool activity failed", duration)
				if synthErr != nil {
					return nil, nil, false, synthErr
				}
				activityByID[info.call.ToolCallID] = toolRes
				continue
			}
			if out == nil {
				return nil, nil, false, fmt.Errorf("tool %q returned nil output", info.call.Name)
			}

			duration := wfCtx.Now().Sub(info.startTime)
			spec, ok := e.r.toolSpec(info.call.Name)
			if !ok {
				tr, synthErr := e.synthesizeUnknownToolResult(ctx, info.call, duration)
				if synthErr != nil {
					return nil, nil, false, synthErr
				}
				activityByID[info.call.ToolCallID] = tr
				continue
			}

			var decoded any
			if out.Error == "" && hasNonNullJSON(out.Payload) {
				v, decErr := e.r.unmarshalToolValue(ctx, info.call.Name, out.Payload, false)
				if decErr != nil {
					return nil, nil, false, fmt.Errorf("tool %q result decode failed (tool_call_id=%s): %w", info.call.Name, info.call.ToolCallID, decErr)
				}
				decoded = v
			}

			toolRes := &planner.ToolResult{
				Name:       info.call.Name,
				Result:     decoded,
				ServerData: out.ServerData,
				ToolCallID: info.call.ToolCallID,
				Telemetry:  out.Telemetry,
			}
			var toolErr *planner.ToolError
			if out.Error != "" {
				toolErr = planner.NewToolError(out.Error)
				toolRes.Error = toolErr
			}
			if err := e.r.enforceToolResultContracts(spec, info.call, toolErr, toolRes); err != nil {
				return nil, nil, false, err
			}
			if out.RetryHint != nil {
				h := *out.RetryHint
				if len(h.ExampleInput) == 0 && len(spec.Payload.ExampleInput) > 0 {
					h.ExampleInput = maps.Clone(spec.Payload.ExampleInput)
				}
				if len(h.PriorInput) == 0 && len(info.call.Payload) > 0 {
					var prior map[string]any
					if err := json.Unmarshal(info.call.Payload, &prior); err == nil && len(prior) > 0 {
						h.PriorInput = prior
					}
				}
				toolRes.RetryHint = &h
			}

			var resultJSON json.RawMessage
			if toolErr == nil {
				resultJSON, err = e.r.marshalToolValue(ctx, info.call.Name, decoded, false)
				if err != nil {
					return nil, nil, false, fmt.Errorf("encode %s tool result for streaming: %w", info.call.Name, err)
				}
			}
			if err := e.publishToolResultReceived(ctx, info.call, toolRes, resultJSON, duration); err != nil {
				return nil, nil, false, err
			}

			activityByID[info.call.ToolCallID] = toolRes
		}
		if finalizeTimer != nil && finalizeTimer.IsReady() && len(pending) > 0 {
			return activityByID, pending, true, nil
		}
	}
	return activityByID, nil, false, nil
}

func (e *toolBatchExec) collectAgentChildResults(wfCtx engine.WorkflowContext, children []agentChildFutureInfo, finalizeTimer engine.Future[time.Time]) (map[string]*planner.ToolResult, []agentChildFutureInfo, bool, error) {
	ctx := wfCtx.Context()
	if len(children) == 0 {
		return map[string]*planner.ToolResult{}, nil, false, nil
	}

	ctxWithInvoker := withFinalizerInvokerFactory(ctx, &finalizerInvokerFactory{
		runtime:         e.r,
		wfCtx:           wfCtx,
		activityName:    e.activityName,
		activityOptions: e.toolActOptions,
		agentID:         e.agentID,
	})
	out := make(map[string]*planner.ToolResult, len(children))
	pending := append([]agentChildFutureInfo(nil), children...)
	for len(pending) > 0 {
		if err := wfCtx.Await(ctx, func() bool {
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
			return nil, nil, false, err
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
				toolRes, synthErr := e.synthesizeToolError(ctx, info.call, err, "agent tool execution failed", duration)
				if synthErr != nil {
					return nil, nil, false, synthErr
				}
				out[info.call.ToolCallID] = toolRes
				continue
			}
			tr, err := e.r.adaptAgentChildOutput(ctxWithInvoker, info.cfg, &info.call, info.nestedRun, outPtr)
			if err != nil {
				return nil, nil, false, err
			}

			duration := wfCtx.Now().Sub(info.startTime)
			var toolErr *planner.ToolError
			if tr.Error != nil {
				toolErr = tr.Error
			}
			spec, ok := e.r.toolSpec(info.call.Name)
			if !ok {
				tr, synthErr := e.synthesizeUnknownToolResult(ctx, info.call, duration)
				if synthErr != nil {
					return nil, nil, false, synthErr
				}
				out[info.call.ToolCallID] = tr
				continue
			}
			if err := e.r.enforceToolResultContracts(spec, info.call, toolErr, tr); err != nil {
				return nil, nil, false, err
			}

			var resultJSON json.RawMessage
			if toolErr == nil {
				resultJSON, err = e.r.marshalToolValue(ctx, info.call.Name, tr.Result, false)
				if err != nil {
					return nil, nil, false, fmt.Errorf("encode %s tool result for streaming: %w", info.call.Name, err)
				}
			}
			if err := e.publishToolResultReceived(ctx, info.call, tr, resultJSON, duration); err != nil {
				return nil, nil, false, err
			}
			out[info.call.ToolCallID] = tr
		}
		if finalizeTimer != nil && finalizeTimer.IsReady() && len(pending) > 0 {
			return out, pending, true, nil
		}
	}
	return out, nil, false, nil
}

func mergeToolResultsInCallOrder(calls []planner.ToolRequest, activityByID, inlineByID map[string]*planner.ToolResult) ([]*planner.ToolResult, error) {
	results := make([]*planner.ToolResult, 0, len(calls))
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
func (r *Runtime) executeToolCalls(wfCtx engine.WorkflowContext, activityName string, toolActOptions engine.ActivityOptions, agentID agent.Ident, runCtx *run.Context, calls []planner.ToolRequest, expectedChildren int, parentTracker *childTracker, finishBy time.Time) ([]*planner.ToolResult, bool, error) {
	if runCtx == nil {
		return nil, false, fmt.Errorf("missing run context")
	}
	exec := &toolBatchExec{
		r:                      r,
		activityName:           activityName,
		toolActOptions:         toolActOptions,
		runID:                  runCtx.RunID,
		agentID:                agentID,
		sessionID:              runCtx.SessionID,
		turnID:                 runCtx.TurnID,
		runCtx:                 runCtx,
		expectedChildren:       expectedChildren,
		parentTracker:          parentTracker,
		finishBy:               finishBy,
	}

	ctx := wfCtx.Context()
	if !finishBy.IsZero() && !wfCtx.Now().Before(finishBy) {
		const cancelMsg = "canceled: time budget reached"
		results := make([]*planner.ToolResult, 0, len(calls))
		for i, call := range calls {
			call = exec.normalizeToolCall(call, i)
			queue := ""
			spec, ok := r.toolSpec(call.Name)
			if ok {
				r.mu.RLock()
				ts, hasTS := r.toolsets[spec.Toolset]
				r.mu.RUnlock()
				if hasTS && ts.TaskQueue != "" {
					queue = ts.TaskQueue
				}
			}
			if err := exec.publishToolCallScheduled(ctx, call, queue); err != nil {
				return nil, false, err
			}

			toolErr := planner.NewToolError(cancelMsg)
			tr := &planner.ToolResult{
				Name:       call.Name,
				ToolCallID: call.ToolCallID,
				Error:      toolErr,
			}
			if ok {
				if err := r.enforceToolResultContracts(spec, call, toolErr, tr); err != nil {
					return nil, false, err
				}
			}
			if err := exec.publishToolResultReceived(ctx, call, tr, nil, 0); err != nil {
				return nil, false, err
			}
			results = append(results, tr)
		}
		return results, true, nil
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

	var finalizeTimer engine.Future[time.Time]
	if !finishBy.IsZero() {
		d := finishBy.Sub(wfCtx.Now())
		t, err := wfCtx.NewTimer(ctx, d)
		if err != nil {
			return nil, false, err
		}
		finalizeTimer = t
	}

	batch, err := exec.dispatchToolCalls(execWfCtx, calls)
	if err != nil {
		cancelExecOnce()
		return nil, false, err
	}
	if err := exec.maybePublishChildTrackerUpdate(ctx, batch.discoveredIDs); err != nil {
		cancelExecOnce()
		return nil, false, err
	}

	activityByID, pendingActs, timedOutActs, err := exec.collectActivityResultsAsComplete(wfCtx, batch.futures, finalizeTimer)
	if err != nil {
		cancelExecOnce()
		return nil, false, err
	}

	childByID, pendingChildren, timedOutChildren, err := exec.collectAgentChildResults(wfCtx, batch.childFutures, finalizeTimer)
	if err != nil {
		cancelExecOnce()
		return nil, false, err
	}

	timedOut := timedOutActs || timedOutChildren
	if timedOut {
		cancelExecOnce()
	}

	for id, tr := range childByID {
		batch.inlineByID[id] = tr
	}

	if timedOut {
		const cancelMsg = "canceled: time budget reached"
		for _, info := range pendingChildren {
			if info.handle != nil {
				if err := info.handle.Cancel(ctx); err != nil {
					return nil, false, err
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
			toolErr := planner.NewToolError(cancelMsg)
			tr := &planner.ToolResult{
				Name:       info.call.Name,
				ToolCallID: info.call.ToolCallID,
				Error:      toolErr,
			}
			spec, ok := r.toolSpec(info.call.Name)
			if ok {
				if err := r.enforceToolResultContracts(spec, info.call, toolErr, tr); err != nil {
					return nil, false, err
				}
			}
			duration := wfCtx.Now().Sub(info.startTime)
			if err := exec.publishToolResultReceived(ctx, info.call, tr, nil, duration); err != nil {
				return nil, false, err
			}
			activityByID[info.call.ToolCallID] = tr
		}

		for _, info := range pendingChildren {
			if info.call.ToolCallID == "" {
				continue
			}
			if _, ok := batch.inlineByID[info.call.ToolCallID]; ok {
				continue
			}
			toolErr := planner.NewToolError(cancelMsg)
			tr := &planner.ToolResult{
				Name:       info.call.Name,
				ToolCallID: info.call.ToolCallID,
				Error:      toolErr,
			}
			spec, ok := r.toolSpec(info.call.Name)
			if ok {
				if err := r.enforceToolResultContracts(spec, info.call, toolErr, tr); err != nil {
					return nil, false, err
				}
			}
			duration := wfCtx.Now().Sub(info.startTime)
			if err := exec.publishToolResultReceived(ctx, info.call, tr, nil, duration); err != nil {
				return nil, false, err
			}
			batch.inlineByID[info.call.ToolCallID] = tr
		}
	}

	merged, err := mergeToolResultsInCallOrder(batch.calls, activityByID, batch.inlineByID)
	if err != nil {
		return nil, false, err
	}
	return merged, timedOut, nil
}
