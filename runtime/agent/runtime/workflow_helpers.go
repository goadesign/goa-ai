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
	finishBy time.Time,
	grouped [][]planner.ToolRequest,
	timeouts []time.Duration,
	toolOpts engine.ActivityOptions,
) ([]*planner.ToolResult, bool, error) {
	var out []*planner.ToolResult
	timedOutAny := false
	for i := range grouped {
		opt := toolOpts
		if timeouts[i] > 0 {
			opt.Timeout = timeouts[i]
		}
		sub, timedOut, err := r.executeToolCalls(
			wfCtx, reg.ExecuteToolActivity, opt, base.RunContext.RunID, agentID,
			&base.RunContext, grouped[i], expectedChildren, turnID, parentTracker, finishBy,
		)
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

// sidecarDescription returns a human-facing description for the tool's
// artifact sidecar when available. It uses the ArtifactDescription metadata
// computed at code generation time from the Artifact DSL.
func (r *Runtime) sidecarDescription(name tools.Ident) string {
	spec, ok := r.toolSpec(name)
	if !ok {
		return ""
	}
	return spec.ArtifactDescription
}

// buildArtifactProducedReminders derives artifact-aware reminders for artifacts
// that were actually produced in this turn. It returns reminder bodies without
// <system-reminder> wrappers; callers are responsible for wrapping.
func (r *Runtime) buildArtifactProducedReminders(results []*planner.ToolResult) []string {
	if len(results) == 0 {
		return nil
	}
	seenKinds := make(map[string]struct{})
	var out []string
	for _, tr := range results {
		if tr == nil || len(tr.Artifacts) == 0 {
			continue
		}
		for _, art := range tr.Artifacts {
			if art == nil || art.Kind == "" {
				continue
			}
			if _, exists := seenKinds[art.Kind]; exists {
				continue
			}
			// Prefer the declaring tool (SourceTool) when available so that
			// descriptions align with the tool that attached the artifact.
			desc := ""
			if art.SourceTool != "" {
				desc = r.sidecarDescription(art.SourceTool)
			}
			// Fallback to the current tool when SourceTool is not present.
			if desc == "" {
				desc = r.sidecarDescription(tr.Name)
			}
			if desc == "" {
				continue
			}
			out = append(out, fmt.Sprintf("The user sees: %s", desc))
			seenKinds[art.Kind] = struct{}{}
		}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

func (r *Runtime) buildArtifactDisabledReminders(allowed []planner.ToolRequest, resultsByID map[string]*planner.ToolResult, artifactsModeByCallID map[string]tools.ArtifactsMode) []string {
	if len(allowed) == 0 || len(resultsByID) == 0 || len(artifactsModeByCallID) == 0 {
		return nil
	}
	seen := make(map[tools.Ident]struct{})
	out := make([]string, 0, len(allowed))
	for _, call := range allowed {
		if call.ToolCallID == "" {
			continue
		}
		if !artifactsDisabled(artifactsModeByCallID[call.ToolCallID]) {
			continue
		}
		tr := resultsByID[call.ToolCallID]
		if tr == nil || tr.Error != nil {
			continue
		}
		if _, dup := seen[call.Name]; dup {
			continue
		}
		desc := r.sidecarDescription(call.Name)
		if desc == "" {
			continue
		}
		out = append(out, fmt.Sprintf("Artifacts were disabled for this tool call. You can re-run with artifacts enabled to show: %s", desc))
		seen[call.Name] = struct{}{}
	}
	if len(out) == 0 {
		return nil
	}
	sort.Strings(out)
	return out
}

// appendUserToolResults appends a user message with tool_result blocks for the
// executed tools and updates the ledger. Tool results are ordered to match the
// assistant tool_use IDs from the allowed calls slice so that provider
// handshakes remain deterministic regardless of execution timing.
//
// If any tool has a ResultReminder configured in its spec, a system message
// with the reminder text is appended after the tool results to provide
// backstage guidance to the model.
func (r *Runtime) appendUserToolResults(
	base *planner.PlanInput,
	allowed []planner.ToolRequest,
	vals []*planner.ToolResult,
	led *transcript.Ledger,
	artifactsModeByCallID map[string]tools.ArtifactsMode,
) {
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
		content := toolResultContent(tr)
		parts = append(parts, model.ToolResultPart{
			ToolUseID: tr.ToolCallID,
			Content:   content,
			IsError:   tr.Error != nil,
		})
		specs = append(specs, transcript.ToolResultSpec{
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
			if spec.Paging != nil {
				cursorField = spec.Paging.CursorField
			}
			if rem := boundsReminder(tr, cursorField); rem != "" {
				reminders = append(reminders, rem)
			}
		} else if rem := boundsReminder(tr, ""); rem != "" {
			reminders = append(reminders, rem)
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

	// Derive artifact-aware reminders from produced artifacts using the
	// sidecar schema descriptions so the model learns what the user sees.
	if artRems := r.buildArtifactProducedReminders(vals); len(artRems) > 0 {
		reminders = append(reminders, artRems...)
	}
	if disabled := r.buildArtifactDisabledReminders(allowed, resultsByID, artifactsModeByCallID); len(disabled) > 0 {
		reminders = append(reminders, disabled...)
	}

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

func toolResultContent(tr *planner.ToolResult) any {
	if tr == nil {
		return nil
	}
	if tr.Error == nil {
		return tr.Result
	}
	if tr.Result == nil {
		return map[string]any{
			"error": tr.Error,
		}
	}
	return map[string]any{
		"result": tr.Result,
		"error":  tr.Error,
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
