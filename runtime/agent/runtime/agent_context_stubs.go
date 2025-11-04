package runtime

import (
    "context"

    "goa.design/goa-ai/runtime/agent/hooks"
    "goa.design/goa-ai/runtime/agent/memory"
    "goa.design/goa-ai/runtime/agent/model"
    "goa.design/goa-ai/runtime/agent/planner"
    "goa.design/goa-ai/runtime/agent/telemetry"
)

// agentContextOptions configures construction of a planner.PlannerContext.
type agentContextOptions struct {
	runtime *Runtime
	agentID string
	runID   string
	memory  memory.Reader
	turnID  string
}

// simplePlannerContext is a minimal implementation of planner.PlannerContext.
type simplePlannerContext struct {
    rt    *Runtime
    agent string
    runID string
    mem   memory.Reader
}

func newAgentContext(opts agentContextOptions) planner.PlannerContext {
    return &simplePlannerContext{rt: opts.runtime, agent: opts.agentID, runID: opts.runID, mem: opts.memory}
}

func (c *simplePlannerContext) ID() string                 { return c.agent }
func (c *simplePlannerContext) RunID() string              { return c.runID }
func (c *simplePlannerContext) Memory() memory.Reader      { return c.mem }
func (c *simplePlannerContext) Logger() telemetry.Logger   { return c.rt.logger }
func (c *simplePlannerContext) Metrics() telemetry.Metrics { return c.rt.metrics }
func (c *simplePlannerContext) Tracer() telemetry.Tracer   { return c.rt.tracer }
func (c *simplePlannerContext) State() planner.AgentState  { return noopAgentState{} }
func (c *simplePlannerContext) ModelClient(id string) (model.Client, bool) {
    c.rt.mu.RLock()
    m, ok := c.rt.models[id]
    c.rt.mu.RUnlock()
    return m, ok
}

// runtimePlannerEvents implements planner.PlannerEvents by publishing to the runtime bus.
type runtimePlannerEvents struct {
    rt    *Runtime
    agent string
    runID string
}

func newPlannerEvents(rt *Runtime, agentID, runID string) planner.PlannerEvents {
    return &runtimePlannerEvents{rt: rt, agent: agentID, runID: runID}
}

func (e *runtimePlannerEvents) AssistantChunk(ctx context.Context, text string) {
    if e == nil || e.rt == nil || e.rt.Bus == nil || text == "" {
        return
    }
    _ = e.rt.Bus.Publish(ctx, hooks.NewAssistantMessageEvent(e.runID, e.agent, text, nil))
}

func (e *runtimePlannerEvents) PlannerThought(ctx context.Context, note string, labels map[string]string) {
    if e == nil || e.rt == nil || e.rt.Bus == nil || note == "" {
        return
    }
    _ = e.rt.Bus.Publish(ctx, hooks.NewPlannerNoteEvent(e.runID, e.agent, note, labels))
}

func (e *runtimePlannerEvents) UsageDelta(ctx context.Context, usage model.TokenUsage) {
    if e == nil || e.rt == nil || e.rt.Bus == nil {
        return
    }
    _ = e.rt.Bus.Publish(ctx, hooks.NewUsageEvent(e.runID, e.agent, usage.InputTokens, usage.OutputTokens, usage.TotalTokens))
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
