// Package runtime executes agent runs and enforces runtime-owned tool-result
// contracts, including bounded-result invariants across all ingress paths.
package runtime

import (
	"fmt"

	"goa.design/goa-ai/boundedresult"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

// validateToolResultContract enforces runtime-owned invariants for one tool
// result across all ingress paths (activity, inline, child, and provided).
//
// Contract:
//   - Tool results are never nil.
//   - Result and Error are mutually exclusive.
//   - Unbounded tools never carry bounds metadata.
//   - Error results never carry bounds metadata.
//   - Successful bounded results must carry bounds.
//   - Truncated bounded results must provide continuation via next cursor or
//     refinement hint.
//   - next cursor is only valid for bounded tools with paging configured.
func validateToolResultContract(spec tools.ToolSpec, call planner.ToolRequest, tr *planner.ToolResult) error {
	if tr == nil {
		return fmt.Errorf("nil tool result for %q (%s)", call.Name, call.ToolCallID)
	}
	if tr.Result != nil && tr.Error != nil {
		return fmt.Errorf("tool %q result is invalid: error and result are both set (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	return validateToolBoundsContract(spec, call, tr.Error, tr.Bounds)
}

// validateToolBoundsContract enforces the bounds-specific subset of the runtime
// tool-result contract after the result/error shape has been validated.
func validateToolBoundsContract(spec tools.ToolSpec, call planner.ToolRequest, toolErr *planner.ToolError, bounds *agent.Bounds) error {
	if spec.Bounds == nil {
		if bounds != nil {
			return fmt.Errorf("unbounded tool %q returned unexpected bounds metadata (tool_call_id=%s)", call.Name, call.ToolCallID)
		}
		return nil
	}
	if toolErr != nil {
		if bounds != nil {
			return fmt.Errorf("bounded tool %q returned error with unexpected bounds metadata (tool_call_id=%s)", call.Name, call.ToolCallID)
		}
		return nil
	}
	if bounds == nil {
		return fmt.Errorf("bounded tool %q returned result without bounds (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if bounds.Truncated && !boundedresult.HasContinuation(bounds.NextCursor, bounds.RefinementHint) {
		return fmt.Errorf("bounded tool %q returned truncated result without next_cursor or refinement_hint (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if spec.Bounds.Paging == nil && bounds.NextCursor != nil {
		return fmt.Errorf("bounded tool %q returned next_cursor but paging is not configured (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	return nil
}
