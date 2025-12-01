package runtime

import (
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// agentContextOptions configures construction of a planner.PlannerContext.
type agentContextOptions struct {
	runtime *Runtime
	agentID agent.Ident
	runID   string
	memory  memory.Reader
	turnID  string
	events  planner.PlannerEvents
	cache   CachePolicy
}

// simplePlannerContext is a minimal implementation of planner.PlannerContext.
type simplePlannerContext struct {
	rt    *Runtime
	agent agent.Ident
	runID string
	mem   memory.Reader
	ev    planner.PlannerEvents
	cache CachePolicy
}

func newAgentContext(opts agentContextOptions) planner.PlannerContext {
	return &simplePlannerContext{
		rt:    opts.runtime,
		agent: opts.agentID,
		runID: opts.runID,
		mem:   opts.memory,
		ev:    opts.events,
		cache: opts.cache,
	}
}

func (c *simplePlannerContext) ID() agent.Ident            { return c.agent }
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
	cli := m
	// Wrap with per-turn event decorator so thinking/text/usage are captured automatically.
	if c.ev != nil {
		cli = newEventDecoratedClient(cli, c.ev)
	}
	// Apply agent cache policy so planners do not need to thread CacheOptions
	// through every model.Request construction. Explicit Request.Cache values
	// continue to take precedence over the agent policy.
	if c.cache.AfterSystem || c.cache.AfterTools {
		cli = newCacheConfiguredClient(cli, c.cache)
	}
	return cli, true
}

func (c *simplePlannerContext) AddReminder(r reminder.Reminder) {
	if c.rt == nil {
		return
	}
	c.rt.addReminder(c.runID, r)
}

func (c *simplePlannerContext) RemoveReminder(id string) {
	if c.rt == nil || id == "" {
		return
	}
	c.rt.removeReminder(c.runID, id)
}

// noopAgentState implements planner.AgentState with no persistence.
type noopAgentState struct{}

func (noopAgentState) Get(string) (any, bool) { return nil, false }
func (noopAgentState) Set(string, any)        {}
func (noopAgentState) Keys() []string         { return nil }
