package runtime

import (
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
	events  planner.PlannerEvents
}

// simplePlannerContext is a minimal implementation of planner.PlannerContext.
type simplePlannerContext struct {
	rt    *Runtime
	agent string
	runID string
	mem   memory.Reader
	ev    planner.PlannerEvents
}

func newAgentContext(opts agentContextOptions) planner.PlannerContext {
	return &simplePlannerContext{
		rt:    opts.runtime,
		agent: opts.agentID,
		runID: opts.runID,
		mem:   opts.memory,
		ev:    opts.events,
	}
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
	if !ok || m == nil {
		return nil, false
	}
	// Wrap with per-turn event decorator so thinking/text/usage are captured automatically.
	if c.ev != nil {
		return newEventDecoratedClient(m, c.ev), true
	}
	return m, true
}

// noopAgentState implements planner.AgentState with no persistence.
type noopAgentState struct{}

func (noopAgentState) Get(string) (any, bool) { return nil, false }
func (noopAgentState) Set(string, any)        {}
func (noopAgentState) Keys() []string         { return nil }


