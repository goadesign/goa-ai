// Package runtime loads planner-facing tool outputs from the canonical run log
// after compact planner activity inputs cross the workflow boundary.
//
// Contract:
//   - `api.ToolOutputRef` carries the exact recording run and tool-call identity
//     across the plan activity boundary.
//   - Canonical tool payload lives in the durable run log via
//     `ToolCallScheduledEvent`.
//   - Canonical planner-visible tool outcome state lives in the durable run log
//     via `ToolResultReceivedEvent`.
//   - Planner code receives fully hydrated `planner.ToolOutput` values. Missing or
//     inconsistent canonical run-log entries are invariant violations and fail
//     fast.
package runtime

import (
	"context"
	"fmt"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
)

type canonicalToolEvents struct {
	scheduled *hooks.ToolCallScheduledEvent
	result    *hooks.ToolResultReceivedEvent
}

// loadPlannerToolOutputs hydrates canonical planner-facing tool outputs from the
// run log using workflow-safe tool-output references.
func (r *Runtime) loadPlannerToolOutputs(ctx context.Context, refs []*api.ToolOutputRef) ([]*planner.ToolOutput, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if r.RunEventStore == nil {
		return nil, fmt.Errorf("runtime: run event store is nil")
	}

	wantedByRun := make(map[string]map[string]struct{})
	seen := make(map[string]struct{}, len(refs))
	for idx, ref := range refs {
		if ref == nil {
			return nil, fmt.Errorf("runtime: nil tool output ref at index %d", idx)
		}
		if ref.ToolCallID == "" {
			return nil, fmt.Errorf("runtime: tool output ref at index %d is missing tool_call_id", idx)
		}
		if ref.CallRunID == "" || ref.ResultRunID == "" {
			return nil, fmt.Errorf("runtime: tool output ref at index %d is missing call_run_id or result_run_id", idx)
		}
		key := ref.CallRunID + "\x00" + ref.ResultRunID + "\x00" + ref.ToolCallID
		if _, ok := seen[key]; ok {
			return nil, fmt.Errorf("runtime: duplicate tool output ref for call_run_id=%s result_run_id=%s tool_call_id=%s", ref.CallRunID, ref.ResultRunID, ref.ToolCallID)
		}
		seen[key] = struct{}{}
		for _, sourceRunID := range []string{ref.CallRunID, ref.ResultRunID} {
			wanted := wantedByRun[sourceRunID]
			if wanted == nil {
				wanted = make(map[string]struct{})
				wantedByRun[sourceRunID] = wanted
			}
			wanted[ref.ToolCallID] = struct{}{}
		}
	}

	eventsByRun := make(map[string]map[string]*canonicalToolEvents, len(wantedByRun))
	for sourceRunID, wanted := range wantedByRun {
		events, err := r.loadCanonicalToolEvents(ctx, sourceRunID, wanted)
		if err != nil {
			return nil, err
		}
		eventsByRun[sourceRunID] = events
	}

	outputs := make([]*planner.ToolOutput, 0, len(refs))
	for _, ref := range refs {
		callEvents := eventsByRun[ref.CallRunID][ref.ToolCallID]
		output, err := r.plannerToolOutputFromCanonicalEvents(
			ref.CallRunID,
			ref.ResultRunID,
			ref.ToolCallID,
			callEvents,
			eventsByRun[ref.ResultRunID][ref.ToolCallID],
		)
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

// plannerToolOutputFromCanonicalEvents constructs one planner ToolOutput from
// canonical scheduled/result events in the run log.
func (r *Runtime) plannerToolOutputFromCanonicalEvents(callRunID, resultRunID, toolCallID string, callEvents, resultEvents *canonicalToolEvents) (*planner.ToolOutput, error) {
	if callEvents == nil {
		return nil, fmt.Errorf("runtime: missing canonical tool history in run log (run_id=%s tool_call_id=%s)", callRunID, toolCallID)
	}
	if callEvents.scheduled == nil {
		return nil, fmt.Errorf("runtime: missing canonical tool payload in run log (run_id=%s tool_call_id=%s)", callRunID, toolCallID)
	}
	if callEvents.scheduled.ToolCallID != toolCallID {
		return nil, fmt.Errorf(
			"runtime: canonical tool schedule call mismatch (run_id=%s tool_call_id=%s event_tool_call_id=%s)",
			callRunID,
			toolCallID,
			callEvents.scheduled.ToolCallID,
		)
	}
	if resultEvents == nil || resultEvents.result == nil {
		return nil, fmt.Errorf("runtime: missing canonical tool result in run log (run_id=%s tool_call_id=%s tool=%s)", resultRunID, toolCallID, callEvents.scheduled.ToolName)
	}
	if err := hooks.ValidateToolResultCorrelation(callEvents.scheduled, resultEvents.result); err != nil {
		return nil, fmt.Errorf(
			"runtime: canonical tool result identity mismatch (call_run_id=%s result_run_id=%s tool_call_id=%s): %w",
			callRunID,
			resultRunID,
			toolCallID,
			err,
		)
	}

	output := &planner.ToolOutput{
		CallRunID:                  callRunID,
		ResultRunID:                resultRunID,
		Name:                       callEvents.scheduled.ToolName,
		ToolCallID:                 toolCallID,
		ModelToolCallID:            callEvents.scheduled.ModelToolCallID,
		ContinuationRootToolCallID: callEvents.scheduled.ContinuationRootToolCallID,
		Payload:                    append(rawjson.Message(nil), callEvents.scheduled.Payload...),
		ServerData:                 append(rawjson.Message(nil), resultEvents.result.ServerData...),
		Bounds:                     resultEvents.result.Bounds,
		Failure:                    resultEvents.result.Failure,
		Telemetry:                  resultEvents.result.Telemetry,
	}
	resultJSON := resultEvents.result.ResultJSON
	if len(resultJSON) != resultEvents.result.ResultBytes {
		return nil, fmt.Errorf(
			"runtime: canonical tool result size mismatch (run_id=%s tool_call_id=%s tool=%s got=%d want=%d)",
			resultRunID,
			toolCallID,
			output.Name,
			len(resultJSON),
			resultEvents.result.ResultBytes,
		)
	}
	if resultEvents.result.Failure != nil {
		if len(resultJSON) > 0 {
			return nil, fmt.Errorf(
				"runtime: canonical failed tool result contains result JSON (run_id=%s tool_call_id=%s tool=%s)",
				resultRunID,
				toolCallID,
				output.Name,
			)
		}
		return output, nil
	}
	spec, ok := r.toolSpec(output.Name)
	if !ok {
		return nil, fmt.Errorf("runtime: canonical tool history references unregistered tool %q", output.Name)
	}
	if _, err := decodeSuccessfulToolResult(spec, resultJSON); err != nil {
		return nil, fmt.Errorf(
			"runtime: canonical successful tool result is invalid (run_id=%s tool_call_id=%s tool=%s): %w",
			resultRunID,
			toolCallID,
			output.Name,
			err,
		)
	}
	output.Result = append(rawjson.Message(nil), resultJSON...)
	return output, nil
}

// loadCanonicalToolEvents scans one run's canonical events oldest-first. A
// result recorded in its scheduling run must follow the matching schedule. A
// continuation result may appear without a local schedule because CallRunID
// identifies the earlier run that owns it.
func (r *Runtime) loadCanonicalToolEvents(ctx context.Context, runID string, wanted map[string]struct{}) (map[string]*canonicalToolEvents, error) {
	pageSize := min(max(len(wanted), 64), 256)
	cursor := ""
	events := make(map[string]*canonicalToolEvents, len(wanted))

	for {
		page, err := r.RunEventStore.List(ctx, runID, cursor, pageSize)
		if err != nil {
			return nil, fmt.Errorf("runtime: list run log for tool hydration (run_id=%s): %w", runID, err)
		}
		if len(page.Events) == 0 {
			break
		}
		for index, event := range page.Events {
			if event == nil {
				return nil, fmt.Errorf(
					"runtime: nil event from run log during tool hydration (run_id=%s page_cursor=%q index=%d)",
					runID,
					cursor,
					index,
				)
			}
			if event.Type == hooks.ToolCallScheduled {
				decoded, err := decodeToolCallScheduledRunlogEvent(event)
				if err != nil {
					return nil, err
				}
				if _, ok := wanted[decoded.ToolCallID]; !ok {
					continue
				}
				entry := canonicalEntry(events, decoded.ToolCallID)
				if entry.scheduled != nil {
					return nil, fmt.Errorf(
						"runtime: duplicate canonical tool payload in run log (run_id=%s tool_call_id=%s)",
						runID,
						decoded.ToolCallID,
					)
				}
				if entry.result != nil {
					if err := hooks.ValidateToolResultPlacement(runID, decoded, entry.result); err != nil {
						return nil, fmt.Errorf(
							"runtime: canonical tool result placement is invalid "+
								"(run_id=%s tool_call_id=%s tool=%s): %w",
							runID,
							decoded.ToolCallID,
							decoded.ToolName,
							err,
						)
					}
				}
				entry.scheduled = decoded
				continue
			}
			if event.Type == hooks.ToolResultReceived {
				decoded, err := decodeToolResultRunlogEvent(event)
				if err != nil {
					return nil, err
				}
				if _, ok := wanted[decoded.ToolCallID]; !ok {
					continue
				}
				entry := canonicalEntry(events, decoded.ToolCallID)
				if err := hooks.ValidateToolResultPlacement(runID, entry.scheduled, decoded); err != nil {
					return nil, fmt.Errorf(
						"runtime: canonical tool result placement is invalid "+
							"(run_id=%s tool_call_id=%s tool=%s): %w",
						runID,
						decoded.ToolCallID,
						decoded.ToolName,
						err,
					)
				}
				if entry.result != nil {
					return nil, fmt.Errorf(
						"runtime: duplicate canonical tool result in run log (run_id=%s tool_call_id=%s)",
						runID,
						decoded.ToolCallID,
					)
				}
				entry.result = decoded
			}
		}
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return events, nil
}

// canonicalEntry returns the mutable canonical event accumulator for one tool call.
func canonicalEntry(events map[string]*canonicalToolEvents, toolCallID string) *canonicalToolEvents {
	entry, ok := events[toolCallID]
	if ok {
		return entry
	}
	entry = &canonicalToolEvents{}
	events[toolCallID] = entry
	return entry
}

// decodeToolCallScheduledRunlogEvent reconstructs a ToolCallScheduledEvent from
// a canonical run-log event payload.
func decodeToolCallScheduledRunlogEvent(event *runlog.Event) (*hooks.ToolCallScheduledEvent, error) {
	decoded, err := hooks.DecodeRunlogEvent(event)
	if err != nil {
		return nil, err
	}
	toolEvent, ok := decoded.(*hooks.ToolCallScheduledEvent)
	if !ok {
		return nil, fmt.Errorf("runtime: run log event %s decoded as %T, want *hooks.ToolCallScheduledEvent", event.ID, decoded)
	}
	return toolEvent, nil
}

// decodeToolResultRunlogEvent reconstructs a ToolResultReceivedEvent from a
// canonical run-log event payload.
func decodeToolResultRunlogEvent(event *runlog.Event) (*hooks.ToolResultReceivedEvent, error) {
	decoded, err := hooks.DecodeRunlogEvent(event)
	if err != nil {
		return nil, err
	}
	toolEvent, ok := decoded.(*hooks.ToolResultReceivedEvent)
	if !ok {
		return nil, fmt.Errorf("runtime: run log event %s decoded as %T, want *hooks.ToolResultReceivedEvent", event.ID, decoded)
	}
	return toolEvent, nil
}
