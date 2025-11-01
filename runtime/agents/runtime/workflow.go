package runtime

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"goa.design/goa-ai/runtime/agents/engine"
	"goa.design/goa-ai/runtime/agents/hooks"
	"goa.design/goa-ai/runtime/agents/interrupt"
	"goa.design/goa-ai/runtime/agents/memory"
	"goa.design/goa-ai/runtime/agents/planner"
	"goa.design/goa-ai/runtime/agents/policy"
	"goa.design/goa-ai/runtime/agents/run"
)

type (
	// futureInfo bundles a Future with its associated tool call metadata for parallel execution.
	// When tools are launched asynchronously via ExecuteActivityAsync, we need to track the
	// future handle alongside the original call details and start time so we can correlate
	// results and measure duration when collecting completed activities.
	futureInfo struct {
		// future is the engine Future returned by ExecuteActivityAsync for this tool call.
		future engine.Future
		// call is the original tool request that was submitted for execution.
		call planner.ToolRequest
		// startTime records when the activity was scheduled, used to calculate tool duration.
		startTime time.Time
	}

	// turnSequencer tracks the turn ID and monotonic sequence counter for event ordering.
	// Each run maintains its own sequencer to ensure deterministic event ordering within
	// a conversational turn.
	turnSequencer struct {
		turnID   string
		sequence int
	}

	// childTracker tracks dynamically discovered child tool calls for a parent tool
	// (agent-as-tool pattern). As the planner discovers new child tools across iterations,
	// the tracker maintains the unique set of discovered IDs and triggers update events
	// when the count increases. This enables UI progress tracking ("3 of 5 complete").
	//
	// NOTE: This infrastructure is currently unused and reserved for future implementation
	// of nested agent-as-tool workflows where tools run their own internal planning loops
	// and dynamically discover child tools across multiple iterations. The struct and
	// methods are defined now to support future codegen and maintain API stability.
	childTracker struct {
		// parentToolCallID identifies the parent tool (usually an agent-as-tool invocation).
		parentToolCallID string
		// discovered maps tool call IDs to struct{} for efficient membership checking.
		// The map size is the current expected children total.
		discovered map[string]struct{}
		// lastExpectedTotal is the count last reported via ToolCallUpdatedEvent.
		// We only emit update events when len(discovered) > lastExpectedTotal.
		lastExpectedTotal int
	}
)

// ExecuteWorkflow is the main entry point for the generated workflow handler.
// It executes the agent's plan/tool loop using the configured planner, policy,
// and runtime hooks. Returns the final agent output or an error if the workflow
// fails. Generated code calls this from the workflow handler registered with
// the engine.
func (r *Runtime) ExecuteWorkflow(wfCtx engine.WorkflowContext, input RunInput) (*RunOutput, error) {
	if input.AgentID == "" {
		return nil, errors.New("agent id is required")
	}
	defer r.storeWorkflowHandle(input.RunID, nil)
	reg, ok := r.Agent(input.AgentID)
	if !ok {
		return nil, fmt.Errorf("agent %q is not registered", input.AgentID)
	}
	ctrl := interrupt.NewController(wfCtx)
	reader := r.memoryReader(wfCtx.Context(), input.AgentID, input.RunID)
	agentCtx := newAgentContext(agentContextOptions{
		runtime: r,
		agentID: input.AgentID,
		runID:   input.RunID,
		memory:  reader,
		turnID:  input.TurnID,
	})
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
		Labels:    input.Labels,
	}
	// Initialize turn sequencer for event ordering if TurnID is provided
	var seq *turnSequencer
	if input.TurnID != "" {
		seq = &turnSequencer{turnID: input.TurnID}
	}
	r.publishHook(wfCtx.Context(), hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input), seq)
	r.recordRunStatus(wfCtx.Context(), &input, run.StatusRunning, nil)
	defer r.publishHook(wfCtx.Context(), hooks.NewRunCompletedEvent(input.RunID, input.AgentID, "success", nil), seq)

	planInput := planner.PlanInput{
		Messages:   input.Messages,
		RunContext: runCtx,
		Agent:      agentCtx,
	}
	startReq := PlanActivityInput{
		AgentID:    input.AgentID,
		RunID:      input.RunID,
		Messages:   planInput.Messages,
		RunContext: planInput.RunContext,
	}
	result, err := r.runPlanActivity(wfCtx, reg.PlanActivityName, reg.PlanActivityOptions, startReq)
	if err != nil {
		r.recordRunStatus(wfCtx.Context(), &input, run.StatusFailed, map[string]any{"error": err.Error()})
		return nil, err
	}
	caps := initialCaps(reg.Policy)
	var deadline time.Time
	if reg.Policy.TimeBudget > 0 {
		deadline = wfCtx.Now().Add(reg.Policy.TimeBudget)
	}
	nextAttempt := planInput.RunContext.Attempt + 1
	out, err := r.runLoop(wfCtx, reg, &input, planInput, result, caps, deadline, nextAttempt, seq, nil, ctrl)
	if err != nil {
		r.recordRunStatus(wfCtx.Context(), &input, run.StatusFailed, map[string]any{"error": err.Error()})
		return nil, err
	}
	r.recordRunStatus(wfCtx.Context(), &input, run.StatusCompleted, nil)
	return out, nil
}

