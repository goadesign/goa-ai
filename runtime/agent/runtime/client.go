// Package runtime provides typed clients for sessionful and one-shot agent runs.
package runtime

import (
	"context"
	"errors"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// AgentClient is the high-level execution surface for one agent.
	//
	// Contract:
	// - Run and Start are sessionful APIs: callers must provide a concrete
	//   session ID that already exists in the runtime SessionStore.
	// - StartOneShot and OneShotRun are sessionless: callers provide no session
	//   ID and the runtime persists only canonical run-log events for
	//   introspection by RunID.
	// - Generated code typically returns AgentClient implementations via NewClient
	//   helpers bound to one agent route.
	AgentClient interface {
		// Run starts one sessionful workflow and blocks until completion.
		//
		// Run is request/response oriented: it is equivalent to Start followed by
		// handle.Wait on the returned workflow handle.
		Run(ctx context.Context, sessionID string, messages []*model.Message, opts ...RunOption) (*RunOutput, error)

		// Start starts one sessionful workflow and returns immediately with a
		// workflow handle for asynchronous coordination.
		//
		// Callers use the handle to wait or cancel. Start does not block
		// on workflow completion.
		Start(ctx context.Context, sessionID string, messages []*model.Message, opts ...RunOption) (engine.WorkflowHandle, error)

		// Continue loads one exact predecessor suspension, starts a new sessionful
		// workflow with its first pending response, and blocks until completion.
		Continue(ctx context.Context, sessionID, predecessorRunID, runID, turnID string, response *api.PendingInputResponse) (*RunOutput, error)

		// StartContinuation starts a new sessionful workflow from one exact
		// suspension and returns immediately. RunID and TurnID are required caller-
		// owned identities for the new workflow and conversational turn.
		StartContinuation(ctx context.Context, sessionID, predecessorRunID, runID, turnID string, response *api.PendingInputResponse) (engine.WorkflowHandle, error)

		// StartOneShot starts one sessionless workflow and returns immediately with
		// a workflow handle for asynchronous coordination.
		//
		// One-shot runs do not participate in session lifecycle and do not emit
		// session-scoped stream events. They still append canonical lifecycle and
		// prompt/tool events to the run log.
		//
		// Callers use this when they need durable background work that can be
		// observed by RunID before the request returns. StartOneShot does not block
		// on workflow completion.
		StartOneShot(ctx context.Context, messages []*model.Message, opts ...RunOption) (engine.WorkflowHandle, error)

		// OneShotRun starts one sessionless workflow and blocks until completion.
		//
		// OneShotRun is request/response oriented: it is equivalent to
		// StartOneShot followed by handle.Wait on the returned workflow handle.
		OneShotRun(ctx context.Context, messages []*model.Message, opts ...RunOption) (*RunOutput, error)
	}

	// AgentRoute carries the minimum metadata needed to run an agent when the
	// caller process does not register that agent locally.
	//
	// Generated NewClient helpers embed this route metadata so most callers do
	// not construct AgentRoute directly. Use Runtime.ClientFor when routing is
	// dynamic (for example, gateway and orchestration processes).
	AgentRoute struct {
		// ID is the canonical agent identifier.
		// It must match the identifier used by worker-side registration.
		ID agent.Ident

		// WorkflowName is the engine-registered workflow definition name.
		// It must match the workflow name workers register with the engine.
		WorkflowName string

		// DefaultTaskQueue is the default queue workers use for this agent.
		// Per-run overrides may replace this queue via WithTaskQueue.
		DefaultTaskQueue string
	}

	// agentClient binds execution to one locally-registered agent.
	agentClient struct {
		r  *Runtime
		id agent.Ident
	}

	// agentClientRoute binds execution to one externally supplied route.
	agentClientRoute struct {
		r     *Runtime
		route AgentRoute
	}
)

