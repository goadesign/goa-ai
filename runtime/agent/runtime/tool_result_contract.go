// Package runtime executes agent runs and enforces runtime-owned tool-result
// contracts, including bounded-result invariants across all ingress paths.
package runtime

import (
	"errors"
	"fmt"
	"reflect"

	"goa.design/goa-ai/boundedresult"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

// sanitizeAndValidateExecutorToolResult takes ownership of an executor-authored
// failure before checking it. Executors own the failure classification, error,
// recovery action, and field issues; correction input and examples are
// workflow-owned, so this boundary discards those bytes from its private clone.
// Successful results continue through materialization before their complete
// result and bounds contract is checked.
func sanitizeAndValidateExecutorToolResult(spec tools.ToolSpec, call ToolCall, tr *planner.ToolResult) error {
	if tr.Failure == nil {
		return nil
	}
	tr.Failure = planner.CloneToolFailure(tr.Failure)
	tr.Failure.Recovery.PriorInput = nil
	tr.Failure.Recovery.ExampleJSON = nil
	return validateToolResultContract(spec, call, tr)
}

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
func validateToolResultContract(spec tools.ToolSpec, call ToolCall, tr *planner.ToolResult) error {
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
	}
	return validateToolBoundsContract(spec, call, tr.Failure != nil, tr.Bounds)
}

// decodeSuccessfulToolResult checks the JSON returned for one successful tool.
// Tools with a result type must return bytes accepted by their generated codec;
// tools without a result type must return no bytes.
func decodeSuccessfulToolResult(spec tools.ToolSpec, result rawjson.Message) (any, error) {
	if spec.Result.Codec.FromJSON == nil {
		if len(result) > 0 {
			return nil, errors.New("tool does not define a result but contains one")
		}
		return nil, nil
	}
	if len(result) == 0 {
		return nil, errors.New("tool result is missing")
	}
	decoded, err := spec.Result.Codec.FromJSON(result)
	if err != nil {
		return nil, fmt.Errorf("tool result does not satisfy its generated contract: %w", err)
	}
	if decoded == nil {
		return nil, errors.New("tool result decoded to nil")
	}
	value := reflect.ValueOf(decoded)
	kind := value.Kind()
	nilResult := kind == reflect.UnsafePointer && value.IsZero()
	nilable := kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface ||
		kind == reflect.Map || kind == reflect.Ptr || kind == reflect.Slice
	if nilResult || nilable && value.IsNil() {
		return nil, errors.New("tool result decoded to nil")
	}
	return decoded, nil
}

// validateToolClarificationContract enforces the runtime-owned user-question
// contract after the durable tool-result contract has been validated.
func validateToolClarificationContract(call ToolCall, tr *planner.ToolResult, clarification *ToolClarification) error {
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
func validateToolBoundsContract(spec tools.ToolSpec, call ToolCall, failed bool, bounds *agent.Bounds) error {
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
