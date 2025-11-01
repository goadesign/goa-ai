package runtime

import (
	"context"

	"goa.design/goa-ai/runtime/agents/hooks"
	"goa.design/goa-ai/runtime/agents/memory"
	"goa.design/goa-ai/runtime/agents/model"
	"goa.design/goa-ai/runtime/agents/planner"
	"goa.design/goa-ai/runtime/agents/telemetry"
)

// agentContextOptions configures construction of a planner.AgentContext.
type agentContextOptions struct {
	runtime *Runtime
	agentID string
	runID   string
	memory  memory.Reader
	turnID  string
}

// simpleAgentContext is a minimal implementation of planner.AgentContext
// sufficient for code generation and runtime wiring. Tests and production
// logic may replace or extend this implementation.
type simpleAgentContext struct {
	rt    *Runtime
	agent string
	runID string
	mem   memory.Reader
}

func newAgentContext(opts agentContextOptions) planner.AgentContext {
	return &simpleAgentContext{rt: opts.runtime, agent: opts.agentID, runID: opts.runID, mem: opts.memory}
}

func (c *simpleAgentContext) ID() string                 { return c.agent }
func (c *simpleAgentContext) RunID() string              { return c.runID }
func (c *simpleAgentContext) Memory() memory.Reader      { return c.mem }
func (c *simpleAgentContext) Hooks() hooks.Bus           { return c.rt.Bus }
func (c *simpleAgentContext) Logger() telemetry.Logger   { return c.rt.logger }
func (c *simpleAgentContext) Metrics() telemetry.Metrics { return c.rt.metrics }
func (c *simpleAgentContext) Tracer() telemetry.Tracer   { return c.rt.tracer }
func (c *simpleAgentContext) State() planner.AgentState  { return noopAgentState{} }
func (c *simpleAgentContext) ModelClient(id string) (model.Client, bool) {
	c.rt.mu.RLock()
	m, ok := c.rt.models[id]
	c.rt.mu.RUnlock()
	return m, ok
}
func (c *simpleAgentContext) EmitAssistantMessage(ctx context.Context, message string, structured any) {
}
func (c *simpleAgentContext) EmitPlannerNote(ctx context.Context, note string, labels map[string]string) {
}

// noopAgentState implements planner.AgentState with no persistence.
type noopAgentState struct{}

func (noopAgentState) Get(string) (any, bool) { return nil, false }
func (noopAgentState) Set(string, any)        {}
func (noopAgentState) Keys() []string         { return nil }

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
