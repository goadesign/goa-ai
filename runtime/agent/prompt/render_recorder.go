package prompt

// This file records prompt versions resolved while one caller builds a model
// request. The caller carries the recorded facts to the workflow or activity
// that consumes the rendered text; rendering itself never writes runtime state.

import (
	"cmp"
	"context"
	"maps"
	"slices"
	"sort"
	"sync"
)

type (
	// RenderRecorder collects the prompt versions resolved while one operation
	// builds model input. A recorder may be shared by concurrent render calls.
	RenderRecorder struct {
		mu     sync.Mutex
		events []RenderEvent
	}

	renderRecorderContextKey struct{}
)

// NewRenderRecorder creates an empty prompt render recorder.
func NewRenderRecorder() *RenderRecorder {
	return &RenderRecorder{}
}

// WithRenderRecorder returns a context that records successful prompt renders
// in recorder. The caller later passes Events to the operation that consumes
// the rendered text.
func WithRenderRecorder(ctx context.Context, recorder *RenderRecorder) context.Context {
	if recorder == nil {
		panic("prompt: render recorder is nil")
	}
	return context.WithValue(ctx, renderRecorderContextKey{}, recorder)
}

// Events returns an owned copy of the recorded prompt render facts in stable
// identity order. Callers use the result after all renders have completed so
// concurrent completion order cannot change workflow start identity.
func (r *RenderRecorder) Events() []RenderEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	events := make([]RenderEvent, len(r.events))
	for index, event := range r.events {
		events[index] = cloneRenderEvent(event)
	}
	sort.Slice(events, func(left, right int) bool {
		return compareRenderEvents(events[left], events[right]) < 0
	})
	return events
}

// record appends one successful render while preserving the scope values that
// were used for override selection.
func (r *RenderRecorder) record(event RenderEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, cloneRenderEvent(event))
}

// renderRecorderFromContext returns the recorder installed for this operation.
func renderRecorderFromContext(ctx context.Context) *RenderRecorder {
	if ctx == nil {
		return nil
	}
	recorder, _ := ctx.Value(renderRecorderContextKey{}).(*RenderRecorder)
	return recorder
}

// cloneRenderEvent copies the label map so later caller edits cannot change a
// recorded prompt scope.
func cloneRenderEvent(event RenderEvent) RenderEvent {
	event.Scope = cloneScope(event.Scope)
	return event
}

// compareRenderEvents orders every value that identifies one resolved prompt.
func compareRenderEvents(left, right RenderEvent) int {
	if compared := cmp.Compare(left.PromptID.String(), right.PromptID.String()); compared != 0 {
		return compared
	}
	if compared := cmp.Compare(left.Version, right.Version); compared != 0 {
		return compared
	}
	if compared := cmp.Compare(left.Scope.SessionID, right.Scope.SessionID); compared != 0 {
		return compared
	}
	return compareLabels(left.Scope.Labels, right.Scope.Labels)
}

// compareLabels orders label maps by sorted key and value pairs.
func compareLabels(left, right map[string]string) int {
	leftKeys := slices.Sorted(maps.Keys(left))
	rightKeys := slices.Sorted(maps.Keys(right))
	for index := 0; index < min(len(leftKeys), len(rightKeys)); index++ {
		leftKey := leftKeys[index]
		rightKey := rightKeys[index]
		if compared := cmp.Compare(leftKey, rightKey); compared != 0 {
			return compared
		}
		if compared := cmp.Compare(left[leftKey], right[rightKey]); compared != 0 {
			return compared
		}
	}
	return len(leftKeys) - len(rightKeys)
}
