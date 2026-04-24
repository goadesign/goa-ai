package runtime

// restrict_to_tool.go centralizes the runtime semantics for persistent
// single-tool mode.
//
// Contract:
// - A tool result may promote the run into restricted-tool mode by returning a
//   RetryHint with RestrictToTool=true and Tool set.
// - Once restricted-tool mode is active, the runtime keeps that constraint for
//   the rest of the run unless the caller overwrote PolicyOverrides directly.
// - Restricted-tool runs must not fall back to tool-free planner finalization
//   before the hard deadline because their next legal move is still a tool call.

import "goa.design/goa-ai/runtime/agent/planner"

// applyToolResultPolicyHints promotes the run into persistent restricted-tool
// mode when a tool result advertises that the next legal move is a specific
// tool.
func applyToolResultPolicyHints(input *RunInput, results []*planner.ToolResult) {
	if len(results) == 0 {
		return
	}
	for _, result := range results {
		if result == nil || result.RetryHint == nil || !result.RetryHint.RestrictToTool || result.RetryHint.Tool == "" {
			continue
		}
		if input.Policy == nil {
			input.Policy = &PolicyOverrides{}
		}
		input.Policy.RestrictToTool = result.RetryHint.Tool
		return
	}
}

// shouldBypassPlannerFinalization reports whether the runtime must preserve the
// current tool-only path instead of switching into tool-free planner
// finalization.
func shouldBypassPlannerFinalization(input *RunInput) bool {
	return input != nil && input.Policy != nil && input.Policy.RestrictToTool != ""
}
