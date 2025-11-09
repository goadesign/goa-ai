package runtime

import (
	"context"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
)

// AgentClient provides a narrow interface to run a specific agent.
type AgentClient interface {
	Run(ctx context.Context, messages []*planner.AgentMessage, opts ...RunOption) (*RunOutput, error)
	Start(ctx context.Context, messages []*planner.AgentMessage, opts ...RunOption) (engine.WorkflowHandle, error)
}

// AgentRoute carries the minimum metadata a caller needs to construct an
// AgentClient without registering the agent locally. This enables remote
// caller processes to start workflows when workers are registered elsewhere.
type AgentRoute struct {
	// ID is the fully qualified agent identifier.
	ID agent.Ident
	// WorkflowName is the workflow definition name registered by workers.
	WorkflowName string
	// DefaultTaskQueue is the queue workers listen on for this agent.
	DefaultTaskQueue string
}

// Client returns a client bound to the given agent identifier if registered.
// Returns ErrAgentNotFound when the agent is unknown.
func (r *Runtime) Client(id agent.Ident) (AgentClient, error) {
	if id == "" {
		return nil, ErrAgentNotFound
	}
	if _, ok := r.agentByID(string(id)); !ok { // confirm presence
		return nil, ErrAgentNotFound
	}
	return &agentClient{r: r, id: string(id)}, nil
}

// MustClient returns a client bound to the given agent identifier and panics
// if the agent is not registered.
func (r *Runtime) MustClient(id agent.Ident) AgentClient {
	c, err := r.Client(id)
	if err != nil {
		panic(err)
	}
	return c
}

// ClientFor returns a client bound to the provided metadata. Use this in
// caller processes that do not register agents locally. The returned client
// uses WorkflowName and DefaultTaskQueue when starting runs unless overridden
// by WithTaskQueue.
func (r *Runtime) ClientFor(route AgentRoute) (AgentClient, error) {
	if route.ID == "" || route.WorkflowName == "" {
		return nil, ErrAgentNotFound
	}
	return &agentClientRoute{r: r, route: route}, nil
}

// MustClientFor is like ClientFor but panics on error.
func (r *Runtime) MustClientFor(route AgentRoute) AgentClient {
	c, err := r.ClientFor(route)
	if err != nil {
		panic(err)
	}
	return c
}

type agentClient struct {
	r  *Runtime
	id string
}

func (c *agentClient) Run(ctx context.Context, messages []*planner.AgentMessage, opts ...RunOption) (*RunOutput, error) {
	in := RunInput{AgentID: c.id, Messages: messages}
	for _, o := range opts {
		if o != nil {
			o(&in)
		}
	}
	h, err := c.r.startRun(ctx, &in)
	if err != nil {
		return nil, err
	}
	var out *RunOutput
	if err := h.Wait(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *agentClient) Start(ctx context.Context, messages []*planner.AgentMessage, opts ...RunOption) (engine.WorkflowHandle, error) {
	in := RunInput{AgentID: c.id, Messages: messages}
	for _, o := range opts {
		if o != nil {
			o(&in)
		}
	}
	return c.r.startRun(ctx, &in)
}

type agentClientRoute struct {
	r     *Runtime
	route AgentRoute
}

func (c *agentClientRoute) Run(ctx context.Context, messages []*planner.AgentMessage, opts ...RunOption) (*RunOutput, error) {
	in := RunInput{AgentID: string(c.route.ID), Messages: messages}
	for _, o := range opts {
		if o != nil {
			o(&in)
		}
	}
	h, err := c.r.startRunWithRoute(ctx, &in, c.route)
	if err != nil {
		return nil, err
	}
	var out *RunOutput
	if err := h.Wait(ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *agentClientRoute) Start(ctx context.Context, messages []*planner.AgentMessage, opts ...RunOption) (engine.WorkflowHandle, error) {
	in := RunInput{AgentID: string(c.route.ID), Messages: messages}
	for _, o := range opts {
		if o != nil {
			o(&in)
		}
	}
	return c.r.startRunWithRoute(ctx, &in, c.route)
}
