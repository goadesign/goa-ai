package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// groupToolCallsByTimeout buckets calls by per-tool timeout override (with `*`
// suffix prefix-match support) or falls back to the default timeout when no
// override applies.
//
// The bucketing is deterministic for workflow replay:
//   - Exact tool-name matches take precedence over prefix matches.
//   - Among prefix matches, the longest prefix wins.
//   - Group ordering follows first appearance in the allowed slice.
func (r *Runtime) groupToolCallsByTimeout(allowed []planner.ToolRequest, input *RunInput, defaultTimeout time.Duration) ([][]planner.ToolRequest, []time.Duration) {
	var grouped [][]planner.ToolRequest
	var timeouts []time.Duration
	if input != nil && input.Policy != nil && len(input.Policy.PerToolTimeout) > 0 {
		type timeoutRule struct {
			prefix  string
			timeout time.Duration
		}
		exact := make(map[string]time.Duration, len(input.Policy.PerToolTimeout))
		prefixes := make([]timeoutRule, 0, len(input.Policy.PerToolTimeout))
		for k, v := range input.Policy.PerToolTimeout {
			kn := string(k)
			if strings.HasSuffix(kn, "*") {
				prefixes = append(prefixes, timeoutRule{
					prefix:  strings.TrimSuffix(kn, "*"),
					timeout: v,
				})
				continue
			}
			exact[kn] = v
		}
		sort.Slice(prefixes, func(i, j int) bool {
			if len(prefixes[i].prefix) != len(prefixes[j].prefix) {
				return len(prefixes[i].prefix) > len(prefixes[j].prefix)
			}
			return prefixes[i].prefix < prefixes[j].prefix
		})

		resolve := func(name tools.Ident) (time.Duration, bool) {
			n := string(name)
			if to, ok := exact[n]; ok {
				return to, true
			}
			for _, r := range prefixes {
				if strings.HasPrefix(n, r.prefix) {
					return r.timeout, true
				}
			}
			return 0, false
		}

		groupIndexByTimeout := make(map[time.Duration]int)
		for _, call := range allowed {
			to := defaultTimeout
			if override, ok := resolve(call.Name); ok && override > 0 {
				to = override
			}
			i, ok := groupIndexByTimeout[to]
			if !ok {
				i = len(grouped)
				groupIndexByTimeout[to] = i
				grouped = append(grouped, nil)
				timeouts = append(timeouts, to)
			}
			grouped[i] = append(grouped[i], call)
		}
	} else {
		grouped = [][]planner.ToolRequest{allowed}
		timeouts = []time.Duration{defaultTimeout}
	}
	return grouped, timeouts
}

// executeGroupedToolCalls runs groups of tool calls with their respective
// timeouts and returns all results in the original group order.
func (r *Runtime) executeGroupedToolCalls(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	agentID agent.Ident,
	base *planner.PlanInput,
	expectedChildren int,
	parentTracker *childTracker,
	finishBy time.Time,
	grouped [][]planner.ToolRequest,
	timeouts []time.Duration,
	toolOpts engine.ActivityOptions,
) ([]*ToolExecutionResult, bool, error) {
	var out []*ToolExecutionResult
	timedOutAny := false
	for i := range grouped {
		opt := toolOpts
		if timeouts[i] > 0 {
			opt.StartToCloseTimeout = timeouts[i]
		}
		sub, timedOut, err := r.executeToolCalls(wfCtx, reg.ExecuteToolActivity, opt, agentID, &base.RunContext, base.Messages, grouped[i], expectedChildren, parentTracker, finishBy)
		if err != nil {
			return nil, false, err
		}
		out = append(out, sub...)
		if timedOut {
			timedOutAny = true
		}
	}
	return out, timedOutAny, nil
}