// runLoop executes the plan/tool/resume cycle until the planner returns a final response
// or a cap/deadline is exceeded. The seq parameter enables turn-based event sequencing.
func (r *Runtime) runLoop(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base planner.PlanInput,
	initial planner.PlanResult,
	caps policy.CapsState,
	deadline time.Time,
	nextAttempt int,
	seq *turnSequencer,
	parentTracker *childTracker,
	ctrl *interrupt.Controller,
) (*RunOutput, error) {
	ctx := wfCtx.Context()
	result := initial
	var lastToolResults []planner.ToolResult
	for {
		if err := r.handleInterrupts(wfCtx, input, &base, seq, ctrl, &nextAttempt); err != nil {
			return nil, err
		}
		if !deadline.IsZero() && wfCtx.Now().After(deadline) {
			return nil, errors.New("time budget exceeded")
		}
		if len(result.ToolCalls) == 0 {
			if result.FinalResponse == nil {
				return nil, errors.New("planner returned neither tool calls nor final response")
			}
			r.publishHook(
				ctx,
				hooks.NewAssistantMessageEvent(
					base.RunContext.RunID,
					base.Agent.ID(),
					result.FinalResponse.Message.Content,
					nil,
				),
				seq,
			)
			for _, note := range result.Notes {
				r.publishHook(
					ctx,
					hooks.NewPlannerNoteEvent(
						base.RunContext.RunID,
						base.Agent.ID(),
						note.Text,
						note.Labels,
					),
					seq,
				)
			}
			return &RunOutput{
				AgentID:    base.Agent.ID(),
				RunID:      base.RunContext.RunID,
				Final:      result.FinalResponse.Message,
				ToolEvents: lastToolResults,
				Notes:      result.Notes,
			}, nil
		}

		if caps.RemainingToolCalls == 0 && caps.MaxToolCalls > 0 {
			return nil, errors.New("tool call cap exceeded")
		}
		if !deadline.IsZero() && wfCtx.Now().After(deadline) {
			return nil, errors.New("time budget exceeded")
		}
		allowed := result.ToolCalls
		if r.Policy != nil {
			decision, err := r.Policy.Decide(ctx, policy.Input{
				RunContext:    base.RunContext,
				Tools:         r.toolMetadata(result.ToolCalls),
				RetryHint:     toPolicyRetryHint(result.RetryHint),
				RemainingCaps: caps,
				Requested:     toolHandles(result.ToolCalls),
				Labels:        base.RunContext.Labels,
			})
			if err != nil {
				return nil, err
			}
			if len(decision.Labels) > 0 {
				base.RunContext.Labels = mergeLabels(base.RunContext.Labels, decision.Labels)
				input.Labels = mergeLabels(input.Labels, decision.Labels)
			}
			if decision.DisableTools {
				return nil, errors.New("tool execution disabled by policy")
			}
			if len(decision.AllowedTools) > 0 {
				allowed = filterToolCalls(allowed, decision.AllowedTools)
			}
			caps = mergeCaps(caps, decision.Caps)
			r.recordPolicyDecision(ctx, input, decision)
			r.publishHook(ctx, hooks.NewPolicyDecisionEvent(
				base.RunContext.RunID,
				base.Agent.ID(),
				handlesToIDs(decision.AllowedTools),
				caps,
				cloneLabels(decision.Labels),
				cloneMetadata(decision.Metadata),
			), seq)
		}
		if len(allowed) == 0 {
			return nil, errors.New("no tools allowed for execution")
		}
		if parentTracker != nil {
			ids := collectToolCallIDs(allowed)
			if len(ids) > 0 && parentTracker.registerDiscovered(ids) {
				r.publishHook(
					ctx,
					hooks.NewToolCallUpdatedEvent(
						base.RunContext.RunID,
						base.Agent.ID(),
						parentTracker.parentToolCallID,
						parentTracker.currentTotal(),
					),
					seq,
				)
				parentTracker.markUpdated()
			}
		}
		if caps.MaxToolCalls > 0 && caps.RemainingToolCalls < len(allowed) {
			allowed = allowed[:caps.RemainingToolCalls]
		}
		for i := range allowed {
			if allowed[i].RunID == "" {
				allowed[i].RunID = base.RunContext.RunID
			}
			if allowed[i].SessionID == "" {
				allowed[i].SessionID = base.RunContext.SessionID
			}
			if allowed[i].TurnID == "" {
				allowed[i].TurnID = base.RunContext.TurnID
			}
			// Assign deterministic tool-call IDs and inherit parent when tracking children.
			if allowed[i].ToolCallID == "" {
				allowed[i].ToolCallID = generateDeterministicToolCallID(
					base.RunContext.RunID, base.RunContext.TurnID, allowed[i].Name, i,
				)
			}
			if parentTracker != nil && allowed[i].ParentToolCallID == "" {
				allowed[i].ParentToolCallID = parentTracker.parentToolCallID
			}
		}
		toolResults, err := r.executeToolCalls(
			wfCtx, reg.ExecuteToolActivity, base.RunContext.RunID, base.Agent.ID(),
			allowed, result.ExpectedChildren, seq, parentTracker,
		)
		if err != nil {
			return nil, err
		}
		lastToolResults = toolResults
		caps.RemainingToolCalls = decrementCap(caps.RemainingToolCalls, len(toolResults))
		if failures(toolResults) > 0 {
			caps.RemainingConsecutiveFailedToolCalls = decrementCap(
				caps.RemainingConsecutiveFailedToolCalls, failures(toolResults),
			)
			if caps.MaxConsecutiveFailedToolCalls > 0 && caps.RemainingConsecutiveFailedToolCalls <= 0 {
				return nil, errors.New("consecutive failed tool call cap exceeded")
			}
		} else if caps.MaxConsecutiveFailedToolCalls > 0 {
			caps.RemainingConsecutiveFailedToolCalls = caps.MaxConsecutiveFailedToolCalls
		}

		resumeCtx := base.RunContext
		resumeCtx.Attempt = nextAttempt
		nextAttempt++
		resumeReq := PlanActivityInput{
			AgentID:     base.Agent.ID(),
			RunID:       base.RunContext.RunID,
			Messages:    base.Messages,
			RunContext:  resumeCtx,
			ToolResults: toolResults,
		}
		result, err = r.runPlanActivity(wfCtx, reg.ResumeActivityName, reg.ResumeActivityOptions, resumeReq)
		if err != nil {
			return nil, err
		}
	}
}