// Client returns a typed client for one locally registered agent.
//
// Returns ErrAgentNotFound when id is empty or not present in runtime
// registration.
func (r *Runtime) Client(id agent.Ident) (AgentClient, error) {
	if id == "" {
		return nil, ErrAgentNotFound
	}
	if _, ok := r.agentByID(id); !ok {
		return nil, ErrAgentNotFound
	}
	return &agentClient{r: r, id: id}, nil
}

// MustClient is like Client but panics when the agent is unknown.
//
// This is intended for process initialization paths where missing agent
// registration is a startup bug.
func (r *Runtime) MustClient(id agent.Ident) AgentClient {
	client, err := r.Client(id)
	if err != nil {
		panic(err)
	}
	return client
}

// ClientFor returns a typed client for one externally supplied route.
//
// Use this in caller-only processes that do not register agents locally but
// still need to start workflows against worker-owned routes.
// Returns ErrAgentNotFound when route metadata is incomplete.
func (r *Runtime) ClientFor(route AgentRoute) (AgentClient, error) {
	if route.ID == "" || route.WorkflowName == "" {
		return nil, ErrAgentNotFound
	}
	return &agentClientRoute{r: r, route: route}, nil
}

// MustClientFor is like ClientFor but panics on invalid route metadata.
//
// This is intended for startup paths where route metadata is expected to be
// validated by construction.
func (r *Runtime) MustClientFor(route AgentRoute) AgentClient {
	client, err := r.ClientFor(route)
	if err != nil {
		panic(err)
	}
	return client
}

