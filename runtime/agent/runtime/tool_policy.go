// Package runtime compiles per-run tool policy into one reusable predicate used
// both before planner prompting and during execution-time enforcement.
package runtime

import (
	"maps"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// compiledToolPolicy is the runtime-owned predicate built from per-run policy
	// overrides. It is the single source of truth for planner-visible tool
	// advertising and execution-time filtering.
	compiledToolPolicy struct {
		callerRestrictToTool tools.Ident
		retryRestrictToTool  tools.Ident
		tagClauses           []api.TagPolicyClause
	}

	// toolPolicyFacts carries the static tool facts needed by compiledToolPolicy.
	// Advertising obtains them from ToolSpec while execution-time filtering obtains
	// them from canonical policy metadata; both paths share the same predicate.
	toolPolicyFacts struct {
		tags        []string
		bookkeeping bool
	}
)

// compileToolPolicy converts public run overrides into the runtime predicate.
func compileToolPolicy(overrides *PolicyOverrides) compiledToolPolicy {
	if overrides == nil {
		return compiledToolPolicy{}
	}
	clauses := make([]api.TagPolicyClause, 0, 1+len(overrides.TagClauses))
	if clause, ok := legacyTagPolicyClause(overrides); ok {
		clauses = append(clauses, clause)
	}
	clauses = append(clauses, cloneTagPolicyClauses(overrides.TagClauses)...)
	return compiledToolPolicy{
		callerRestrictToTool: overrides.RestrictToTool,
		retryRestrictToTool:  overrides.RetryRestrictToTool,
		tagClauses:           clauses,
	}
}

// clonePolicyOverrides deep-copies per-run policy so workflow/activity payloads
// remain isolated from later caller mutation.
func clonePolicyOverrides(overrides *PolicyOverrides) *PolicyOverrides {
	if overrides == nil {
		return nil
	}
	cloned := *overrides
	cloned.AllowedTags = append([]string(nil), overrides.AllowedTags...)
	cloned.DeniedTags = append([]string(nil), overrides.DeniedTags...)
	cloned.TagClauses = cloneTagPolicyClauses(overrides.TagClauses)
	if len(overrides.PerToolTimeout) > 0 {
		cloned.PerToolTimeout = maps.Clone(overrides.PerToolTimeout)
	}
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

// legacyTagPolicyClause lifts AllowedTags/DeniedTags into the explicit clause model.
func legacyTagPolicyClause(overrides *PolicyOverrides) (api.TagPolicyClause, bool) {
	if overrides == nil || (len(overrides.AllowedTags) == 0 && len(overrides.DeniedTags) == 0) {
		return api.TagPolicyClause{}, false
	}
	return api.TagPolicyClause{
		AllowedAny: append([]string(nil), overrides.AllowedTags...),
		DeniedAny:  append([]string(nil), overrides.DeniedTags...),
	}, true
}

// isZero reports whether the compiled policy has no effect.
func (p compiledToolPolicy) isZero() bool {
	return p.callerRestrictToTool == "" && p.retryRestrictToTool == "" && len(p.tagClauses) == 0
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
	if p.retryRestrictToTool != "" && name != p.retryRestrictToTool && !facts.bookkeeping {
		return false
	}
	for _, clause := range p.tagClauses {
		if !tagClauseAllows(clause, facts.tags) {
			return false
		}
	}
	return true
}

// advertisedToolDefinitions materializes model-facing tool definitions after
// applying the compiled runtime policy to registered tool specs.
func advertisedToolDefinitions(specs []tools.ToolSpec, policy compiledToolPolicy) []*model.ToolDefinition {
	definitions := make([]*model.ToolDefinition, 0, len(specs))
	for _, spec := range specs {
		if !policy.allowsTool(spec.Name, toolPolicyFactsFromSpec(spec)) {
			continue
		}
		definitions = append(definitions, toolDefinitionFromSpec(spec))
	}
	return definitions
}

// toolPolicyFactsFromSpec projects a registered tool spec into policy facts for
// planner-visible advertising decisions.
func toolPolicyFactsFromSpec(spec tools.ToolSpec) toolPolicyFacts {
	return toolPolicyFacts{
		tags:        spec.Tags,
		bookkeeping: spec.Bookkeeping,
	}
}

// toolPolicyFactsFromMetadata projects canonical runtime metadata into policy
// facts for execution-time filtering decisions.
func toolPolicyFactsFromMetadata(meta policy.ToolMetadata) toolPolicyFacts {
	return toolPolicyFacts{
		tags:        meta.Tags,
		bookkeeping: meta.BudgetClass == policy.ToolBudgetClassBookkeeping,
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

// toolDefinitionFromSpec converts one runtime tool spec into the model-facing
// shape advertised to providers. Invalid generated schemas are invariant
// violations and therefore panic.
func toolDefinitionFromSpec(spec tools.ToolSpec) *model.ToolDefinition {
	return model.ToolDefinitionFromSpec(spec)
}