func (r *Runtime) handleInterrupts(
	wfCtx engine.WorkflowContext,
	input *RunInput,
	base *planner.PlanInput,
	seq *turnSequencer,
	ctrl *interrupt.Controller,
	nextAttempt *int,
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
		r.recordRunStatus(ctx, input, run.StatusPaused, map[string]any{"reason": req.Reason})
		r.publishHook(
			ctx,
			hooks.NewRunPausedEvent(
				input.RunID,
				input.AgentID,
				req.Reason,
				req.RequestedBy,
				req.Labels,
				req.Metadata,
			),
			seq,
		)

		resumeReq, err := ctrl.WaitResume(ctx)
		if err != nil {
			return err
		}
		if len(resumeReq.Messages) > 0 {
			base.Messages = append(base.Messages, resumeReq.Messages...)
		}
		base.RunContext.Attempt = *nextAttempt
		*nextAttempt++
		r.recordRunStatus(ctx, input, run.StatusRunning, map[string]any{"resumed_by": resumeReq.RequestedBy})
		r.publishHook(
			ctx,
			hooks.NewRunResumedEvent(
				input.RunID,
				input.AgentID,
				resumeReq.Notes,
				resumeReq.RequestedBy,
				resumeReq.Labels,
				len(resumeReq.Messages),
			),
			seq,
		)
	}
	return nil
}

