package runtime

// tool_events.go contains helpers for encoding and decoding tool results into
// workflow-boundary safe envelopes.
//
// Contract:
// - planner.ToolResult contains `any` fields (Result). Crossing a
//   workflow boundary with those values allows engines/codecs (e.g. Temporal) to
//   rehydrate them as map[string]any, breaking tool codecs.
// - encodeToolEvents converts typed tool results into api.ToolEvent values that
//   only contain canonical JSON bytes plus structured metadata.

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

const maxPlanToolResultBytes = 64 * 1024
const resultOmittedReasonWorkflowBudget = "workflow_budget"

// encodeToolEvents converts typed in-memory tool results into workflow-boundary safe
// envelopes.
//
// Contract:
//   - Input values are trusted to be runtime-produced tool results; nil entries are
//     a bug.
//   - Tool result values are encoded via the registered tool result codec and stored
//     as canonical JSON bytes on api.ToolEvent.Result.
func (r *Runtime) encodeToolEvents(ctx context.Context, events []*planner.ToolResult) ([]*api.ToolEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]*api.ToolEvent, 0, len(events))
	for _, ev := range events {
		result, err := r.marshalToolValue(ctx, ev.Name, ev.Result, ev.Bounds)
		if err != nil {
			return nil, fmt.Errorf("encode tool result for %s: %w", ev.Name, err)
		}
		out = append(out, &api.ToolEvent{
			Name:          ev.Name,
			Result:        rawjson.Message(result),
			ResultBytes:   len(result),
			ResultOmitted: false,
			ServerData:    append(rawjson.Message(nil), ev.ServerData...),
			Bounds:        ev.Bounds,
			Error:         ev.Error,
			RetryHint:     ev.RetryHint,
			Telemetry:     ev.Telemetry,
			ToolCallID:    ev.ToolCallID,
			ChildrenCount: ev.ChildrenCount,
			RunLink:       ev.RunLink,
		})
	}
	return out, nil
}

// encodeToolEventsForPlanning converts tool results into workflow-boundary safe envelopes
// suitable for PlanStart/PlanResume activity inputs.
//
// The planner resume inputs must remain small: the workflow loop is the control plane
// and should not shuttle observer-side rendering payloads or large values across workflow/activity
// boundaries. Full tool results are persisted via hooks and returned in
// RunOutput.ToolEvents for callers that need them.
//
// Contract:
//   - ServerData is always omitted.
//   - Results larger than maxPlanToolResultBytes are omitted (Result=nil) and must be
//     consumed via the provider transcript (tool_result parts) or via out-of-band
//     persistence.
func (r *Runtime) encodeToolEventsForPlanning(ctx context.Context, events []*planner.ToolResult) ([]*api.ToolEvent, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]*api.ToolEvent, 0, len(events))
	for _, ev := range events {
		result, err := r.marshalToolValue(ctx, ev.Name, ev.Result, ev.Bounds)
		if err != nil {
			return nil, fmt.Errorf("encode tool result for %s: %w", ev.Name, err)
		}
		resultBytes := len(result)
		omitted := false
		omittedReason := ""
		if len(result) > maxPlanToolResultBytes {
			omitted = true
			omittedReason = resultOmittedReasonWorkflowBudget
			result = nil
		}
		out = append(out, &api.ToolEvent{
			Name:                ev.Name,
			Result:              rawjson.Message(result),
			ResultBytes:         resultBytes,
			ResultOmitted:       omitted,
			ResultOmittedReason: omittedReason,
			ServerData:          nil,
			Bounds:              ev.Bounds,
			Error:               ev.Error,
			RetryHint:           ev.RetryHint,
			Telemetry:           ev.Telemetry,
			ToolCallID:          ev.ToolCallID,
			ChildrenCount:       ev.ChildrenCount,
			RunLink:             ev.RunLink,
		})
	}
	return out, nil
}

