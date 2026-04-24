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
	calls, results, err := r.filterPlannerVisibleToolResults(calls, results)
	if err != nil {
		return nil, err
	}
	if len(calls) == 0 {
		return nil, nil
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

// encodePlannerToolOutputs converts planner ToolOutput values into canonical
// run-log references for plan activities.
func encodePlannerToolOutputs(outputs []*planner.ToolOutput) ([]*api.ToolOutputRef, error) {
	if len(outputs) == 0 {
		return nil, nil
	}
	out := make([]*api.ToolOutputRef, 0, len(outputs))
	for _, output := range outputs {
		if output == nil {
			return nil, fmt.Errorf("encode planner tool outputs: nil tool output")
		}
		if output.ToolCallID == "" {
			return nil, fmt.Errorf("encode planner tool outputs: missing tool_call_id")
		}
		out = append(out, &api.ToolOutputRef{ToolCallID: output.ToolCallID})
	}
	return out, nil
}
