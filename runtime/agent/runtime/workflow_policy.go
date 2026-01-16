package runtime

// workflow_policy.go contains policy-related helpers used by the workflow loop to
// filter and cap tool calls deterministically.
//
// Contract:
// - Per-run overrides are applied first (restrict-to-tool and tag allow/deny).
// - Runtime policy decisions can further filter calls and update caps.

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
)

// applyPerRunOverrides filters candidate tool calls using per-run overrides:
// RestrictToTool and tag allow/deny lists. Returns the filtered slice.
func (r *Runtime) applyPerRunOverrides(ctx context.Context, input *RunInput, candidates []planner.ToolRequest) []planner.ToolRequest {
	if input == nil || input.Policy == nil || len(candidates) == 0 {
		return candidates
	}
	ov := input.Policy
	if ov.RestrictToTool == "" && len(ov.AllowedTags) == 0 && len(ov.DeniedTags) == 0 {
		return candidates
	}
	r.logger.Info(ctx, "Applying per-run policy overrides", "restrict_to_tool", ov.RestrictToTool, "allowed_tags", ov.AllowedTags, "denied_tags", ov.DeniedTags)
	metas := r.toolMetadata(candidates)
	filtered := make([]planner.ToolRequest, 0, len(candidates))
	for i, call := range candidates {
		if ov.RestrictToTool != "" && call.Name != ov.RestrictToTool {
			r.logger.Info(ctx, "Tool filtered by RestrictToTool", "tool", call.Name)
			continue
		}
		ok := true
		if len(ov.AllowedTags) > 0 || len(ov.DeniedTags) > 0 {
			tags := metas[i].Tags
			if len(ov.AllowedTags) > 0 && !hasIntersection(tags, ov.AllowedTags) {
				r.logger.Info(ctx, "Tool filtered by AllowedTags", "tool", call.Name, "tags", tags, "required", ov.AllowedTags)
				ok = false
			}
			if ok && len(ov.DeniedTags) > 0 && hasIntersection(tags, ov.DeniedTags) {
				r.logger.Info(ctx, "Tool filtered by DeniedTags", "tool", call.Name, "tags", tags, "denied", ov.DeniedTags)
				ok = false
			}
		}
		if ok {
			filtered = append(filtered, call)
		}
	}
	r.logger.Info(ctx, "After per-run policy filtering", "candidates", len(filtered))
	return filtered
}

// applyRuntimePolicy applies the runtime policy (if configured) to the provided
// candidates, returning the allowed set and updated caps. It also records and
// publishes the policy decision.
func (r *Runtime) applyRuntimePolicy(
	ctx context.Context,
	base *planner.PlanInput,
	input *RunInput,
	candidates []planner.ToolRequest,
	caps policy.CapsState,
	turnID string,
	retry *planner.RetryHint,
) ([]planner.ToolRequest, policy.CapsState, error) {
	if r.Policy == nil {
		return candidates, caps, nil
	}
	r.logger.Info(ctx, "Applying runtime policy decision")
	decision, err := r.Policy.Decide(ctx, policy.Input{
		RunContext:    base.RunContext,
		Tools:         r.toolMetadata(candidates),
		RetryHint:     toPolicyRetryHint(retry),
		RemainingCaps: caps,
		Requested:     toolHandles(candidates),
		Labels:        base.RunContext.Labels,
	})
	if err != nil {
		return nil, caps, err
	}
	if len(decision.Labels) > 0 {
		base.RunContext.Labels = mergeLabels(base.RunContext.Labels, decision.Labels)
		input.Labels = mergeLabels(input.Labels, decision.Labels)
	}
	if decision.DisableTools {
		return nil, caps, errors.New("tool execution disabled by policy")
	}
	allowed := candidates
	if len(decision.AllowedTools) > 0 {
		allowed = filterToolCalls(allowed, decision.AllowedTools)
	}
	caps = mergeCaps(caps, decision.Caps)
	if err := r.publishHook(
		ctx,
		hooks.NewPolicyDecisionEvent(
			base.RunContext.RunID,
			input.AgentID,
			base.RunContext.SessionID,
			decision.AllowedTools,
			caps,
			cloneLabels(decision.Labels),
			cloneMetadata(decision.Metadata),
		),
		turnID,
	); err != nil {
		return nil, caps, err
	}
	return allowed, caps, nil
}

// capAllowedCalls applies per-turn and remaining caps to the allowed set.
func (r *Runtime) capAllowedCalls(allowed []planner.ToolRequest, input *RunInput, caps policy.CapsState) []planner.ToolRequest {
	if input.Policy != nil && input.Policy.PerTurnMaxToolCalls > 0 && len(allowed) > input.Policy.PerTurnMaxToolCalls {
		allowed = allowed[:input.Policy.PerTurnMaxToolCalls]
	}
	if caps.MaxToolCalls > 0 && caps.RemainingToolCalls < len(allowed) {
		allowed = allowed[:caps.RemainingToolCalls]
	}
	return allowed
}

// prepareAllowedCallsMetadata stamps run/session/turn IDs and deterministic tool
// call IDs on allowed calls. It also fills parentToolCallID when tracking children.
func (r *Runtime) prepareAllowedCallsMetadata(agentID agent.Ident, base *planner.PlanInput, allowed []planner.ToolRequest, parentTracker *childTracker) []planner.ToolRequest {
	for i := range allowed {
		if allowed[i].RunID == "" {
			allowed[i].RunID = base.RunContext.RunID
		}
		if allowed[i].AgentID == "" {
			allowed[i].AgentID = agentID
		}
		if allowed[i].SessionID == "" {
			allowed[i].SessionID = base.RunContext.SessionID
		}
		if allowed[i].TurnID == "" {
			allowed[i].TurnID = base.RunContext.TurnID
		}
		if allowed[i].ToolCallID == "" {
			allowed[i].ToolCallID = generateDeterministicToolCallID(
				base.RunContext.RunID, base.RunContext.TurnID, base.RunContext.Attempt, allowed[i].Name, i,
			)
		}
		if parentTracker != nil && allowed[i].ParentToolCallID == "" {
			allowed[i].ParentToolCallID = parentTracker.parentToolCallID
		}
	}
	return allowed
}
