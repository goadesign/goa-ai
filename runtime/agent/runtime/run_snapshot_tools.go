// Package runtime replays saved tool schedules, updates, and results for snapshots.
// The same checks apply to a run and its inherited continuation history, so a
// continued result must still name the original scheduling run and tool.
package runtime

import (
	"fmt"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
)

type (
	// snapshotToolState keeps the original schedule beside each unfinished
	// call. Completed IDs remain available to reject later updates or results.
	snapshotToolState struct {
		parentToolCallID string
		calls            map[string]*run.ToolCallSnapshot
		active           map[string]*run.ToolCallSnapshot
		schedules        map[string]*hooks.ToolCallScheduledEvent
		completed        map[string]struct{}
	}
)

// apply checks one saved tool event against the calls seen before it and
// updates the snapshot state, rejecting missing schedules or changed identity.
func (state *snapshotToolState) apply(e *runlog.Event, decoded hooks.Event) error {
	switch p := decoded.(type) {
	case *hooks.ToolCallScheduledEvent:
		if _, ok := state.schedules[p.ToolCallID]; ok {
			return fmt.Errorf("duplicate tool schedule for call %q", p.ToolCallID)
		}
		tc := &run.ToolCallSnapshot{
			ToolCallID:            p.ToolCallID,
			ToolName:              p.ToolName,
			ParentToolCallID:      p.ParentToolCallID,
			ScheduledAt:           e.Timestamp,
			ExpectedChildrenTotal: p.ExpectedChildrenTotal,
		}
		state.calls[p.ToolCallID] = tc
		state.active[p.ToolCallID] = tc
		state.schedules[p.ToolCallID] = p

		if p.ParentToolCallID != "" && p.ParentToolCallID != state.parentToolCallID {
			parent, ok := state.active[p.ParentToolCallID]
			if !ok {
				return fmt.Errorf(
					"tool schedule for call %q requires active parent schedule %q",
					p.ToolCallID,
					p.ParentToolCallID,
				)
			}
			parent.ObservedChildrenTotal++
		}

	case *hooks.ToolCallUpdatedEvent:
		if _, ok := state.completed[p.ToolCallID]; ok {
			return fmt.Errorf("tool update follows completion for call %q", p.ToolCallID)
		}
		tc, ok := state.active[p.ToolCallID]
		if !ok {
			return fmt.Errorf("tool update requires active schedule for call %q", p.ToolCallID)
		}
		tc.ExpectedChildrenTotal = p.ExpectedChildrenTotal

	case *hooks.ToolResultReceivedEvent:
		if _, ok := state.completed[p.ToolCallID]; ok {
			return fmt.Errorf("tool result follows completion for call %q", p.ToolCallID)
		}
		tc := state.active[p.ToolCallID]
		schedule := state.schedules[p.ToolCallID]
		var localSchedule *hooks.ToolCallScheduledEvent
		if schedule != nil && schedule.RunID() == e.RunID {
			localSchedule = schedule
		}
		if err := hooks.ValidateToolResultPlacement(e.RunID, localSchedule, p); err != nil {
			return fmt.Errorf("tool result placement is invalid for call %q: %w", p.ToolCallID, err)
		}
		if err := hooks.ValidateToolResultCorrelation(schedule, p); err != nil {
			return fmt.Errorf("tool result correlation is invalid for call %q: %w", p.ToolCallID, err)
		}
		delete(state.active, p.ToolCallID)
		tc.ToolName = p.ToolName
		tc.ParentToolCallID = p.ParentToolCallID
		tc.CompletedAt = e.Timestamp
		tc.Duration = p.Duration
		if p.Failure != nil {
			tc.ErrorSummary = p.Failure.Error.Message
		}
		state.completed[p.ToolCallID] = struct{}{}
	}
	return nil
}
