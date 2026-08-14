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
//   - Result and Failure are mutually exclusive.
//   - Unbounded tools never carry bounds metadata.
//   - Failed results never carry bounds metadata or server-only data.
//   - Successful bounded results must carry bounds.
//   - Truncated bounded results must provide continuation via next cursor or
//     refinement hint.
//   - A present next cursor must be non-empty.
//   - next cursor is only valid for bounded tools with paging configured.
func validateToolResultContract(spec tools.ToolSpec, call planner.ToolRequest, tr *planner.ToolResult) error {
	if tr == nil {
		return fmt.Errorf("nil tool result for %q (%s)", call.Name, call.ToolCallID)
	}
	if tr.Result != nil && tr.Failure != nil {
		return fmt.Errorf("tool %q result is invalid: failure and result are both set (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if tr.Failure != nil {
		if len(tr.ServerData) > 0 {
			return fmt.Errorf("tool %q result is invalid: failure and server data are both set (tool_call_id=%s)", call.Name, call.ToolCallID)
		}
		if err := planner.ValidateToolFailure(tr.Failure); err != nil {
			return fmt.Errorf("tool %q result is invalid: %w (tool_call_id=%s)", call.Name, err, call.ToolCallID)
		}
		if tr.Failure.Recovery.Action == planner.RecoveryCorrectCall {
			if err := validatePlannerToolPayload(tr.Failure.Recovery.PriorInput); err != nil {
				return fmt.Errorf(
					"tool %q result is invalid: correct-call recovery prior input is invalid: %w (tool_call_id=%s)",
					call.Name,
					err,
					call.ToolCallID,
				)
			}
		}
	}
	return validateToolBoundsContract(spec, call, tr.Failure != nil, tr.Bounds)
}

// validateToolClarificationContract enforces the runtime-owned user-question
// contract after the durable tool-result contract has been validated.
func validateToolClarificationContract(call planner.ToolRequest, tr *planner.ToolResult, clarification *ToolClarification) error {
	if clarification == nil {
		return nil
	}
	if tr == nil {
		return fmt.Errorf("tool %q clarification is invalid: missing tool result (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if tr.Failure != nil {
		return fmt.Errorf("tool %q clarification is invalid: clarification and failure are both set (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if clarification.ID == "" {
		return fmt.Errorf("tool %q clarification is invalid: id is required (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if clarification.Question == "" {
		return fmt.Errorf("tool %q clarification is invalid: question is required (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	return nil
}

// validateToolBoundsContract enforces the bounds-specific subset of the runtime
// tool-result contract after the result/error shape has been validated.
func validateToolBoundsContract(spec tools.ToolSpec, call planner.ToolRequest, failed bool, bounds *agent.Bounds) error {
	if spec.Bounds == nil {
		if bounds != nil {
			return fmt.Errorf("unbounded tool %q returned unexpected bounds metadata (tool_call_id=%s)", call.Name, call.ToolCallID)
		}
		return nil
	}
	if failed {
		if bounds != nil {
			return fmt.Errorf("bounded tool %q returned error with unexpected bounds metadata (tool_call_id=%s)", call.Name, call.ToolCallID)
		}
		return nil
	}
	if bounds == nil {
		return fmt.Errorf("bounded tool %q returned result without bounds (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if bounds.NextCursor != nil && *bounds.NextCursor == "" {
		return fmt.Errorf("bounded tool %q returned an empty next_cursor (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if bounds.Truncated && !boundedresult.HasContinuation(bounds.NextCursor, bounds.RefinementHint) {
		return fmt.Errorf("bounded tool %q returned truncated result without next_cursor or refinement_hint (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if spec.Bounds.Paging == nil && bounds.NextCursor != nil {
		return fmt.Errorf("bounded tool %q returned next_cursor but paging is not configured (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if bounds.NextCursor != nil && !bounds.Truncated {
		return fmt.Errorf("bounded tool %q returned next_cursor without truncation (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	return nil
}
