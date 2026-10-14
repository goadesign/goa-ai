// Package runtime uses the owning store's run and event APIs. A child run's
// tool calls can refer to the call in its parent run. The saved parent schedule
// and child link must agree before that outside reference is accepted; missing
// local schedules and arbitrary outside references remain invalid.
package runtime

import (
	"context"
	"fmt"
	"reflect"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// parentToolCallForChild reads ordered parent events and verifies the schedule
// that admitted this child. It returns that exact call ID, including after the
// parent completes, or rejects a missing, finished or mismatched admission.
func parentToolCallForChild(events []*runlog.Event, child session.RunMeta) (string, error) {
	if _, err := genrunidentity.EncodeRunSubject(&child.Subject); err != nil {
		return "", storage.NewContractError(fmt.Errorf("snapshot child subject: %w", err))
	}
	childAgent, ok := child.Subject.Executor.AsAgent()
	if !ok {
		return "", storage.NewContractError(fmt.Errorf("snapshot child link requires an Agent subject"))
	}
	active := make(map[string]*hooks.ToolCallScheduledEvent)
	for _, event := range events {
		switch event.Type {
		case hooks.ToolCallScheduled, hooks.ToolResultReceived, hooks.ChildRunLinked:
			decoded, err := hooks.DecodeRunlogEvent(event)
			if err != nil {
				return "", err
			}
			switch record := decoded.(type) {
			case *hooks.ToolCallScheduledEvent:
				active[record.ToolCallID] = record
			case *hooks.ToolResultReceivedEvent:
				if schedule := active[record.ToolCallID]; schedule != nil && record.CallRunID == schedule.RunID() {
					delete(active, record.ToolCallID)
				}
			case *hooks.ChildRunLinkedEvent:
				if record.ChildRunID != child.RunID || event.RunID != child.ParentRunID {
					continue
				}
				schedule := active[record.ToolCallID]
				if schedule == nil || schedule.ToolName != record.ToolName || string(record.ChildAgentID) != string(childAgent) || !reflect.DeepEqual(record.Subject(), schedule.Subject()) || event.SessionID != child.SessionID {
					return "", fmt.Errorf("child run %q has no matching active parent schedule", child.RunID)
				}
				return record.ToolCallID, nil
			}
		}
	}
	return "", fmt.Errorf("child run %q is not linked by parent run %q", child.RunID, child.ParentRunID)
}

// loadRunEvents reads one run through bounded owner pages and rejects repeated
// cursors. Suspended runs are immutable, so their complete pages contain all
// execution events even when the conversation ended at an earlier position.
func (r *Runtime) loadRunEvents(ctx context.Context, runID string) ([]*runlog.Event, error) {
	const pageSize = 512

	var (
		cursor = ""
		events []*runlog.Event
	)
	seen := make(map[string]struct{})
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		page, err := r.Store.ListRunRecords(ctx, runID, cursor, pageSize)
		if err != nil {
			return nil, err
		}
		for _, event := range page.Events {
			if event == nil || event.RunID != runID {
				return nil, fmt.Errorf("run %q has invalid stored event at index %d", runID, len(events))
			}
			events = append(events, event)
		}
		if page.NextCursor == "" {
			break
		}
		if _, repeated := seen[page.NextCursor]; repeated || page.NextCursor == cursor {
			return nil, storage.NewContractError(fmt.Errorf("snapshot record page made no progress"))
		}
		seen[page.NextCursor] = struct{}{}
		cursor = page.NextCursor
	}
	return events, nil
}

// snapshotParentToolCall reads the exact saved parent for a child. A root has
// no outside parent call; a child must have been linked while its parent's
// matching schedule was active, with the selected tool and child Agent intact.
func (r *Runtime) snapshotParentToolCall(ctx context.Context, child session.RunMeta) (string, error) {
	if child.ParentRunID == "" {
		return "", nil
	}
	parent, err := r.Store.LoadRun(ctx, child.ParentRunID)
	if err != nil {
		return "", err
	}
	if parent.RunID != child.ParentRunID || parent.SessionID != child.SessionID {
		return "", storage.NewContractError(storage.ErrRunRecordOwnerMismatch)
	}
	inherited, err := r.snapshotInheritedEvents(ctx, parent)
	if err != nil {
		return "", err
	}
	events, err := r.loadRunEvents(ctx, child.ParentRunID)
	if err != nil {
		return "", err
	}
	return parentToolCallForChild(append(inherited, events...), child)
}