// appendUserToolResults appends a user message with tool_result blocks for the
// executed tools and persists the canonical transcript delta. Tool results are
// ordered to match the assistant tool_use IDs from the allowed calls slice so
// that provider handshakes remain deterministic regardless of execution timing.
//
// If any tool has a ResultReminder configured in its spec, a system message
// with the reminder text is appended after the tool results to provide
// backstage guidance to the model.
func (r *Runtime) appendUserToolResults(
	ctx context.Context,
	agentID agent.Ident,
	base *planner.PlanInput,
	allowed []planner.ToolRequest,
	vals []*planner.ToolResult,
	turnID string,
) error {
	allowed, vals, err := r.filterPlannerVisibleToolResults(allowed, vals)
	if err != nil {
		return err
	}
	if len(vals) == 0 {
		return nil
	}
	resultsByID := make(map[string]*planner.ToolResult, len(vals))
	for _, tr := range vals {
		resultsByID[tr.ToolCallID] = tr
	}

	parts := make([]model.Part, 0, len(resultsByID))
	var reminders []string
	for _, call := range allowed {
		tr, ok := resultsByID[call.ToolCallID]
		if !ok {
			continue
		}
		content, err := r.toolResultContent(&call, tr)
		if err != nil {
			return err
		}
		parts = append(parts, model.ToolResultPart{
			ToolUseID: tr.ToolCallID,
			Content:   content,
			IsError:   tr.Error != nil,
		})
		if spec, ok := r.toolSpec(tr.Name); ok && spec.ResultReminder != "" {
			reminders = append(reminders, spec.ResultReminder)
		}
		if rem := retryHintReminder(tr); rem != "" {
			reminders = append(reminders, rem)
		}
		if spec, ok := r.toolSpec(tr.Name); ok {
			cursorField := ""
			if spec.Bounds != nil && spec.Bounds.Paging != nil {
				cursorField = spec.Bounds.Paging.CursorField
			}
			if rem := boundsReminder(tr, cursorField); rem != "" {
				reminders = append(reminders, rem)
			}
		} else if rem := boundsReminder(tr, ""); rem != "" {
			reminders = append(reminders, rem)
		}
	}
	if len(parts) == 0 {
		return nil
	}

	messages := []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: parts,
	}}

	if len(reminders) > 0 {
		var reminderText strings.Builder
		for i, rem := range reminders {
			if i > 0 {
				reminderText.WriteString("\n\n")
			}
			reminderText.WriteString("<system-reminder>")
			reminderText.WriteString(rem)
			reminderText.WriteString("</system-reminder>")
		}
		messages = append(messages, &model.Message{
			Role:  model.ConversationRoleSystem,
			Parts: []model.Part{model.TextPart{Text: reminderText.String()}},
		})
	}
	return r.appendTranscriptMessages(ctx, agentID, base, turnID, messages)
}

// filterPlannerVisibleToolCalls returns the subset of tool calls that are
// definitely visible before execution.
//
// Successful bookkeeping calls remain hidden from future planner turns because
// they are control-plane side effects rather than reasoning inputs. Retryable
// bookkeeping failures are appended later, after execution reveals the
// RetryHint-bearing result that must be replayed for repair.
func (r *Runtime) filterPlannerVisibleToolCalls(calls []planner.ToolRequest) []planner.ToolRequest {
	if len(calls) == 0 {
		return nil
	}
	filtered := make([]planner.ToolRequest, 0, len(calls))
	for _, call := range calls {
		if r.isBookkeeping(call.Name) {
			continue
		}
		filtered = append(filtered, call)
	}
	return filtered
}

