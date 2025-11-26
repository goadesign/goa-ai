package runtime

import "goa.design/goa-ai/runtime/agent/memory"

// emptyMemoryReader implements memory.Reader over an empty snapshot.
type emptyMemoryReader struct{}

func (emptyMemoryReader) Events() []memory.Event                         { return nil }
func (emptyMemoryReader) FilterByType(t memory.EventType) []memory.Event { return nil }
func (emptyMemoryReader) Latest(t memory.EventType) (memory.Event, bool) {
	return memory.Event{}, false
}

// newMemoryReader creates a memory.Reader over a static list of events.
type sliceMemoryReader struct{ events []memory.Event }

func newMemoryReader(events []memory.Event) memory.Reader { return sliceMemoryReader{events: events} }
func (r sliceMemoryReader) Events() []memory.Event {
	return append([]memory.Event(nil), r.events...)
}
func (r sliceMemoryReader) FilterByType(t memory.EventType) []memory.Event {
	if len(r.events) == 0 {
		return nil
}
	out := make([]memory.Event, 0, len(r.events))
	for _, e := range r.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}
func (r sliceMemoryReader) Latest(t memory.EventType) (memory.Event, bool) {
	for i := len(r.events) - 1; i >= 0; i-- {
		if r.events[i].Type == t {
			return r.events[i], true
		}
	}
	return memory.Event{}, false
}


