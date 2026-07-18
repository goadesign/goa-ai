package runtime

// workflow_policy.go contains policy-related helpers used by the workflow loop to
// filter and cap tool calls deterministically.
//
// Contract:
// - Per-run overrides are applied first using the same compiled predicate used to
//   advertise tools to planners.
// - Runtime policy decisions rewrite denied calls to tool_unavailable so one
//   provider response remains an atomic transcript unit.

import (
	"context"
	"errors"
	"fmt"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

// applyPerRunOverrides rewrites policy-denied tool calls to the runtime-owned
// tool_unavailable tool using the same compiled predicate that already shaped
// the planner-visible advertised tool set.
func (r *Runtime) applyPerRunOverrides(ctx context.Context, input *RunInput, candidates []planner.ToolRequest) ([]planner.ToolRequest, error) {
	if input == nil || len(candidates) == 0 {
		return candidates, nil
	}
	runPolicy := compileToolPolicy(input.Policy)
	if runPolicy.isZero() {
		return candidates, nil
	}
	r.logger.Info(
		ctx,
		"Applying per-run policy overrides",
		"restrict_to_tool",
		input.Policy.RestrictToTool,
		"tag_clauses",
		len(input.Policy.TagClauses),
	)
	metas := r.toolMetadata(candidates)
	rewritten := make([]planner.ToolRequest, 0, len(candidates))
	for i, call := range candidates {
		if runPolicy.allowsTool(call.Name, toolPolicyFactsFromMetadata(metas[i])) {
			rewritten = append(rewritten, call)
			continue
		}
		r.logger.Info(ctx, "Tool rewritten by run policy", "tool", call.Name, "tags", metas[i].Tags)
		unavailable, err := r.rewriteToolCallUnavailable(call)
		if err != nil {
			return nil, err
		}
		rewritten = append(rewritten, unavailable)
	}
	r.logger.Info(ctx, "After per-run policy rewriting", "candidates", len(rewritten))
	return rewritten, nil
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
		RetryHint:     retry,
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
		allowed, err = r.rewritePolicyDeniedToolCalls(allowed, decision.AllowedTools)
		if err != nil {
			return nil, caps, err
		}
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

// rewritePolicyDeniedToolCalls preserves one provider response atomically by
// converting denied calls into runtime-owned tool_unavailable executions.
func (r *Runtime) rewritePolicyDeniedToolCalls(calls []planner.ToolRequest, allowed []tools.Ident) ([]planner.ToolRequest, error) {
	allow := make(map[tools.Ident]struct{}, len(allowed))
	for _, name := range allowed {
		allow[name] = struct{}{}
	}
	out := make([]planner.ToolRequest, len(calls))
	for i, call := range calls {
		if call.Name == tools.ToolUnavailable {
			out[i] = call
			continue
		}
		if _, ok := allow[call.Name]; ok {
			out[i] = call
			continue
		}
		rewritten, err := r.rewriteToolCallUnavailable(call)
		if err != nil {
			return nil, err
		}
		out[i] = rewritten
	}
	return out, nil
}

// admitToolBatch checks one atomic model tool-call response against the
// run-level MaxToolCalls budget.
//
// The run-level MaxToolCalls budget applies to budgeted (non-bookkeeping) tools
// only. The response is admitted whole when every budgeted call fits and
// rejected whole otherwise; provider responses are never partially edited.
func (r *Runtime) admitToolBatch(calls []planner.ToolRequest, caps policy.CapsState) (int, bool) {
	remaining := caps.RemainingToolCalls
	if remaining < 0 {
		panic(fmt.Sprintf("runtime: negative remaining tool calls: %d", remaining))
	}
	budgetCost := 0
	for _, call := range calls {
		if !r.isBookkeeping(call.Name) {
			budgetCost++
		}
	}
	return budgetCost, caps.MaxToolCalls <= 0 || budgetCost <= remaining
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
