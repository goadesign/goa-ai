package runtime

import (
	"context"
	"slices"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// agentContextOptions configures construction of a planner.PlannerContext.
type agentContextOptions struct {
	runtime             *Runtime
	agentID             agent.Ident
	runID               string
	memory              memory.Reader
	sessionID           string
	labels              map[string]string
	policy              compiledToolPolicy
	turnID              string
	events              planner.PlannerEvents
	invocations         modelInvocationSink
	cache               CachePolicy
	history             HistoryPolicy
	continuationActions []continuationAction
	unavailableTools    []tools.Ident
	advertisedSpecs     []tools.ToolSpec
	historyMessages     []*model.Message
	historyContext      *api.HistoryContext
}

// simplePlannerContext is a minimal implementation of planner.PlannerContext.
type simplePlannerContext struct {
	rt                  *Runtime
	agent               agent.Ident
	runID               string
	turnID              string
	mem                 memory.Reader
	sessionID           string
	labels              map[string]string
	policy              compiledToolPolicy
	ev                  planner.PlannerEvents
	invocations         modelInvocationSink
	cache               CachePolicy
	history             HistoryPolicy
	continuationActions []continuationAction
	unavailableTools    []tools.Ident
	advertisedSpecs     []tools.ToolSpec
	historyMessages     []*model.Message
	historyContext      *api.HistoryContext
}

func newAgentContext(opts agentContextOptions) planner.PlannerContext {
	var advertisedSpecs []tools.ToolSpec
	if opts.advertisedSpecs != nil {
		advertisedSpecs = cloneToolSpecs(opts.advertisedSpecs)
	}
	return &simplePlannerContext{
		rt:                  opts.runtime,
		agent:               opts.agentID,
		runID:               opts.runID,
		turnID:              opts.turnID,
		mem:                 opts.memory,
		sessionID:           opts.sessionID,
		labels:              cloneLabels(opts.labels),
		policy:              opts.policy,
		ev:                  opts.events,
		invocations:         opts.invocations,
		cache:               opts.cache,
		history:             opts.history,
		continuationActions: opts.continuationActions,
		unavailableTools:    opts.unavailableTools,
		advertisedSpecs:     advertisedSpecs,
		historyMessages:     opts.historyMessages,
		historyContext:      opts.historyContext,
	}
}

// conversationID returns the GenAI conversation identifier for a run. Sessioned
// runs use the stable session ID; one-shot runs use their run ID because the run
// is the complete user/model exchange.
func conversationID(sessionID, runID string) string {
	if sessionID != "" {
		return sessionID
	}
	return runID
}

func (c *simplePlannerContext) ID() agent.Ident            { return c.agent }
func (c *simplePlannerContext) RunID() string              { return c.runID }
func (c *simplePlannerContext) Memory() memory.Reader      { return c.mem }
func (c *simplePlannerContext) Logger() telemetry.Logger   { return c.rt.logger }
func (c *simplePlannerContext) Metrics() telemetry.Metrics { return c.rt.metrics }
func (c *simplePlannerContext) Tracer() telemetry.Tracer   { return c.rt.tracer }
func (c *simplePlannerContext) State() planner.AgentState  { return noopAgentState{} }
func (c *simplePlannerContext) AdvertisedToolDefinitions() []*model.ToolDefinition {
	specs := c.advertisedSpecs
	if specs == nil {
		specs = c.rt.ToolSpecsForAgent(c.agent)
	}
	specs = cloneToolSpecs(specs)
	visible := specs[:0]
	for _, spec := range specs {
		if slices.Contains(c.unavailableTools, spec.Name) {
			continue
		}
		if isDedicatedContinuationSpec(spec) {
			continue
		}
		visible = append(visible, spec)
	}
	definitions := c.rt.advertisedToolDefinitions(visible, c.policy)
	for _, action := range c.continuationActions {
		if slices.Contains(c.unavailableTools, action.spec.Name) ||
			!c.policy.allowsTool(action.spec.Name, toolPolicyFactsFromSpec(action.spec)) {
			continue
		}
		definition := c.rt.advertisedToolDefinitions(
			[]tools.ToolSpec{action.spec},
			compiledToolPolicy{},
		)[0]
		definition.Name = action.modelName.String()
		definition.Description = action.description
		definition.NoArguments = true
		definitions = append(definitions, definition)
	}
	return definitions
}
func (c *simplePlannerContext) ModelClient(id string) (model.Client, bool) {
	return c.configuredModelClient(id, false)
}

func (c *simplePlannerContext) PlannerModelClient(id string) (planner.PlannerModelClient, bool) {
	cli, ok := c.configuredModelClient(id, true)
	if !ok {
		return nil, false
	}
	return newPlannerModelClient(cli), true
}

// configuredModelClient returns the runtime-managed raw model client for the
// current planner turn, with transport/policy wrappers applied but without
// PlannerEvents decoration.
func (c *simplePlannerContext) configuredModelClient(id string, designated bool) (model.Client, bool) {
	c.rt.mu.RLock()
	m, ok := c.rt.models[id]
	c.rt.mu.RUnlock()
	if !ok || m == nil {
		return nil, false
	}
	// Cache defaults and history selection must use the same complete request
	// that the destination provider receives. Client retrieval does no counting
	// or summarization; preparation runs only on Complete or Stream.
	cli := newRequestConfiguredClient(m, c.cache, c.history, c.agent, c.historyMessages, c.historyContext)
	// Check and save each provider response before tracing or planner code can
	// read it. This also keeps concurrent model calls separate.
	if designated {
		cli = newDesignatedModelInvocationClient(cli, c.invocations)
	} else {
		cli = newModelInvocationClient(cli, c.invocations)
	}
	// Trace only responses that passed the model response contract. Streaming
	// traces still cover the complete accepted stream lifetime.
	cli = newTracedClient(cli, c.rt.tracer, c.rt.logger, id, telemetry.GenAIContext{
		ConversationID: conversationID(c.sessionID, c.runID),
		AgentID:        string(c.agent),
		AgentName:      string(c.agent),
	}, c.rt.captureGenAIMessages)
	return cli, true
}

// RenderPrompt resolves and renders a prompt for the current run scope.
func (c *simplePlannerContext) RenderPrompt(ctx context.Context, id prompt.Ident, data any) (*prompt.PromptContent, error) {
	scope := prompt.Scope{
		SessionID: c.sessionID,
		Labels:    cloneLabels(c.labels),
	}
	events := c.ev.(*runtimePlannerEvents)
	renderContext := prompt.WithRenderRecorder(ctx, events.prompts)
	return c.rt.PromptRegistry.Render(renderContext, id, scope, data)
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
