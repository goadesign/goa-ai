package runtime

// tool_events.go contains helpers for encoding and decoding tool results into
// workflow-boundary safe envelopes.
//
// Contract:
// - planner.ToolResult contains `any` fields (Result). Crossing a
//   workflow boundary with those values allows engines/codecs (e.g. Temporal) to
//   rehydrate them as map[string]any, breaking tool codecs.
// - encodeToolEvent converts typed tool results into api.ToolEvent values that
//   only contain canonical JSON bytes plus structured metadata.

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

// encodeToolEvent converts one result using its call's selected codec. The
// returned event contains only JSON bytes and can cross a workflow boundary.
func encodeToolEvent(event *planner.ToolResult, call ToolCall, lookup toolSpecLookup) (*api.ToolEvent, error) {
	spec, ok, err := lookupCallSpec(call, lookup)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("encode tool result: no contract for %q", call.Name)
	}
	result, err := EncodeCanonicalToolResult(spec, event.Result, event.Bounds)
	if err != nil {
		return nil, fmt.Errorf("encode tool result for %s: %w", event.Name, err)
	}
	return &api.ToolEvent{
		Name: event.Name, Result: result,
		ServerData: append(rawjson.Message(nil), event.ServerData...),
		Bounds:     event.Bounds, Failure: planner.CloneToolFailure(event.Failure),
		Telemetry: event.Telemetry, ToolCallID: event.ToolCallID,
		ChildrenCount: event.ChildrenCount, RunLink: event.RunLink,
	}, nil
}

// buildPlannerToolOutputRecords converts paired step records into planner
// ToolOutput values suitable for run-loop state.
func (r *Runtime) buildPlannerToolOutputRecords(records []stepToolRecord) ([]*planner.ToolOutput, error) {
	records, err := r.filterResumeRequiredToolRecords(records)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, nil
	}
	out := make([]*planner.ToolOutput, 0, len(records))
	for _, record := range records {
		if record.callRunID == "" || record.resultRunID == "" {
			return nil, fmt.Errorf("build planner tool output: missing call or result run id for tool_call_id %s", record.call.ToolCallID)
		}
		call := record.call
		result := record.result
		output := &planner.ToolOutput{
			Registry:                   call.Registry.Clone(),
			CallRunID:                  record.callRunID,
			ResultRunID:                record.resultRunID,
			Name:                       call.Name,
			ToolCallID:                 call.ToolCallID,
			ModelToolCallID:            call.ModelToolCallID,
			ContinuationRootToolCallID: call.ContinuationRootToolCallID,
			Payload:                    append(rawjson.Message(nil), call.Payload...),
			ServerData:                 append(rawjson.Message(nil), result.ServerData...),
			Bounds:                     result.Bounds,
			Failure:                    planner.CloneToolFailure(result.Failure),
			Telemetry:                  result.Telemetry,
		}
		if result.Failure == nil {
			spec, ok, err := lookupCallSpec(call, r.toolSpec)
			if err != nil {
				return nil, err
			}
			if !ok {
				return nil, fmt.Errorf("build planner tool output: no contract for %q", call.Name)
			}
			resultJSON, err := EncodeCanonicalToolResult(spec, result.Result, result.Bounds)
			if err != nil {
				return nil, fmt.Errorf("build planner tool output result for %s: %w", call.Name, err)
			}
			output.Result = resultJSON
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
		if output.CallRunID == "" || output.ResultRunID == "" {
			return nil, fmt.Errorf("encode planner tool outputs: missing call or result run id for tool_call_id %s", output.ToolCallID)
		}
		out = append(out, &api.ToolOutputRef{
			CallRunID:   output.CallRunID,
			ResultRunID: output.ResultRunID,
			ToolCallID:  output.ToolCallID,
		})
	}
	return out, nil
}