// buildPlannerToolOutputs converts executed tool calls plus their results into
// planner ToolOutput values suitable for run-loop state.
//
// Contract:
//   - Calls are emitted in canonical call order, keyed by ToolCallID.
//   - Results may arrive in a different order (for example, externally provided
//     await results); they are matched by ToolCallID, not slice position.
//   - If a result was already omitted at an upstream truthful boundary, the
//     original omission metadata is preserved without re-encoding synthetic bytes.
func (r *Runtime) buildPlannerToolOutputs(ctx context.Context, calls []planner.ToolRequest, results []*planner.ToolResult) ([]*planner.ToolOutput, error) {
	if len(calls) == 0 {
		return nil, nil
	}
	if len(calls) != len(results) {
		return nil, fmt.Errorf("encode tool outputs: calls/results length mismatch (%d != %d)", len(calls), len(results))
	}
	resultsByToolCallID := make(map[string]*planner.ToolResult, len(results))
	for _, result := range results {
		if result == nil {
			return nil, fmt.Errorf("encode tool outputs: nil tool result")
		}
		if result.ToolCallID == "" {
			return nil, fmt.Errorf("encode tool outputs: missing result tool_call_id for %s", result.Name)
		}
		if _, exists := resultsByToolCallID[result.ToolCallID]; exists {
			return nil, fmt.Errorf("encode tool outputs: duplicate result tool_call_id %s", result.ToolCallID)
		}
		resultsByToolCallID[result.ToolCallID] = result
	}
	out := make([]*planner.ToolOutput, 0, len(calls))
	for _, call := range calls {
		if call.ToolCallID == "" {
			return nil, fmt.Errorf("build planner tool outputs: missing call tool_call_id for %s", call.Name)
		}
		result, ok := resultsByToolCallID[call.ToolCallID]
		if !ok {
			return nil, fmt.Errorf("build planner tool outputs: missing result for tool_call_id %s", call.ToolCallID)
		}
		if result.Name != "" && result.Name != call.Name {
			return nil, fmt.Errorf("build planner tool outputs: result name %s does not match call %s", result.Name, call.Name)
		}
		output := &planner.ToolOutput{
			Name:                call.Name,
			ToolCallID:          call.ToolCallID,
			Payload:             append(rawjson.Message(nil), call.Payload...),
			ResultBytes:         result.ResultBytes,
			ResultOmitted:       result.ResultOmitted,
			ResultOmittedReason: result.ResultOmittedReason,
			ServerData:          append(rawjson.Message(nil), result.ServerData...),
			Bounds:              result.Bounds,
			Error:               result.Error,
			RetryHint:           result.RetryHint,
			Telemetry:           result.Telemetry,
		}
		if !result.ResultOmitted {
			resultJSON, err := r.marshalToolValue(ctx, call.Name, result.Result, result.Bounds)
			if err != nil {
				return nil, fmt.Errorf("build planner tool output result for %s: %w", call.Name, err)
			}
			output.Result = rawjson.Message(resultJSON)
			output.ResultBytes = len(resultJSON)
		}
		out = append(out, output)
	}
	return out, nil
}

// decodeToolOutputs converts workflow-boundary tool output envelopes back into
// planner ToolOutput values.
func (r *Runtime) decodeToolOutputs(events []*api.ToolCallOutput) ([]*planner.ToolOutput, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]*planner.ToolOutput, 0, len(events))
	for _, ev := range events {
		if ev == nil {
			return nil, fmt.Errorf("CRITICAL: nil tool output entry")
		}
		out = append(out, &planner.ToolOutput{
			Name:                ev.Name,
			ToolCallID:          ev.ToolCallID,
			Payload:             append(rawjson.Message(nil), ev.Payload...),
			Result:              append(rawjson.Message(nil), ev.Result...),
			ResultBytes:         ev.ResultBytes,
			ResultOmitted:       ev.ResultOmitted,
			ResultOmittedReason: ev.ResultOmittedReason,
			ServerData:          append(rawjson.Message(nil), ev.ServerData...),
			Bounds:              ev.Bounds,
			Error:               ev.Error,
			RetryHint:           ev.RetryHint,
			Telemetry:           ev.Telemetry,
		})
	}
	return out, nil
}

// encodePlannerToolOutputs clones planner ToolOutput values into the
// workflow-boundary safe api.ToolCallOutput shape used by plan activities.
func encodePlannerToolOutputs(outputs []*planner.ToolOutput) ([]*api.ToolCallOutput, error) {
	if len(outputs) == 0 {
		return nil, nil
	}
	out := make([]*api.ToolCallOutput, 0, len(outputs))
	for _, output := range outputs {
		if output == nil {
			return nil, fmt.Errorf("encode planner tool outputs: nil tool output")
		}
		out = append(out, &api.ToolCallOutput{
			Name:                output.Name,
			ToolCallID:          output.ToolCallID,
			Payload:             append(rawjson.Message(nil), output.Payload...),
			Result:              append(rawjson.Message(nil), output.Result...),
			ResultBytes:         output.ResultBytes,
			ResultOmitted:       output.ResultOmitted,
			ResultOmittedReason: output.ResultOmittedReason,
			ServerData:          append(rawjson.Message(nil), output.ServerData...),
			Bounds:              output.Bounds,
			Error:               output.Error,
			RetryHint:           output.RetryHint,
			Telemetry:           output.Telemetry,
		})
	}
	return out, nil
}