// filterLatePlannerVisibleToolCalls returns bookkeeping calls that become
// planner-visible only after execution produced a retryable failure.
func (r *Runtime) filterLatePlannerVisibleToolCalls(calls []planner.ToolRequest, results []*planner.ToolResult) ([]planner.ToolRequest, error) {
	if len(calls) == 0 && len(results) == 0 {
		return nil, nil
	}
	if len(calls) != len(results) {
		return nil, fmt.Errorf("filter late planner-visible tool calls: calls/results length mismatch (%d != %d)", len(calls), len(results))
	}

	resultsByToolCallID := make(map[string]*planner.ToolResult, len(results))
	for _, result := range results {
		if result == nil {
			return nil, fmt.Errorf("filter late planner-visible tool calls: nil tool result")
		}
		if result.ToolCallID == "" {
			return nil, fmt.Errorf("filter late planner-visible tool calls: missing result tool_call_id for %s", result.Name)
		}
		if _, exists := resultsByToolCallID[result.ToolCallID]; exists {
			return nil, fmt.Errorf("filter late planner-visible tool calls: duplicate result tool_call_id %s", result.ToolCallID)
		}
		resultsByToolCallID[result.ToolCallID] = result
	}

	filtered := make([]planner.ToolRequest, 0, len(calls))
	for _, call := range calls {
		if call.ToolCallID == "" {
			return nil, fmt.Errorf("filter late planner-visible tool calls: missing call tool_call_id for %s", call.Name)
		}
		if !r.isBookkeeping(call.Name) {
			continue
		}
		result, ok := resultsByToolCallID[call.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("filter late planner-visible tool calls: missing result for tool_call_id %s", call.ToolCallID)
		}
		if !r.plannerVisibleToolResult(call, result) {
			continue
		}
		filtered = append(filtered, call)
	}
	return filtered, nil
}

// filterPlannerVisibleToolResults returns the subset of executed tool
// calls/results that remain visible to future planner turns. Bookkeeping tools
// still execute and publish durable run events, but only retryable bookkeeping
// failures remain visible because the planner must see their RetryHint to
// repair and resend the payload.
func (r *Runtime) filterPlannerVisibleToolResults(calls []planner.ToolRequest, results []*planner.ToolResult) ([]planner.ToolRequest, []*planner.ToolResult, error) {
	if len(calls) == 0 && len(results) == 0 {
		return nil, nil, nil
	}
	if len(calls) != len(results) {
		return nil, nil, fmt.Errorf("filter planner-visible tool results: calls/results length mismatch (%d != %d)", len(calls), len(results))
	}

	resultsByToolCallID := make(map[string]*planner.ToolResult, len(results))
	for _, result := range results {
		if result == nil {
			return nil, nil, fmt.Errorf("filter planner-visible tool results: nil tool result")
		}
		if result.ToolCallID == "" {
			return nil, nil, fmt.Errorf("filter planner-visible tool results: missing result tool_call_id for %s", result.Name)
		}
		if _, exists := resultsByToolCallID[result.ToolCallID]; exists {
			return nil, nil, fmt.Errorf("filter planner-visible tool results: duplicate result tool_call_id %s", result.ToolCallID)
		}
		resultsByToolCallID[result.ToolCallID] = result
	}

	filteredCalls := make([]planner.ToolRequest, 0, len(calls))
	filteredResults := make([]*planner.ToolResult, 0, len(results))
	for _, call := range calls {
		if call.ToolCallID == "" {
			return nil, nil, fmt.Errorf("filter planner-visible tool results: missing call tool_call_id for %s", call.Name)
		}
		result, ok := resultsByToolCallID[call.ToolCallID]
		if !ok {
			return nil, nil, fmt.Errorf("filter planner-visible tool results: missing result for tool_call_id %s", call.ToolCallID)
		}
		if !r.plannerVisibleToolResult(call, result) {
			continue
		}
		filteredCalls = append(filteredCalls, call)
		filteredResults = append(filteredResults, result)
	}
	return filteredCalls, filteredResults, nil
}

// plannerVisibleToolResult reports whether the executed result must be replayed
// into a future planner turn.
func (r *Runtime) plannerVisibleToolResult(call planner.ToolRequest, result *planner.ToolResult) bool {
	if !r.isBookkeeping(call.Name) {
		return true
	}
	return result != nil && result.Error != nil && result.RetryHint != nil
}

