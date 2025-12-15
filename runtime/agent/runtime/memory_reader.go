package runtime

import (
	"context"

	"goa.design/goa-ai/runtime/agent/memory"
)

// memoryReader returns a read-only view of the persisted memory for the given run.
//
// When a Memory store is configured, the reader is backed by a snapshot loaded from
// the store and copied so planners cannot mutate shared state. When no Memory store
// is configured, the reader is empty.
func (r *Runtime) memoryReader(ctx context.Context, agentID, runID string) (memory.Reader, error) {
	if r.Memory == nil {
		return emptyMemoryReader{}, nil
	}
	snapshot, err := r.Memory.LoadRun(ctx, agentID, runID)
	if err != nil {
		return nil, err
	}
	return newMemoryReader(snapshot.Events), nil
}

type emptyMemoryReader struct{}

func (emptyMemoryReader) Events() []memory.Event {
	return nil
}

func (emptyMemoryReader) FilterByType(memory.EventType) []memory.Event {
	return nil
}

func (emptyMemoryReader) Latest(memory.EventType) (memory.Event, bool) {
	return memory.Event{}, false
}

type memorySnapshotReader struct {
	events []memory.Event
}

// newMemoryReader constructs a snapshot-backed reader from the given events.
// The returned reader always owns its slice; callers may mutate their input slice
// after calling this function without affecting the reader.
func newMemoryReader(events []memory.Event) memory.Reader {
	if len(events) == 0 {
		return emptyMemoryReader{}
	}
	cp := make([]memory.Event, len(events))
	copy(cp, events)
	return &memorySnapshotReader{
		events: cp,
	}
}

func (r *memorySnapshotReader) Events() []memory.Event {
	if len(r.events) == 0 {
		return nil
	}
	cp := make([]memory.Event, len(r.events))
	copy(cp, r.events)
	return cp
}

func (r *memorySnapshotReader) FilterByType(t memory.EventType) []memory.Event {
	if len(r.events) == 0 {
		return nil
	}
	out := make([]memory.Event, 0, len(r.events))
	for _, evt := range r.events {
		if evt.Type == t {
			out = append(out, evt)
		}
	}
	return out
}

func (r *memorySnapshotReader) Latest(t memory.EventType) (memory.Event, bool) {
	if len(r.events) == 0 {
		return memory.Event{}, false
	}
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Type == t {
			return r.events[i], true
		}
	}
	return memory.Event{}, false
}
