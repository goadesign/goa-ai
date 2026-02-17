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
// - decodeToolEvents converts api.ToolEvent values back into planner.ToolResult
//   values by decoding Result bytes via the registered tool codec.

import (
	"context"
	"encoding/json"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
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
		result, err := r.marshalToolValue(ctx, ev.Name, ev.Result, false)
		if err != nil {
			return nil, fmt.Errorf("encode tool result for %s: %w", ev.Name, err)
		}
		out = append(out, &api.ToolEvent{
			Name:          ev.Name,
			Result:        result,
			ResultBytes:   len(result),
			ResultOmitted: false,
			ServerData:    append(json.RawMessage(nil), ev.ServerData...),
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
		result, err := r.marshalToolValue(ctx, ev.Name, ev.Result, false)
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
			Result:              result,
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

// decodeToolEvents converts workflow-boundary tool event envelopes back into typed
// planner tool results.
//
// Contract:
//   - Result bytes are decoded via the registered tool result codec.
func (r *Runtime) decodeToolEvents(ctx context.Context, events []*api.ToolEvent) ([]*planner.ToolResult, error) {
	if len(events) == 0 {
		return nil, nil
	}
	out := make([]*planner.ToolResult, 0, len(events))
	for _, ev := range events {
		if ev == nil {
			return nil, fmt.Errorf("CRITICAL: nil tool event entry")
		}
		var decoded any
		if hasNonNullJSON(ev.Result) && ev.Error == nil {
			val, err := r.unmarshalToolValue(ctx, ev.Name, ev.Result, false)
			if err != nil {
				return nil, fmt.Errorf("decode tool result for %s: %w", ev.Name, err)
			}
			decoded = val
		}
		tr := &planner.ToolResult{
			Name:                ev.Name,
			Result:              decoded,
			ResultBytes:         ev.ResultBytes,
			ResultOmitted:       ev.ResultOmitted,
			ResultOmittedReason: ev.ResultOmittedReason,
			ServerData:          append(json.RawMessage(nil), ev.ServerData...),
			Bounds:              ev.Bounds,
			Error:               ev.Error,
			RetryHint:           ev.RetryHint,
			Telemetry:           ev.Telemetry,
			ToolCallID:          ev.ToolCallID,
			ChildrenCount:       ev.ChildrenCount,
			RunLink:             ev.RunLink,
		}
		out = append(out, tr)
	}
	return out, nil
}