func (r *Runtime) toolResultContent(call *planner.ToolRequest, tr *planner.ToolResult) (any, error) {
	if tr == nil {
		return nil, nil
	}
	var resultJSON rawjson.Message
	if tr.Result != nil {
		raw, err := r.marshalToolValue(context.Background(), tr.Name, tr.Result, tr.Bounds)
		if err != nil {
			return nil, fmt.Errorf("runtime: encode tool_result for %s: %w", tr.Name, err)
		}
		resultJSON = rawjson.Message(raw)
	}
	errorMessage := ""
	if tr.Error != nil {
		errorMessage = tr.Error.Error()
	}
	return transcript.ProjectToolResultContent(
		resultJSON,
		tr.Bounds,
		formatResultPreviewForCall(context.Background(), r, call, tr.Result, tr.Bounds),
		errorMessage,
	)
}

// hardProtectionIfNeeded emits a protection event and signals finalization when
// agent-as-tool calls produced no child tool calls.
func (r *Runtime) hardProtectionIfNeeded(
	ctx context.Context,
	agentID agent.Ident,
	base *planner.PlanInput,
	vals []*planner.ToolResult,
	turnID string,
) (bool, error) {
	var agentToolCount int
	var totalChildren int
	toolNames := make([]tools.Ident, 0, len(vals))
	for _, tr := range vals {
		if spec, ok := r.toolSpec(tr.Name); ok && spec.IsAgentTool {
			agentToolCount++
			toolNames = append(toolNames, tr.Name)
			if tr.ChildrenCount > 0 {
				totalChildren += tr.ChildrenCount
			}
		}
	}
	if agentToolCount > 0 && totalChildren == 0 {
		if err := r.publishHook(
			ctx,
			hooks.NewHardProtectionEvent(
				base.RunContext.RunID,
				agentID,
				base.RunContext.SessionID,
				"agent_tool_no_children",
				agentToolCount,
				totalChildren,
				toolNames,
			),
			turnID,
		); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

// appendToolOutputs records canonical planner tool outputs in run-loop state.
//
// Contract:
//   - Calls must already have deterministic ToolCallIDs.
//   - Results are matched back to calls by ToolCallID so await-provided results
//     preserve the requested call order.
func (r *Runtime) appendToolOutputs(ctx context.Context, st *runLoopState, calls []planner.ToolRequest, results []*planner.ToolResult) error {
	if len(calls) == 0 {
		return nil
	}
	outputs, err := r.buildPlannerToolOutputs(ctx, calls, results)
	if err != nil {
		return err
	}
	st.ToolOutputs = append(st.ToolOutputs, outputs...)
	return nil
}

// buildNextResumeRequest converts the base plan input into provider-ready
// messages and builds the next PlanActivityInput.
func (r *Runtime) buildNextResumeRequest(
	agentID agent.Ident,
	base *planner.PlanInput,
	runPolicy *PolicyOverrides,
	toolOutputs []*planner.ToolOutput,
	nextAttempt *int,
) (PlanActivityInput, error) {
	resumeCtx := base.RunContext
	resumeCtx.Attempt = *nextAttempt
	*nextAttempt++
	plannerMsgs := cloneMessages(base.Messages)
	if err := transcript.ValidatePlannerTranscript(plannerMsgs); err != nil {
		return PlanActivityInput{}, fmt.Errorf("invalid resume transcript for run %s: %w", base.RunContext.RunID, err)
	}
	encodedToolOutputs, err := encodePlannerToolOutputs(toolOutputs)
	if err != nil {
		return PlanActivityInput{}, err
	}
	out := PlanActivityInput{
		AgentID:     agentID,
		RunID:       base.RunContext.RunID,
		Messages:    plannerMsgs,
		RunContext:  resumeCtx,
		Policy:      clonePolicyOverrides(runPolicy),
		ToolOutputs: encodedToolOutputs,
	}
	if err := enforcePlanActivityInputBudget(out); err != nil {
		return PlanActivityInput{}, err
	}
	return out, nil
}
