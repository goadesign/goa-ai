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
	turnID string,
	parentTracker *childTracker,
	deadline time.Time,
	grouped [][]planner.ToolRequest,
	timeouts []time.Duration,
	toolOpts engine.ActivityOptions,
) ([]*planner.ToolResult, error) {
	var out []*planner.ToolResult
	for i := range grouped {
		opt := toolOpts
		if timeouts[i] > 0 {
			opt.Timeout = timeouts[i]
		}
		sub, err := r.executeToolCalls(
			wfCtx, reg.ExecuteToolActivity, opt, base.RunContext.RunID, agentID,
			&base.RunContext, grouped[i], expectedChildren, turnID, parentTracker, deadline,
		)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// appendUserToolResults appends a user message with tool_result blocks for the
// executed tools and updates the ledger. Tool results are ordered to match the
// assistant tool_use IDs from the allowed calls slice so that provider
// handshakes remain deterministic regardless of execution timing.
//
// If any tool has a ResultReminder configured in its spec, a system message
// with the reminder text is appended after the tool results to provide
// backstage guidance to the model.
func (r *Runtime) appendUserToolResults(base *planner.PlanInput, allowed []planner.ToolRequest, vals []*planner.ToolResult, led *transcript.Ledger) {
	if len(vals) == 0 {
		return
	}
	resultsByID := make(map[string]*planner.ToolResult, len(vals))
	for _, tr := range vals {
		if tr == nil || tr.ToolCallID == "" {
			continue
		}
		resultsByID[tr.ToolCallID] = tr
	}

	parts := make([]model.Part, 0, len(resultsByID))
	specs := make([]transcript.ToolResultSpec, 0, len(resultsByID))
	var reminders []string
	for _, call := range allowed {
		tr, ok := resultsByID[call.ToolCallID]
		if !ok || tr == nil || tr.ToolCallID == "" {
			continue
		}
		parts = append(parts, model.ToolResultPart{
			ToolUseID: tr.ToolCallID,
			Content:   tr.Result,
			IsError:   tr.Error != nil,
		})
		specs = append(specs, transcript.ToolResultSpec{
			ToolUseID: tr.ToolCallID,
			Content:   tr.Result,
			IsError:   tr.Error != nil,
		})
		if spec, ok := r.toolSpec(tr.Name); ok && spec.ResultReminder != "" {
			reminders = append(reminders, spec.ResultReminder)
		}
	}
	if len(parts) == 0 {
		return
	}

	base.Messages = append(base.Messages, &model.Message{
		Role:  model.ConversationRoleUser,
		Parts: parts,
	})
	led.AppendUserToolResults(specs)

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
		base.Messages = append(base.Messages, &model.Message{
			Role:  model.ConversationRoleSystem,
			Parts: []model.Part{model.TextPart{Text: reminderText.String()}},
		})
	}
}

// deriveBounds extracts Bounds metadata from a decoded tool result when the
// result type implements agent.BoundedResult. It returns nil only when the
// value does not implement the interface or when ResultBounds() returns nil.
// A zero-value Bounds (Returned=0, Total=nil, Truncated=false, RefinementHint="")
// is valid metadata indicating no truncation occurred and is returned as-is.
func deriveBounds(result any) *agent.Bounds {
	if result == nil {
		return nil
	}
	br, ok := result.(agent.BoundedResult)
	if !ok || br == nil {
		return nil
	}
	return br.ResultBounds()
}

// hardProtectionIfNeeded emits a protection event and signals finalization when
// agent-as-tool calls produced no child tool calls.
func (r *Runtime) hardProtectionIfNeeded(
	ctx context.Context,
	agentID agent.Ident,
	base *planner.PlanInput,
	vals []*planner.ToolResult,
	turnID string,
) bool {
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
		r.publishHook(
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
		)
		return true
	}
	return false
}

// buildNextResumeRequest converts the base plan input into provider-ready
// messages and builds the next PlanActivityInput.
func (r *Runtime) buildNextResumeRequest(
	agentID agent.Ident,
	base *planner.PlanInput,
	lastToolResults []*planner.ToolResult,
	nextAttempt *int,
) (PlanActivityInput, error) {
	resumeCtx := base.RunContext
	resumeCtx.Attempt = *nextAttempt
	*nextAttempt++
	plannerMsgs := cloneMessages(base.Messages)
	if err := transcript.ValidateBedrock(plannerMsgs, false); err != nil {
		return PlanActivityInput{}, fmt.Errorf("invalid Bedrock transcript for run %s: %w", base.RunContext.RunID, err)
	}
	return PlanActivityInput{
		AgentID:     agentID,
		RunID:       base.RunContext.RunID,
		Messages:    plannerMsgs,
		RunContext:  resumeCtx,
		ToolResults: lastToolResults,
	}, nil
}
