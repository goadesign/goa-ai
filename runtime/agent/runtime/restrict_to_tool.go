package runtime

// restrict_to_tool.go centralizes the runtime semantics for retry-driven
// single-tool correction mode.
//
// Contract:
// - A tool result may promote the run into restricted-tool mode by returning a
//   RetryHint with RestrictToTool=true and Tool set.
// - Once restricted-tool mode is active, the runtime keeps that constraint until
//   the requested tool succeeds. The correction is scoped to the failed tool
//   payload, not to the rest of the run.
// - Caller-supplied WithRestrictToTool policy is run-scoped. Retry restrictions
//   live in separate runtime-owned state and never clear caller policy.
// - Retry restricted-tool mode constrains budgeted work tools only. Bookkeeping
//   tools, including terminal run tools, are never filtered by retry-owned state.
// - Caller-supplied WithRestrictToTool policy is run-scoped and absolute; it
//   still filters bookkeeping and terminal run tools.

import "goa.design/goa-ai/runtime/agent/planner"

// applyToolResultPolicyHints applies retry restrictions from tool outputs. A
// successful result for the retry-restricted tool clears only retry-owned state
// so later planner turns can use the full caller-allowed tool set again.
func applyToolResultPolicyHints(input *RunInput, results []*planner.ToolResult) {
	if len(results) == 0 {
		return
	}
	clearSatisfiedToolRestriction(input, results)
	for _, result := range results {
		if result == nil || result.RetryHint == nil || !result.RetryHint.RestrictToTool || result.RetryHint.Tool == "" {
			continue
		}
		if input.Policy == nil {
			input.Policy = &PolicyOverrides{}
		}
		input.Policy.RetryRestrictToTool = result.RetryHint.Tool
		return
	}
}

// clearSatisfiedToolRestriction removes a retry-driven restriction after the
// requested tool has produced a successful result in the current batch.
func clearSatisfiedToolRestriction(input *RunInput, results []*planner.ToolResult) {
	if input == nil || input.Policy == nil || input.Policy.RetryRestrictToTool == "" {
		return
	}
	for _, result := range results {
		if result == nil || result.Name != input.Policy.RetryRestrictToTool || result.Error != nil {
			continue
		}
		input.Policy.RetryRestrictToTool = ""
		return
	}
}
