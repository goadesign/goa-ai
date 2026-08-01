// Package runtime executes agent runs and enforces runtime-owned tool-result
// contracts, including bounded-result invariants across all ingress paths.
package runtime

import (
	"fmt"

	"goa.design/goa-ai/boundedresult"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/toolerrors"
	"goa.design/goa-ai/runtime/agent/tools"
)

// validateToolResultContract enforces runtime-owned invariants for one tool
// result across all ingress paths (activity, inline, child, and provided).
//
// Contract:
//   - Tool results are never nil.
//   - Result and Failure are mutually exclusive.
//   - Unbounded tools never carry bounds metadata.
//   - Failed results never carry bounds metadata.
//   - Successful bounded results must carry bounds.
//   - Truncated bounded results must provide continuation via next cursor or
//     refinement hint.
//   - provider cursors and model-visible continuations exist as a pair and are
//     only valid for bounded tools with paging configured.
func validateToolResultContract(spec tools.ToolSpec, call planner.ToolRequest, tr *planner.ToolResult) error {
	if tr == nil {
		return fmt.Errorf("nil tool result for %q (%s)", call.Name, call.ToolCallID)
	}
	if tr.Result != nil && tr.Failure != nil {
		return fmt.Errorf("tool %q result is invalid: failure and result are both set (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if tr.Failure != nil {
		if err := validateToolFailure(tr.Failure); err != nil {
			return fmt.Errorf("tool %q result is invalid: %w (tool_call_id=%s)", call.Name, err, call.ToolCallID)
		}
	}
	return validateToolBoundsContract(spec, call, tr.Failure != nil, tr.Bounds)
}

// validateToolPauseContract enforces the runtime-owned pause contract for one
// executed tool result after the durable tool-result contract has been validated.
func validateToolPauseContract(call planner.ToolRequest, tr *planner.ToolResult, pause *ToolPause) error {
	if pause == nil {
		return nil
	}
	if tr == nil {
		return fmt.Errorf("tool %q pause is invalid: missing tool result (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if tr.Failure != nil {
		return fmt.Errorf("tool %q pause is invalid: pause and failure are both set (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if pause.Clarification == nil {
		return fmt.Errorf("tool %q pause is invalid: missing clarification payload (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if pause.Clarification.ID == "" {
		return fmt.Errorf("tool %q pause is invalid: clarification id is required (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if pause.Clarification.Question == "" {
		return fmt.Errorf("tool %q pause is invalid: clarification question is required (tool_call_id=%s)", call.Name, call.ToolCallID)
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
	if bounds.Truncated && !boundedresult.HasContinuation(bounds.NextCursor, bounds.RefinementHint) {
		return fmt.Errorf("bounded tool %q returned truncated result without next_cursor or refinement_hint (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if spec.Bounds.Paging == nil && bounds.NextCursor != nil {
		return fmt.Errorf("bounded tool %q returned next_cursor but paging is not configured (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if bounds.NextCursor != nil && !bounds.Truncated {
		return fmt.Errorf("bounded tool %q returned next_cursor without truncation (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	if (bounds.NextCursor == nil) != (bounds.Continuation == nil) {
		return fmt.Errorf("bounded tool %q returned an incomplete continuation pair (tool_call_id=%s)", call.Name, call.ToolCallID)
	}
	return nil
}

// validateToolFailure enforces the runtime-owned classification and transition
// vocabulary at every tool-result ingress boundary.
func validateToolFailure(failure *planner.ToolFailure) error {
	if err := toolerrors.Validate(failure.Error); err != nil {
		return fmt.Errorf("failure error is invalid: %w", err)
	}
	switch failure.Kind {
	case planner.FailureInvalidCall,
		planner.FailureDomainRejection,
		planner.FailureUnavailable,
		planner.FailureRateLimited,
		planner.FailureTimeout,
		planner.FailureMalformedResult,
		planner.FailureInternal:
	default:
		return fmt.Errorf("unknown failure kind %q", failure.Kind)
	}
	switch failure.Recovery.Action {
	case planner.RecoveryCorrectCall:
		if failure.Kind != planner.FailureInvalidCall &&
			failure.Kind != planner.FailureDomainRejection {
			return fmt.Errorf("failure kind %q cannot require same-tool correction", failure.Kind)
		}
		if err := validatePlannerToolPayload(failure.Recovery.PriorInput); err != nil {
			return fmt.Errorf("correct-call recovery prior input is invalid: %w", err)
		}
		if len(failure.Recovery.ExampleJSON) > 0 {
			if err := validatePlannerToolPayload(failure.Recovery.ExampleJSON); err != nil {
				return fmt.Errorf("correct-call recovery example is invalid: %w", err)
			}
		}
		if len(failure.Recovery.Issues) > 0 {
			if err := tools.ValidateFieldIssues(failure.Recovery.Issues); err != nil {
				return fmt.Errorf("correct-call recovery issues are invalid: %w", err)
			}
		}
	case planner.RecoveryReplan, planner.RecoveryFinish:
		if len(failure.Recovery.Issues) > 0 ||
			len(failure.Recovery.PriorInput) > 0 ||
			len(failure.Recovery.ExampleJSON) > 0 {
			return fmt.Errorf("recovery %q cannot carry correction data", failure.Recovery.Action)
		}
	default:
		return fmt.Errorf("unknown recovery action %q", failure.Recovery.Action)
	}
	return nil
}
