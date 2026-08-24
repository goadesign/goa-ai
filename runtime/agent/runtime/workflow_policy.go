package runtime

// workflow_policy.go contains policy-related helpers used by the workflow loop to
// filter and cap tool calls deterministically.
//
// Contract:
// - Per-run overrides are applied first using the same compiled predicate used to
//   advertise tools to planners.
// - A call excluded from the advertised per-run catalog is invalid planner
//   output.
// - A runtime policy decision made after planning uses tool_unavailable so one
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

// applyPerRunOverrides rejects calls excluded by the same rule that shaped the
// planner-visible tool list.
func (r *Runtime) applyPerRunOverrides(ctx context.Context, input *RunInput, candidates []ToolCall) ([]ToolCall, error) {
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
	for i, call := range candidates {
		if runPolicy.allowsTool(call.Name, toolPolicyFactsFromMetadata(metas[i])) {
			continue
		}
		return nil, planner.NewOutputContractError(
			fmt.Errorf("planner called tool %q excluded from this run", call.Name),
		)
	}
	return candidates, nil
}

// applyRuntimePolicy applies the runtime policy (if configured) to the provided
// candidates, returning the allowed set and updated caps. It also records and
// publishes the policy decision.
func (r *Runtime) applyRuntimePolicy(
	ctx context.Context,
	base *planner.PlanInput,
	input *RunInput,
	candidates []ToolCall,
	caps policy.CapsState,
	turnID string,
) ([]ToolCall, policy.CapsState, error) {
	if r.Policy == nil {
		return candidates, caps, nil
	}
	r.logger.Info(ctx, "Applying runtime policy decision")
	decision, err := r.Policy.Decide(ctx, policy.Input{
		RunContext:    base.RunContext,
		Tools:         r.toolMetadata(candidates),
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
func (r *Runtime) rewritePolicyDeniedToolCalls(calls []ToolCall, allowed []tools.Ident) ([]ToolCall, error) {
	allow := make(map[tools.Ident]struct{}, len(allowed))
	for _, name := range allowed {
		allow[name] = struct{}{}
	}
	out := make([]ToolCall, len(calls))
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
func (r *Runtime) admitToolBatch(calls []ToolCall, caps policy.CapsState) (int, bool) {
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

// prepareAllowedCallsMetadata adds run metadata after the call's owner has
// supplied its stable ID.
func (r *Runtime) prepareAllowedCallsMetadata(agentID agent.Ident, base *planner.PlanInput, allowed []ToolCall, parentTracker *childTracker) []ToolCall {
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
		if parentTracker != nil && allowed[i].ParentToolCallID == "" {
			allowed[i].ParentToolCallID = parentTracker.parentToolCallID
		}
	}
	return allowed
}