// executeToolCalls schedules tool activities in parallel and collects their results.
// Tools are launched asynchronously via ExecuteActivityAsync, then results are collected
// in order. This provides better performance for independent tool calls while maintaining
// deterministic result ordering. expectedChildren indicates how many child tools are expected
// to be discovered dynamically by the tools in this batch (0 if not tracked).
func collectToolCallIDs(calls []planner.ToolRequest) []string {
	ids := make([]string, 0, len(calls))
	for _, call := range calls {
		ids = append(ids, call.ToolCallID)
	}
	return ids
}

func (r *Runtime) executeToolCalls(
	wfCtx engine.WorkflowContext,
	activityName, runID, agentID string,
	calls []planner.ToolRequest,
	expectedChildren int,
	seq *turnSequencer,
	parentTracker *childTracker,
) ([]planner.ToolResult, error) {
	if activityName == "" {
		return nil, errors.New("execute tool activity not registered")
	}
	ctx := wfCtx.Context()

	// Launch all activities in parallel
	futures := make([]futureInfo, 0, len(calls))
	discoveredIDs := make([]string, 0, len(calls))
	for i, call := range calls {
		if call.ToolCallID == "" {
			call.ToolCallID = generateDeterministicToolCallID(runID, call.TurnID, call.Name, i)
			calls[i] = call
		}
		if parentTracker != nil && call.ParentToolCallID == "" {
			call.ParentToolCallID = parentTracker.parentToolCallID
			calls[i] = call
		}
		rawPayload, err := r.marshalToolValue(ctx, call.Name, call.Payload, true)
		if err != nil {
			return nil, err
		}

		toolsetName := toolsetIdentifier(call.Name)
		req := engine.ActivityRequest{
			Name: activityName,
			Input: ToolInput{
				AgentID:          agentID,
				RunID:            runID,
				ToolsetName:      toolsetName,
				ToolName:         call.Name,
				ToolCallID:       call.ToolCallID,
				Payload:          rawPayload,
				SessionID:        call.SessionID,
				TurnID:           call.TurnID,
				ParentToolCallID: call.ParentToolCallID,
			},
		}

		if ts, ok := r.toolsets[toolsetName]; ok && ts.TaskQueue != "" {
			req.Queue = ts.TaskQueue
		}

		r.publishHook(ctx,
			hooks.NewToolCallScheduledEvent(
				runID, agentID, call.Name, call.ToolCallID, call.Payload, req.Queue,
				call.ParentToolCallID, expectedChildren,
			),
			seq,
		)

		future, err := wfCtx.ExecuteActivityAsync(ctx, req)
		if err != nil {
			return nil, fmt.Errorf("failed to schedule tool %q: %w", call.Name, err)
		}

		futures = append(futures, futureInfo{
			future:    future,
			call:      call,
			startTime: wfCtx.Now(),
		})
		if parentTracker != nil {
			discoveredIDs = append(discoveredIDs, call.ToolCallID)
		}
	}

	if parentTracker != nil && parentTracker.registerDiscovered(discoveredIDs) && parentTracker.needsUpdate() {
		r.publishHook(
			ctx,
			hooks.NewToolCallUpdatedEvent(runID, agentID, parentTracker.parentToolCallID, parentTracker.currentTotal()),
			seq,
		)
		parentTracker.markUpdated()
	}

	// Collect all results in order
	results := make([]planner.ToolResult, 0, len(futures))
	for _, info := range futures {
		var out ToolOutput
		if err := info.future.Get(ctx, &out); err != nil {
			return nil, fmt.Errorf("tool %q failed: %w", info.call.Name, err)
		}

		duration := wfCtx.Now().Sub(info.startTime)
		decoded, err := r.unmarshalToolValue(ctx, info.call.Name, out.Payload, false)
		if err != nil {
			return nil, err
		}

		toolRes := planner.ToolResult{
			Name:       info.call.Name,
			Result:     decoded,
			ToolCallID: info.call.ToolCallID,
			Telemetry:  out.Telemetry,
		}
		var toolErr *planner.ToolError
		if out.Error != "" {
			toolErr = planner.NewToolError(out.Error)
			toolRes.Error = toolErr
		}
		if out.RetryHint != nil {
			toolRes.RetryHint = out.RetryHint
		}

		r.publishHook(
			ctx,
			hooks.NewToolResultReceivedEvent(
				runID,
				agentID,
				info.call.Name,
				info.call.ToolCallID,
				info.call.ParentToolCallID,
				decoded,
				duration,
				out.Telemetry,
				toolErr,
			),
			seq,
		)

		results = append(results, toolRes)
	}

	return results, nil
}

