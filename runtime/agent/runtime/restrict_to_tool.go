package runtime

// restrict_to_tool.go centralizes the runtime semantics for retry-driven
// tool correction mode.
//
// Contract:
// - A tool result may promote the run into restricted-tool mode by returning a
//   RetryHint with RestrictToTool=true and Tool set. Every restricting hint in
//   a batch contributes its tool, so the active restriction set always matches
//   the per-failure retry reminders the runtime emits.
// - Once restricted-tool mode is active, each restricted tool keeps its
//   constraint until that tool succeeds. The correction is scoped to the failed
//   tool payloads, not to the rest of the run.
// - Caller-supplied WithRestrictToTool policy is run-scoped. Retry restrictions
//   live in separate runtime-owned state and never clear caller policy.
// - Retry restricted-tool mode constrains budgeted work tools only. Bookkeeping
//   tools, including terminal run tools, are never filtered by retry-owned state.
// - Caller-supplied WithRestrictToTool policy is run-scoped and absolute; it
//   still filters bookkeeping and terminal run tools.

import (
	"slices"

	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// applyToolResultPolicyHints applies retry restrictions from tool outputs.
// Satisfied restrictions clear first so a successful correction releases only
// its own constraint, then every restricting hint whose tool did not succeed in
// the same batch joins the restriction set.
func applyToolResultPolicyHints(input *RunInput, results []*planner.ToolResult) {
	if len(results) == 0 {
		return
	}
	clearSatisfiedToolRestrictions(input, results)
	succeeded := succeededToolNames(results)
	for _, result := range results {
		if result == nil || result.RetryHint == nil || !result.RetryHint.RestrictToTool || result.RetryHint.Tool == "" {
			continue
		}
		if _, ok := succeeded[result.RetryHint.Tool]; ok {
			continue
		}
		if input.Policy == nil {
			input.Policy = &PolicyOverrides{}
		}
		if slices.Contains(input.Policy.RetryRestrictToTools, result.RetryHint.Tool) {
			continue
		}
		input.Policy.RetryRestrictToTools = append(input.Policy.RetryRestrictToTools, result.RetryHint.Tool)
	}
}

// clearSatisfiedToolRestrictions removes each retry-driven restriction whose
// tool produced a successful result in the current batch.
func clearSatisfiedToolRestrictions(input *RunInput, results []*planner.ToolResult) {
	if input == nil || input.Policy == nil || len(input.Policy.RetryRestrictToTools) == 0 {
		return
	}
	succeeded := succeededToolNames(results)
	remaining := slices.DeleteFunc(input.Policy.RetryRestrictToTools, func(name tools.Ident) bool {
		_, ok := succeeded[name]
		return ok
	})
	if len(remaining) == 0 {
		remaining = nil
	}
	input.Policy.RetryRestrictToTools = remaining
}

// succeededToolNames indexes the tools that produced a successful result in the
// current batch.
func succeededToolNames(results []*planner.ToolResult) map[tools.Ident]struct{} {
	succeeded := make(map[tools.Ident]struct{}, len(results))
	for _, result := range results {
		if result != nil && result.Error == nil {
			succeeded[result.Name] = struct{}{}
		}
	}
	return succeeded
}
