package runtime

import (
	"context"
	"fmt"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
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
)

type toolCallBatch struct {
	calls                 []planner.ToolRequest
	artifactsModeByCallID map[string]tools.ArtifactsMode

	futures      []futureInfo
	childFutures []agentChildFutureInfo
	inlineByID   map[string]*planner.ToolResult

	discoveredIDs []string
}

// collectToolCallIDs returns the tool call IDs in the same order as calls.
func collectToolCallIDs(calls []planner.ToolRequest) []string {
	ids := make([]string, 0, len(calls))
	for _, call := range calls {
		ids = append(ids, call.ToolCallID)
	}
	return ids
}

func normalizeToolCall(
	call planner.ToolRequest,
	i int,
	runID string,
	runCtx *run.Context,
	parentTracker *childTracker,
	artifactsModeByCallID map[string]tools.ArtifactsMode,
) (planner.ToolRequest, error) {
	if call.ToolCallID == "" {
		attempt := 0
		if runCtx != nil {
			attempt = runCtx.Attempt
		}
		call.ToolCallID = generateDeterministicToolCallID(runID, call.TurnID, attempt, call.Name, i)
	}
	if parentTracker != nil && call.ParentToolCallID == "" {
		call.ParentToolCallID = parentTracker.parentToolCallID
	}
	if call.ParentToolCallID == "" && runCtx != nil && runCtx.ParentToolCallID != "" {
		call.ParentToolCallID = runCtx.ParentToolCallID
	}

	mode := call.ArtifactsMode
	stripped := call.Payload
	if mode == "" {
		var err error
		mode, stripped, err = extractArtifactsMode(call.Payload)
		if err != nil {
			return planner.ToolRequest{}, err
		}
	}
	call.ArtifactsMode = mode
	if mode != "" {
		artifactsModeByCallID[call.ToolCallID] = mode
	}
	call.Payload = stripped
	return call, nil
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

func (r *Runtime) enforceToolResultContracts(
	spec tools.ToolSpec,
	call planner.ToolRequest,
	toolErr *planner.ToolError,
	tr *planner.ToolResult,
	artifactsModeByCallID map[string]tools.ArtifactsMode,
) error {
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
		return fmt.Errorf(
			"bounded tool %q returned result without bounds (tool_call_id=%s, type=%T)",
			call.Name,
			call.ToolCallID,
			tr.Result,
		)
	}
	enabled := !artifactsDisabled(artifactsModeByCallID[call.ToolCallID])
	if !enabled {
		tr.Artifacts = nil
	}
	if enabled {
		if err := requireArtifacts(spec, call.ToolCallID, toolErr == nil, tr.Artifacts); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) publishToolCallScheduled(
	ctx context.Context,
	runID string,
	agentID agent.Ident,
	sessionID string,
	call planner.ToolRequest,
	queue string,
	expectedChildren int,
	turnID string,
) {
	r.publishHook(
		ctx,
		hooks.NewToolCallScheduledEvent(
			runID,
			agentID,
			sessionID,
			call.Name,
			call.ToolCallID,
			call.Payload,
			queue,
			call.ParentToolCallID,
			expectedChildren,
		),
		turnID,
	)
}

func computeToolActivityOptions(wfCtx engine.WorkflowContext, base engine.ActivityOptions, hardDeadline time.Time) engine.ActivityOptions {
	callOpts := base
	timeout := base.Timeout
	if !hardDeadline.IsZero() {
		now := wfCtx.Now()
		if rem := hardDeadline.Sub(now); rem > 0 {
			if timeout == 0 || timeout > rem {
				timeout = rem
			}
		}
	}
	callOpts.Timeout = timeout
	return callOpts
}

func (r *Runtime) dispatchToolCalls(
	wfCtx engine.WorkflowContext,
	activityName string,
	toolActOptions engine.ActivityOptions,
	runID string,
	agentID agent.Ident,
	runCtx *run.Context,
	calls []planner.ToolRequest,
	expectedChildren int,
	turnID string,
	parentTracker *childTracker,
	hardDeadline time.Time,
) (*toolCallBatch, error) {
	ctx := wfCtx.Context()
	sessionID := ""
	if runCtx != nil {
		sessionID = runCtx.SessionID
	}

	b := &toolCallBatch{
		calls:                 calls,
		artifactsModeByCallID: make(map[string]tools.ArtifactsMode, len(calls)),
		futures:               make([]futureInfo, 0, len(calls)),
		childFutures:          make([]agentChildFutureInfo, 0, len(calls)),
		inlineByID:            make(map[string]*planner.ToolResult, len(calls)),
		discoveredIDs:         make([]string, 0, len(calls)),
	}

	for i, call := range calls {
		normalized, err := normalizeToolCall(call, i, runID, runCtx, parentTracker, b.artifactsModeByCallID)
		if err != nil {
			return nil, err
		}
		call = normalized
		b.calls[i] = call

		spec, hasSpec := r.toolSpec(call.Name)
		if !hasSpec {
			return nil, fmt.Errorf("unknown tool %q", call.Name)
		}
		r.mu.RLock()
		ts, hasTS := r.toolsets[spec.Toolset]
		r.mu.RUnlock()

		queue := ""
		if hasTS && ts.TaskQueue != "" {
			queue = ts.TaskQueue
		}
		r.publishToolCallScheduled(ctx, runID, agentID, sessionID, call, queue, expectedChildren, turnID)

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
					ArtifactsMode:    call.ArtifactsMode,
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
				messages, nestedRunCtx, err := r.buildAgentChildRequest(ctx, ts.AgentTool, &call)
				if err != nil {
					return nil, err
				}
				r.publishHook(
					wfCtx.Context(),
					hooks.NewAgentRunStartedEvent(
						call.RunID,
						call.AgentID,
						call.SessionID,
						call.Name,
						call.ToolCallID,
						nestedRunCtx.RunID,
						ts.AgentTool.AgentID,
					),
					"",
				)
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
				handle, err := wfCtx.StartChildWorkflow(
					wfCtx.Context(),
					engine.ChildWorkflowRequest{
						ID:        input.RunID,
						Workflow:  route.WorkflowName,
						TaskQueue: route.DefaultTaskQueue,
						Input:     &input,
					},
				)
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
				if parentTracker != nil {
					b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
				}
				continue
			}

			start := wfCtx.Now()
			ctxInline := engine.WithWorkflowContext(ctx, wfCtx)
			ctxInline = withFinalizerInvokerFactory(ctxInline, &finalizerInvokerFactory{
				runtime:         r,
				wfCtx:           wfCtx,
				activityName:    activityName,
				activityOptions: toolActOptions,
				agentID:         agentID,
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
			if err := r.enforceToolResultContracts(spec, call, toolErr, result, b.artifactsModeByCallID); err != nil {
				return nil, err
			}
			r.publishHook(
				ctx,
				hooks.NewToolResultReceivedEvent(
					runID,
					agentID,
					sessionID,
					call.Name,
					call.ToolCallID,
					call.ParentToolCallID,
					result.Result,
					formatResultPreview(call.Name, result.Result),
					result.Bounds,
					result.Artifacts,
					duration,
					result.Telemetry,
					toolErr,
				),
				turnID,
			)
			b.inlineByID[call.ToolCallID] = result
			if parentTracker != nil {
				b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
			}
			continue
		}

		// Activity path (service-backed tools).
		toolInput := ToolInput{
			AgentID:          agentID,
			RunID:            runID,
			ToolsetName:      spec.Toolset,
			ToolName:         call.Name,
			ToolCallID:       call.ToolCallID,
			Payload:          call.Payload,
			ArtifactsMode:    call.ArtifactsMode,
			SessionID:        call.SessionID,
			TurnID:           call.TurnID,
			ParentToolCallID: call.ParentToolCallID,
		}
		callOpts := computeToolActivityOptions(wfCtx, toolActOptions, hardDeadline)
		if callOpts.Queue == "" && hasTS && !ts.Inline && ts.TaskQueue != "" {
			callOpts.Queue = ts.TaskQueue
		}
		future, err := wfCtx.ExecuteToolActivityAsync(ctx, engine.ToolActivityCall{
			Name:    activityName,
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
		if parentTracker != nil {
			b.discoveredIDs = append(b.discoveredIDs, call.ToolCallID)
		}
	}

	return b, nil
}

func (r *Runtime) maybePublishChildTrackerUpdate(ctx context.Context, runCtx *run.Context, sessionID string, parentTracker *childTracker, discoveredIDs []string, turnID string) error {
	if parentTracker == nil || !parentTracker.registerDiscovered(discoveredIDs) || !parentTracker.needsUpdate() {
		return nil
	}
	if runCtx == nil || runCtx.ParentRunID == "" || runCtx.ParentAgentID == "" {
		return fmt.Errorf("nested tool tracker requires parent run context")
	}
	r.publishHook(
		ctx,
		hooks.NewToolCallUpdatedEvent(
			runCtx.ParentRunID,
			runCtx.ParentAgentID,
			sessionID,
			parentTracker.parentToolCallID,
			parentTracker.currentTotal(),
		),
		turnID,
	)
	parentTracker.markUpdated()
	return nil
}

func (r *Runtime) collectActivityResultsAsComplete(
	wfCtx engine.WorkflowContext,
	runID string,
	agentID agent.Ident,
	sessionID string,
	runCtx *run.Context,
	futures []futureInfo,
	artifactsModeByCallID map[string]tools.ArtifactsMode,
	turnID string,
) (map[string]*planner.ToolResult, error) {
	ctx := wfCtx.Context()
	activityByID := make(map[string]*planner.ToolResult, len(futures))
	pending := append([]futureInfo(nil), futures...)
	for len(pending) > 0 {
		if err := wfCtx.Await(ctx, func() bool {
			for _, info := range pending {
				if info.future.IsReady() {
					return true
				}
			}
			return false
		}); err != nil {
			return nil, err
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
				return nil, fmt.Errorf("tool %q failed: %w", info.call.Name, err)
			}
			if out == nil {
				return nil, fmt.Errorf("tool %q returned nil output", info.call.Name)
			}

			duration := wfCtx.Now().Sub(info.startTime)
			var decoded any
			if out.Error == "" && hasNonNullJSON(out.Payload) {
				v, decErr := r.unmarshalToolValue(ctx, info.call.Name, out.Payload, false)
				if decErr != nil {
					return nil, fmt.Errorf("tool %q result decode failed (tool_call_id=%s): %w", info.call.Name, info.call.ToolCallID, decErr)
				}
				decoded = v
			}

			toolRes := &planner.ToolResult{
				Name:       info.call.Name,
				Result:     decoded,
				Artifacts:  out.Artifacts,
				ToolCallID: info.call.ToolCallID,
				Telemetry:  out.Telemetry,
			}
			spec, ok := r.toolSpec(info.call.Name)
			if !ok {
				return nil, fmt.Errorf("unknown tool %q", info.call.Name)
			}
			var toolErr *planner.ToolError
			if out.Error != "" {
				toolErr = planner.NewToolError(out.Error)
				toolRes.Error = toolErr
			}
			if err := r.enforceToolResultContracts(spec, info.call, toolErr, toolRes, artifactsModeByCallID); err != nil {
				return nil, err
			}
			if out.RetryHint != nil {
				toolRes.RetryHint = out.RetryHint
			}

			parentID := parentToolCallID(info.call, runCtx)
			r.publishHook(
				ctx,
				hooks.NewToolResultReceivedEvent(
					runID,
					agentID,
					sessionID,
					info.call.Name,
					info.call.ToolCallID,
					parentID,
					decoded,
					formatResultPreview(info.call.Name, decoded),
					toolRes.Bounds,
					out.Artifacts,
					duration,
					out.Telemetry,
					toolErr,
				),
				turnID,
			)

			activityByID[info.call.ToolCallID] = toolRes
		}
	}
	return activityByID, nil
}

func (r *Runtime) collectAgentChildResults(
	wfCtx engine.WorkflowContext,
	activityName string,
	toolActOptions engine.ActivityOptions,
	runID string,
	agentID agent.Ident,
	sessionID string,
	runCtx *run.Context,
	children []agentChildFutureInfo,
	artifactsModeByCallID map[string]tools.ArtifactsMode,
	turnID string,
) (map[string]*planner.ToolResult, error) {
	ctx := wfCtx.Context()
	if len(children) == 0 {
		return map[string]*planner.ToolResult{}, nil
	}

	ctxWithInvoker := withFinalizerInvokerFactory(ctx, &finalizerInvokerFactory{
		runtime:         r,
		wfCtx:           wfCtx,
		activityName:    activityName,
		activityOptions: toolActOptions,
		agentID:         agentID,
	})
	out := make(map[string]*planner.ToolResult, len(children))
	for _, info := range children {
		outPtr, err := info.handle.Get(wfCtx.Context())
		if err != nil {
			return nil, fmt.Errorf("agent tool %q failed: %w", info.call.Name, err)
		}
		tr, err := r.adaptAgentChildOutput(ctxWithInvoker, info.cfg, &info.call, info.nestedRun, outPtr)
		if err != nil {
			return nil, err
		}

		duration := wfCtx.Now().Sub(info.startTime)
		var toolErr *planner.ToolError
		if tr.Error != nil {
			toolErr = tr.Error
		}
		spec, ok := r.toolSpec(info.call.Name)
		if !ok {
			return nil, fmt.Errorf("unknown tool %q", info.call.Name)
		}
		if err := r.enforceToolResultContracts(spec, info.call, toolErr, tr, artifactsModeByCallID); err != nil {
			return nil, err
		}

		parentID := parentToolCallID(info.call, runCtx)
		r.publishHook(
			ctx,
			hooks.NewToolResultReceivedEvent(
				runID,
				agentID,
				sessionID,
				info.call.Name,
				info.call.ToolCallID,
				parentID,
				tr.Result,
				formatResultPreview(info.call.Name, tr.Result),
				tr.Bounds,
				tr.Artifacts,
				duration,
				tr.Telemetry,
				toolErr,
			),
			turnID,
		)
		out[info.call.ToolCallID] = tr
	}
	return out, nil
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
func (r *Runtime) executeToolCalls(
	wfCtx engine.WorkflowContext,
	activityName string, toolActOptions engine.ActivityOptions, runID string, agentID agent.Ident,
	runCtx *run.Context,
	calls []planner.ToolRequest,
	expectedChildren int,
	turnID string,
	parentTracker *childTracker,
	hardDeadline time.Time,
) ([]*planner.ToolResult, error) {
	ctx := wfCtx.Context()
	sessionID := ""
	if runCtx != nil {
		sessionID = runCtx.SessionID
	}

	batch, err := r.dispatchToolCalls(
		wfCtx,
		activityName,
		toolActOptions,
		runID,
		agentID,
		runCtx,
		calls,
		expectedChildren,
		turnID,
		parentTracker,
		hardDeadline,
	)
	if err != nil {
		return nil, err
	}

	if err := r.maybePublishChildTrackerUpdate(ctx, runCtx, sessionID, parentTracker, batch.discoveredIDs, turnID); err != nil {
		return nil, err
	}

	activityByID, err := r.collectActivityResultsAsComplete(
		wfCtx,
		runID,
		agentID,
		sessionID,
		runCtx,
		batch.futures,
		batch.artifactsModeByCallID,
		turnID,
	)
	if err != nil {
		return nil, err
	}

	childByID, err := r.collectAgentChildResults(
		wfCtx,
		activityName,
		toolActOptions,
		runID,
		agentID,
		sessionID,
		runCtx,
		batch.childFutures,
		batch.artifactsModeByCallID,
		turnID,
	)
	if err != nil {
		return nil, err
	}
	for id, tr := range childByID {
		batch.inlineByID[id] = tr
	}

	return mergeToolResultsInCallOrder(batch.calls, activityByID, batch.inlineByID)
}