func (c *agentClient) Run(ctx context.Context, sessionID string, messages []*model.Message, opts ...RunOption) (*RunOutput, error) {
	handle, err := c.Start(ctx, sessionID, messages, opts...)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

func (c *agentClient) Start(ctx context.Context, sessionID string, messages []*model.Message, opts ...RunOption) (engine.WorkflowHandle, error) {
	input := buildSessionRunInput(c.id, sessionID, messages, opts)
	return c.r.startRun(ctx, &input)
}

func (c *agentClient) Continue(ctx context.Context, sessionID, predecessorRunID, runID, turnID string, response *api.PendingInputResponse) (*RunOutput, error) {
	handle, err := c.StartContinuation(ctx, sessionID, predecessorRunID, runID, turnID, response)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

func (c *agentClient) StartContinuation(ctx context.Context, sessionID, predecessorRunID, runID, turnID string, response *api.PendingInputResponse) (engine.WorkflowHandle, error) {
	input, err := c.r.buildStoredContinuationRunInput(ctx, c.id, sessionID, predecessorRunID, runID, turnID, response)
	if err != nil {
		return nil, err
	}
	return c.r.startRun(ctx, input)
}

func (c *agentClient) StartOneShot(ctx context.Context, messages []*model.Message, opts ...RunOption) (engine.WorkflowHandle, error) {
	input := buildOneShotRunInput(c.id, messages, opts)
	return c.r.startOneShotRun(ctx, &input)
}

func (c *agentClient) OneShotRun(ctx context.Context, messages []*model.Message, opts ...RunOption) (*RunOutput, error) {
	handle, err := c.StartOneShot(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

func (c *agentClientRoute) Run(ctx context.Context, sessionID string, messages []*model.Message, opts ...RunOption) (*RunOutput, error) {
	handle, err := c.Start(ctx, sessionID, messages, opts...)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

func (c *agentClientRoute) Start(ctx context.Context, sessionID string, messages []*model.Message, opts ...RunOption) (engine.WorkflowHandle, error) {
	input := buildSessionRunInput(c.route.ID, sessionID, messages, opts)
	return c.r.startRunWithRoute(ctx, &input, c.route)
}

func (c *agentClientRoute) Continue(ctx context.Context, sessionID, predecessorRunID, runID, turnID string, response *api.PendingInputResponse) (*RunOutput, error) {
	handle, err := c.StartContinuation(ctx, sessionID, predecessorRunID, runID, turnID, response)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

func (c *agentClientRoute) StartContinuation(ctx context.Context, sessionID, predecessorRunID, runID, turnID string, response *api.PendingInputResponse) (engine.WorkflowHandle, error) {
	input, err := c.r.buildStoredContinuationRunInput(ctx, c.route.ID, sessionID, predecessorRunID, runID, turnID, response)
	if err != nil {
		return nil, err
	}
	return c.r.startRunWithRoute(ctx, input, c.route)
}

func (c *agentClientRoute) StartOneShot(ctx context.Context, messages []*model.Message, opts ...RunOption) (engine.WorkflowHandle, error) {
	input := buildOneShotRunInput(c.route.ID, messages, opts)
	return c.r.startOneShotRunWithRoute(ctx, &input, c.route)
}

func (c *agentClientRoute) OneShotRun(ctx context.Context, messages []*model.Message, opts ...RunOption) (*RunOutput, error) {
	handle, err := c.StartOneShot(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

// buildSessionRunInput constructs RunInput for sessionful execution and applies
// all caller options in-order.
func buildSessionRunInput(agentID agent.Ident, sessionID string, messages []*model.Message, opts []RunOption) RunInput {
	input := RunInput{
		AgentID:   agentID,
		SessionID: sessionID,
		Messages:  messages,
	}
	applyRunOptions(&input, opts)
	return input
}

// buildOneShotRunInput constructs RunInput for one-shot execution and applies
// all caller options in-order.
func buildOneShotRunInput(agentID agent.Ident, messages []*model.Message, opts []RunOption) RunInput {
	input := RunInput{
		AgentID:  agentID,
		Messages: messages,
	}
	applyRunOptions(&input, opts)
	return input
}

// buildContinuationRunInput constructs the only legal input shape for a
// suspended sessionful run. Policy, labels, tool context, and transcript state
// are restored from the opaque checkpoint by the worker.
func buildContinuationRunInput(agentID agent.Ident, sessionID, runID, turnID string, suspension *api.RunSuspension, response *api.PendingInputResponse) (*RunInput, error) {
	if sessionID == "" {
		return nil, ErrMissingSessionID
	}
	if runID == "" {
		return nil, errors.New("continuation run id is required")
	}
	if turnID == "" {
		return nil, errors.New("continuation turn id is required")
	}
	if suspension == nil {
		return nil, errors.New("run suspension is required")
	}
	if response == nil {
		return nil, errors.New("pending input response is required")
	}
	if err := validatePendingInputResponse(response); err != nil {
		return nil, err
	}
	return &RunInput{
		AgentID:   agentID,
		RunID:     runID,
		SessionID: sessionID,
		TurnID:    turnID,
		Continuation: &api.RunContinuationInput{
			Suspension: suspension,
			Response:   response,
		},
	}, nil
}

// buildStoredContinuationRunInput loads the exact predecessor checkpoint from
// durable runtime storage so callers provide only domain response and run IDs.
func (r *Runtime) buildStoredContinuationRunInput(ctx context.Context, agentID agent.Ident, sessionID, predecessorRunID, runID, turnID string, response *api.PendingInputResponse) (*RunInput, error) {
	if predecessorRunID == "" {
		return nil, errors.New("predecessor run id is required")
	}
	suspension, err := r.LoadRunSuspension(ctx, predecessorRunID)
	if err != nil {
		return nil, err
	}
	input, err := buildContinuationRunInput(agentID, sessionID, runID, turnID, suspension, response)
	if err != nil {
		return nil, err
	}
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	if err != nil {
		return nil, err
	}
	if err := validateContinuationIdentity(input, checkpoint); err != nil {
		return nil, err
	}
	if checkpoint.BaseContext.RunID != predecessorRunID {
		return nil, errors.New("predecessor run id does not match stored suspension")
	}
	return input, nil
}

// applyRunOptions mutates input with non-nil run options in the order supplied
// by the caller.
func applyRunOptions(input *RunInput, opts []RunOption) {
	for _, option := range opts {
		if option == nil {
			continue
		}
		option(input)
	}
}