// runPlanActivity schedules a plan/resume activity with the configured options.
func (r *Runtime) runPlanActivity(
	wfCtx engine.WorkflowContext, activityName string, options engine.ActivityOptions, input PlanActivityInput,
) (planner.PlanResult, error) {
	if activityName == "" {
		return planner.PlanResult{}, errors.New("plan activity not registered")
	}
	var out PlanActivityOutput
	req := engine.ActivityRequest{Name: activityName, Input: input}
	if options.Queue != "" {
		req.Queue = options.Queue
	}
	if options.Timeout > 0 {
		req.Timeout = options.Timeout
	}
	if !isZeroRetryPolicy(options.RetryPolicy) {
		req.RetryPolicy = options.RetryPolicy
	}
	if err := wfCtx.ExecuteActivity(wfCtx.Context(), req, &out); err != nil {
		return planner.PlanResult{}, err
	}
	return out.Result, nil
}

// recordRunStatus upserts run metadata to the store if configured.
func (r *Runtime) recordRunStatus(ctx context.Context, input *RunInput, status run.Status, meta map[string]any) {
	if r.RunStore == nil {
		return
	}
	rec := run.Record{
		AgentID:   input.AgentID,
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Status:    status,
		StartedAt: time.Now(),
		UpdatedAt: time.Now(),
		Labels:    cloneLabels(input.Labels),
		Metadata:  meta,
	}
	if err := r.RunStore.Upsert(ctx, rec); err != nil {
		r.logWarn(ctx, "run record upsert failed", err)
	}
}

func (r *Runtime) recordPolicyDecision(ctx context.Context, input *RunInput, decision policy.Decision) {
	if r.RunStore == nil {
		return
	}
	rec, err := r.RunStore.Load(ctx, input.RunID)
	if err != nil {
		r.logWarn(ctx, "run record load failed", err, "run_id", input.RunID)
		return
	}
	now := time.Now()
	if rec.RunID == "" {
		rec.AgentID = input.AgentID
		rec.RunID = input.RunID
		rec.SessionID = input.SessionID
		rec.TurnID = input.TurnID
		rec.StartedAt = now
	}
	if rec.StartedAt.IsZero() {
		rec.StartedAt = now
	}
	rec.AgentID = input.AgentID
	rec.SessionID = input.SessionID
	rec.TurnID = input.TurnID
	rec.Status = run.StatusRunning
	rec.UpdatedAt = now
	rec.Labels = mergeLabels(rec.Labels, input.Labels)

	entry := map[string]any{
		"caps":      decision.Caps,
		"timestamp": now.UTC(),
	}
	if ids := handlesToIDs(decision.AllowedTools); len(ids) > 0 {
		entry["allowed_tools"] = ids
	}
	if len(decision.Labels) > 0 {
		entry["labels"] = cloneLabels(decision.Labels)
	}
	if len(decision.Metadata) > 0 {
		entry["metadata"] = cloneMetadata(decision.Metadata)
	}
	if decision.DisableTools {
		entry["disable_tools"] = true
	}

	meta := cloneMetadata(rec.Metadata)
	meta = appendPolicyDecisionMetadata(meta, entry)
	rec.Metadata = meta

	if err := r.RunStore.Upsert(ctx, rec); err != nil {
		r.logWarn(ctx, "policy decision upsert failed", err)
	}
}

// memoryReader loads the run snapshot from the memory store and wraps it in a Reader.
func (r *Runtime) memoryReader(ctx context.Context, agentID, runID string) memory.Reader {
	if r.Memory == nil {
		return emptyMemoryReader{}
	}
	snapshot, err := r.Memory.LoadRun(ctx, agentID, runID)
	if err != nil {
		return emptyMemoryReader{}
	}
	return newMemoryReader(snapshot.Events)
}

// generateRunID creates a unique run identifier by combining the agent ID and a UUID.
func generateRunID(agentID string) string {
	prefix := strings.ReplaceAll(agentID, ".", "-")
	return fmt.Sprintf("%s-%s", prefix, uuid.NewString())
}

// nextSeq increments and returns the next sequence number for this turn.
func (t *turnSequencer) nextSeq() int {
	seq := t.sequence
	t.sequence++
	return seq
}
