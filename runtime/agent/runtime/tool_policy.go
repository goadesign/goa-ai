// Package runtime compiles tool policy into the predicate used to advertise
// planner tools and enforce caller run constraints during execution.
package runtime

import (
	"fmt"
	"maps"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// compiledToolPolicy is the runtime-owned predicate built from per-run policy
	// overrides. The same constraints apply to advertising and execution.
	compiledToolPolicy struct {
		callerRestrictToTool tools.Ident
		tagClauses           []api.TagPolicyClause
	}

	// toolPolicyFacts carries the static tool facts needed by compiledToolPolicy.
	// Advertising obtains them from ToolSpec while execution-time filtering obtains
	// them from canonical policy metadata; both paths share the same predicate.
	toolPolicyFacts struct {
		tags []string
	}
)

// compileToolPolicy converts public run overrides into the runtime predicate.
func compileToolPolicy(overrides *PolicyOverrides) compiledToolPolicy {
	if overrides == nil {
		return compiledToolPolicy{}
	}
	return compiledToolPolicy{
		callerRestrictToTool: overrides.RestrictToTool,
		tagClauses:           cloneTagPolicyClauses(overrides.TagClauses),
	}
}

// clonePolicyOverrides deep-copies per-run policy so workflow/activity payloads
// remain isolated from later caller mutation.
func clonePolicyOverrides(overrides *PolicyOverrides) *PolicyOverrides {
	if overrides == nil {
		return nil
	}
	cloned := *overrides
	cloned.TagClauses = cloneTagPolicyClauses(overrides.TagClauses)
	if len(overrides.PerToolTimeout) > 0 {
		cloned.PerToolTimeout = maps.Clone(overrides.PerToolTimeout)
	}
	cloned.LimitTerminalPlans = cloneLimitTerminalPlans(overrides.LimitTerminalPlans)
	return &cloned
}

// cloneTagPolicyClauses deep-copies tag clauses and their slices.
func cloneTagPolicyClauses(clauses []api.TagPolicyClause) []api.TagPolicyClause {
	if len(clauses) == 0 {
		return nil
	}
	cloned := make([]api.TagPolicyClause, len(clauses))
	for i, clause := range clauses {
		cloned[i] = api.TagPolicyClause{
			AllowedAny: append([]string(nil), clause.AllowedAny...),
			DeniedAny:  append([]string(nil), clause.DeniedAny...),
		}
	}
	return cloned
}

// isZero reports whether the compiled policy has no effect.
func (p compiledToolPolicy) isZero() bool {
	return p.callerRestrictToTool == "" && len(p.tagClauses) == 0
}

// allowsTool reports whether the named tool with the provided tags passes the
// full compiled policy.
func (p compiledToolPolicy) allowsTool(name tools.Ident, facts toolPolicyFacts) bool {
	if name == tools.ToolUnavailable {
		return true
	}
	if p.callerRestrictToTool != "" && name != p.callerRestrictToTool {
		return false
	}
	return TagPolicyAllows(p.tagClauses, facts.tags)
}

// TagPolicyAllows reports whether tool tags satisfy every tag-policy clause.
// Callers that render instructions before starting a run use this predicate to
// keep those instructions aligned with the tools the runtime will advertise.
func TagPolicyAllows(clauses []TagPolicyClause, tags []string) bool {
	for _, clause := range clauses {
		if !tagClauseAllows(clause, tags) {
			return false
		}
	}
	return true
}

// advertisedToolDefinitions materializes model-facing tool definitions after
// applying the compiled runtime policy to registered tool specs.
func (r *Runtime) advertisedToolDefinitions(
	specs []tools.ToolSpec,
	policy compiledToolPolicy,
) []*model.ToolDefinition {
	r.mu.RLock()
	defer r.mu.RUnlock()
	definitions := make([]*model.ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		if !policy.allowsTool(spec.Name, toolPolicyFactsFromSpec(spec)) {
			continue
		}
		base := r.toolDefinitions[spec.Name]
		if base == nil {
			panic(fmt.Sprintf("runtime: tool %q has no compiled model definition", spec.Name))
		}
		definition := *base
		definitions = append(definitions, &definition)
	}
	return definitions
}

// toolPolicyFactsFromSpec projects a registered tool spec into policy facts for
// planner-visible advertising decisions.
func toolPolicyFactsFromSpec(spec tools.ToolSpec) toolPolicyFacts {
	return toolPolicyFacts{
		tags: spec.Tags,
	}
}

// toolPolicyFactsFromMetadata projects canonical runtime metadata into policy
// facts for execution-time filtering decisions.
func toolPolicyFactsFromMetadata(meta policy.ToolMetadata) toolPolicyFacts {
	return toolPolicyFacts{
		tags: meta.Tags,
	}
}

// tagClauseAllows evaluates one explicit tag clause against a tool tag set.
func tagClauseAllows(clause api.TagPolicyClause, tags []string) bool {
	if len(clause.AllowedAny) > 0 && !hasIntersection(tags, clause.AllowedAny) {
		return false
	}
	if len(clause.DeniedAny) > 0 && hasIntersection(tags, clause.DeniedAny) {
		return false
	}
	return true
}
