package runtime

import (
	"context"
	"sync"

	"goa.design/goa-ai/agents/runtime/hooks"
	"goa.design/goa-ai/agents/runtime/memory"
	"goa.design/goa-ai/agents/runtime/model"
	"goa.design/goa-ai/agents/runtime/planner"
	"goa.design/goa-ai/agents/runtime/telemetry"
)

type agentContextOptions struct {
	runtime *Runtime
	agentID string
	runID   string
	memory  memory.Reader
	turnID  string
}

type agentContext struct {
	runtime *Runtime
	agentID string
	runID   string
	turnID  string
	memory  memory.Reader
	hooks   hooks.Bus
	logger  telemetry.Logger
	metrics telemetry.Metrics
	tracer  telemetry.Tracer
	state   *agentState
}

// newAgentContext constructs an agentContext with noop telemetry substituted where needed.
func newAgentContext(opts agentContextOptions) *agentContext {
	rt := opts.runtime
	logger := rt.logger
	if logger == nil {
		logger = telemetry.NoopLogger{}
	}
	metrics := rt.metrics
	if metrics == nil {
		metrics = telemetry.NoopMetrics{}
	}
	tracer := rt.tracer
	if tracer == nil {
		tracer = telemetry.NoopTracer{}
	}
	reader := opts.memory
	if reader == nil {
		reader = emptyMemoryReader{}
	}
	return &agentContext{
		runtime: rt,
		agentID: opts.agentID,
		runID:   opts.runID,
		turnID:  opts.turnID,
		memory:  reader,
		hooks:   rt.Bus,
		logger:  logger,
		metrics: metrics,
		tracer:  tracer,
		state:   newAgentState(),
	}
}

func (a *agentContext) ID() string { return a.agentID }

func (a *agentContext) RunID() string { return a.runID }

func (a *agentContext) Memory() memory.Reader { return a.memory }

func (a *agentContext) Hooks() hooks.Bus {
	if a.hooks == nil {
		return noopHooks{}
	}
	return a.hooks
}

func (a *agentContext) ModelClient(id string) (model.Client, bool) {
	a.runtime.mu.RLock()
	client, ok := a.runtime.models[id]
	a.runtime.mu.RUnlock()
	return client, ok
}

func (a *agentContext) EmitAssistantMessage(ctx context.Context, message string, structured any) {
	if message == "" || a.runtime == nil {
		return
	}
	a.emit(ctx, hooks.NewAssistantMessageEvent(a.runID, a.agentID, message, structured))
}

func (a *agentContext) EmitPlannerNote(ctx context.Context, note string, labels map[string]string) {
	if note == "" || a.runtime == nil {
		return
	}
	a.emit(ctx, hooks.NewPlannerNoteEvent(a.runID, a.agentID, note, labels))
}

func (a *agentContext) emit(ctx context.Context, evt hooks.Event) {
	if setter, ok := evt.(interface{ SetTurn(string, int) }); ok && a.turnID != "" {
		setter.SetTurn(a.turnID, 0)
	}
	a.runtime.publishHook(ctx, evt, nil)
}

func (a *agentContext) Logger() telemetry.Logger { return a.logger }

func (a *agentContext) Metrics() telemetry.Metrics { return a.metrics }

func (a *agentContext) Tracer() telemetry.Tracer { return a.tracer }

func (a *agentContext) State() planner.AgentState { return a.state }

// agentState provides a simple in-memory key/value store for planner state.
type agentState struct {
	mu   sync.RWMutex
	data map[string]any
}

// newAgentState constructs an empty agent state store.
func newAgentState() *agentState {
	return &agentState{data: make(map[string]any)}
}

func (s *agentState) Get(key string) (any, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

func (s *agentState) Set(key string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

func (s *agentState) Keys() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	keys := make([]string, 0, len(s.data))
	for k := range s.data {
		keys = append(keys, k)
	}
	return keys
}

// Memory Reader helpers

type memorySnapshotReader struct {
	events []memory.Event
}

// newMemoryReader wraps a snapshot's events in a Reader for planner access.
func newMemoryReader(events []memory.Event) memory.Reader {
	if len(events) == 0 {
		return emptyMemoryReader{}
	}
	copied := make([]memory.Event, len(events))
	copy(copied, events)
	return &memorySnapshotReader{events: copied}
}

func (m *memorySnapshotReader) Events() []memory.Event {
	copied := make([]memory.Event, len(m.events))
	copy(copied, m.events)
	return copied
}

func (m *memorySnapshotReader) FilterByType(t memory.EventType) []memory.Event {
	var filtered []memory.Event
	for _, evt := range m.events {
		if evt.Type == t {
			filtered = append(filtered, evt)
		}
	}
	return filtered
}

func (m *memorySnapshotReader) Latest(t memory.EventType) (memory.Event, bool) {
	for i := len(m.events) - 1; i >= 0; i-- {
		if m.events[i].Type == t {
			return m.events[i], true
		}
	}
	return memory.Event{}, false
}

type emptyMemoryReader struct{}

func (emptyMemoryReader) Events() []memory.Event                       { return nil }
func (emptyMemoryReader) FilterByType(memory.EventType) []memory.Event { return nil }
func (emptyMemoryReader) Latest(memory.EventType) (memory.Event, bool) { return memory.Event{}, false }

// No-op hooks bus and subscription

type noopHooks struct{}

func (noopHooks) Publish(context.Context, hooks.Event) error { return nil }
func (noopHooks) Register(hooks.Subscriber) (hooks.Subscription, error) {
	return noopSubscription{}, nil
}

type noopSubscription struct{}

func (noopSubscription) Close() error { return nil }
