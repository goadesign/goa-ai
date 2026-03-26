package runtime

// tool_output_hydration.go loads planner-facing tool outputs from the canonical
// run log after compact planner activity inputs cross the workflow boundary.
//
// Contract:
// - `api.ToolOutputRef` carries only tool-call identity across the plan activity
//   boundary.
// - Canonical tool payload lives in the durable run log via
//   `ToolCallScheduledEvent`.
// - Canonical planner-visible tool outcome state lives in the durable run log
//   via `ToolResultReceivedEvent`.
// - Planner code receives fully hydrated `planner.ToolOutput` values. Missing or
//   inconsistent canonical run-log entries are invariant violations and fail
//   fast.

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
func (r *Runtime) loadPlannerToolOutputs(ctx context.Context, runID string, refs []*api.ToolOutputRef) ([]*planner.ToolOutput, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	if r.RunEventStore == nil {
		return nil, fmt.Errorf("runtime: run event store is nil")
	}

	wanted := make(map[string]struct{}, len(refs))
	for idx, ref := range refs {
		if ref == nil {
			return nil, fmt.Errorf("runtime: nil tool output ref at index %d", idx)
		}
		if ref.ToolCallID == "" {
			return nil, fmt.Errorf("runtime: tool output ref at index %d is missing tool_call_id", idx)
		}
		if _, ok := wanted[ref.ToolCallID]; ok {
			return nil, fmt.Errorf("runtime: duplicate tool output ref for tool_call_id %s", ref.ToolCallID)
		}
		wanted[ref.ToolCallID] = struct{}{}
	}

	events, err := r.loadCanonicalToolEvents(ctx, runID, wanted)
	if err != nil {
		return nil, err
	}

	outputs := make([]*planner.ToolOutput, 0, len(refs))
	for _, ref := range refs {
		output, err := plannerToolOutputFromCanonicalEvents(runID, ref.ToolCallID, events[ref.ToolCallID])
		if err != nil {
			return nil, err
		}
		outputs = append(outputs, output)
	}
	return outputs, nil
}

// plannerToolOutputFromCanonicalEvents constructs one planner ToolOutput from
// canonical scheduled/result events in the run log.
func plannerToolOutputFromCanonicalEvents(runID, toolCallID string, events *canonicalToolEvents) (*planner.ToolOutput, error) {
	if events == nil {
		return nil, fmt.Errorf("runtime: missing canonical tool history in run log (run_id=%s tool_call_id=%s)", runID, toolCallID)
	}
	if events.scheduled == nil {
		return nil, fmt.Errorf("runtime: missing canonical tool payload in run log (run_id=%s tool_call_id=%s)", runID, toolCallID)
	}
	if events.result == nil {
		return nil, fmt.Errorf("runtime: missing canonical tool result in run log (run_id=%s tool_call_id=%s tool=%s)", runID, toolCallID, events.scheduled.ToolName)
	}
	if events.result.ToolName != events.scheduled.ToolName {
		return nil, fmt.Errorf(
			"runtime: canonical tool result mismatch (run_id=%s tool_call_id=%s tool=%s event_tool=%s)",
			runID,
			toolCallID,
			events.scheduled.ToolName,
			events.result.ToolName,
		)
	}

	output := &planner.ToolOutput{
		Name:                events.scheduled.ToolName,
		ToolCallID:          toolCallID,
		Payload:             append(rawjson.Message(nil), events.scheduled.Payload...),
		ResultBytes:         events.result.ResultBytes,
		ResultOmitted:       events.result.ResultOmitted,
		ResultOmittedReason: events.result.ResultOmittedReason,
		ServerData:          append(rawjson.Message(nil), events.result.ServerData...),
		Bounds:              events.result.Bounds,
		Error:               events.result.Error,
		RetryHint:           events.result.RetryHint,
		Telemetry:           events.result.Telemetry,
	}
	if events.result.Error == nil && !output.ResultOmitted {
		if len(events.result.ResultJSON) != output.ResultBytes {
			return nil, fmt.Errorf(
				"runtime: canonical tool result size mismatch (run_id=%s tool_call_id=%s tool=%s got=%d want=%d)",
				runID,
				toolCallID,
				output.Name,
				len(events.result.ResultJSON),
				output.ResultBytes,
			)
		}
		output.Result = append(rawjson.Message(nil), events.result.ResultJSON...)
	}
	return output, nil
}

// loadCanonicalToolEvents scans the canonical run log until it finds the
// scheduled and completed events for all requested tool calls.
func (r *Runtime) loadCanonicalToolEvents(ctx context.Context, runID string, wanted map[string]struct{}) (map[string]*canonicalToolEvents, error) {
	pageSize := min(max(len(wanted), 64), 256)
	cursor := ""
	events := make(map[string]*canonicalToolEvents, len(wanted))
	complete := 0

	for complete < len(wanted) {
		page, err := r.RunEventStore.List(ctx, runID, cursor, pageSize)
		if err != nil {
			return nil, fmt.Errorf("runtime: list run log for tool hydration (run_id=%s): %w", runID, err)
		}
		if len(page.Events) == 0 {
			break
		}
		for _, event := range page.Events {
			if event == nil {
				continue
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
				entry.scheduled = decoded
				if entry.result != nil {
					complete++
				}
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
				if entry.result != nil {
					return nil, fmt.Errorf(
						"runtime: duplicate canonical tool result in run log (run_id=%s tool_call_id=%s)",
						runID,
						decoded.ToolCallID,
					)
				}
				entry.result = decoded
				if entry.scheduled != nil {
					complete++
				}
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
	decoded, err := decodeRunlogHookEvent(event)
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
	decoded, err := decodeRunlogHookEvent(event)
	if err != nil {
		return nil, err
	}
	toolEvent, ok := decoded.(*hooks.ToolResultReceivedEvent)
	if !ok {
		return nil, fmt.Errorf("runtime: run log event %s decoded as %T, want *hooks.ToolResultReceivedEvent", event.ID, decoded)
	}
	return toolEvent, nil
}

// decodeRunlogHookEvent reconstructs a hook event from a canonical run-log
// entry.
func decodeRunlogHookEvent(event *runlog.Event) (hooks.Event, error) {
	if event == nil {
		return nil, fmt.Errorf("runtime: nil run log event")
	}
	decoded, err := hooks.DecodeFromHookInput(&hooks.ActivityInput{
		Type:        event.Type,
		EventKey:    event.EventKey,
		RunID:       event.RunID,
		AgentID:     event.AgentID,
		SessionID:   event.SessionID,
		TurnID:      event.TurnID,
		TimestampMS: event.Timestamp.UnixMilli(),
		Payload:     event.Payload,
	})
	if err != nil {
		return nil, fmt.Errorf("runtime: decode run log event %s: %w", event.ID, err)
	}
	return decoded, nil
}
