package runtime

import (
	"context"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

// AgentClient provides a typed interface for running a specific agent. Generated
// code produces NewClient functions that return AgentClient implementations bound
// to a particular agent's route information.
//
// SessionID is a required positional argument. Every run must belong to a session,
// which groups related runs for memory accumulation and conversation tracking. The
// runtime rejects empty or whitespace-only session IDs at the start of execution.
//
// The two methods serve different use cases:
//   - Run: Blocks until the workflow completes and returns the final output. Use
//     this for request/response patterns where the caller waits for the result.
//   - Start: Returns immediately with a workflow handle for asynchronous interaction.
//     Use this when you need to stream events, cancel the run, or wait with a
//     custom timeout.
//
// Example usage:
//
//	client := chat.NewClient(rt)
//
//	// Synchronous run
//	out, err := client.Run(ctx, "session-1", messages)
//
//	// Asynchronous run
//	handle, err := client.Start(ctx, "session-1", messages)
//	if err != nil {
//	    return err
//	}
//	// Subscribe to events, then wait
//	out, err := handle.Wait(ctx)
type AgentClient interface {
	// Run starts a workflow and blocks until it completes, returning the final
	// output containing the assistant message, tool events, and usage data.
	// Returns an error if the workflow fails or is canceled.
	Run(ctx context.Context, sessionID string, messages []*model.Message, opts ...RunOption) (*RunOutput, error)

	// Start launches the workflow asynchronously and returns a handle for
	// interaction. Callers use the handle to wait for completion, send signals,
	// or cancel execution. The handle remains valid until the workflow terminates.
	Start(ctx context.Context, sessionID string, messages []*model.Message, opts ...RunOption) (engine.WorkflowHandle, error)
}

// AgentRoute carries the minimum metadata a caller needs to construct an
// AgentClient without registering the agent locally. This enables remote
// caller processes (API gateways, CLIs, orchestrators) to start workflows
// when workers are registered in separate processes.
//
// Generated code embeds route information in NewClient functions, so most
// callers never construct AgentRoute manually. Use ClientFor or MustClientFor
// when you need to target a specific queue or workflow name dynamically.
//
// Example:
//
//	// From generated code (preferred)
//	client := chat.NewClient(rt)
//
//	// Manual route construction (advanced)
//	route := runtime.AgentRoute{
//	    ID:               agent.Ident("service.chat"),
//	    WorkflowName:     "ChatWorkflow",
//	    DefaultTaskQueue: "orchestrator.chat",
//	}
//	client := rt.MustClientFor(route)
type AgentRoute struct {
	// ID is the fully qualified agent identifier (e.g., "service.chat").
	// This must match the ID used during agent registration on workers.
	ID agent.Ident

	// WorkflowName is the workflow definition name registered by workers.
	// Generated code derives this from the agent DSL (e.g., "ChatWorkflow").
	WorkflowName string

	// DefaultTaskQueue is the queue workers listen on for this agent.
	// This can be overridden per-run using WithTaskQueue.
	DefaultTaskQueue string
}

// Client returns a client bound to the given agent identifier if registered.
// Returns ErrAgentNotFound when the agent is unknown.
func (r *Runtime) Client(id agent.Ident) (AgentClient, error) {
	if id == "" {
		return nil, ErrAgentNotFound
	}
	if _, ok := r.agentByID(id); !ok {
		return nil, ErrAgentNotFound
	}
	return &agentClient{
		r:  r,
		id: id,
	}, nil
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
	return &agentClientRoute{
		r:     r,
		route: route,
	}, nil
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
	id agent.Ident
}

func (c *agentClient) Run(
	ctx context.Context,
	sessionID string,
	messages []*model.Message,
	opts ...RunOption,
) (*RunOutput, error) {
	in := RunInput{
		AgentID:   c.id,
		SessionID: sessionID,
		Messages:  messages,
	}
	for _, o := range opts {
		if o != nil {
			o(&in)
		}
	}
	h, err := c.r.startRun(ctx, &in)
	if err != nil {
		return nil, err
	}
	return h.Wait(ctx)
}

func (c *agentClient) Start(
	ctx context.Context,
	sessionID string,
	messages []*model.Message,
	opts ...RunOption,
) (engine.WorkflowHandle, error) {
	in := RunInput{
		AgentID:   c.id,
		SessionID: sessionID,
		Messages:  messages,
	}
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

func (c *agentClientRoute) Run(
	ctx context.Context,
	sessionID string,
	messages []*model.Message,
	opts ...RunOption,
) (*RunOutput, error) {
	in := RunInput{
		AgentID:   c.route.ID,
		SessionID: sessionID,
		Messages:  messages,
	}
	for _, o := range opts {
		if o != nil {
			o(&in)
		}
	}
	h, err := c.r.startRunWithRoute(ctx, &in, c.route)
	if err != nil {
		return nil, err
	}
	return h.Wait(ctx)
}

func (c *agentClientRoute) Start(
	ctx context.Context,
	sessionID string,
	messages []*model.Message,
	opts ...RunOption,
) (engine.WorkflowHandle, error) {
	in := RunInput{
		AgentID:   c.route.ID,
		SessionID: sessionID,
		Messages:  messages,
	}
	for _, o := range opts {
		if o != nil {
			o(&in)
		}
	}
	return c.r.startRunWithRoute(ctx, &in, c.route)
}
