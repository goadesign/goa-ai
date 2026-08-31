// Package runtime provides typed clients for sessionful and one-shot agent runs.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	// AgentClient is the high-level execution surface for one agent.
	//
	// Contract:
	// - Run, Start, and Prepare are sessionful APIs: callers must provide a
	//   concrete session ID that the host application has already created.
	// - OneShotRun, StartOneShot, and PrepareOneShot are sessionless: callers
	//   provide no session ID and the runtime persists only run records that can
	//   be read by run ID.
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

		// Prepare validates one initial sessionful run and returns its complete
		// launch request without starting a workflow. Callers store the result
		// before StartPrepared so a new process can retry the exact same start.
		Prepare(sessionID string, messages []*model.Message, opts ...RunOption) (*PreparedRun, error)

		// PrepareOneShot validates one initial sessionless run and returns its
		// complete launch request without starting a workflow. Callers store the
		// result before StartPrepared so a new process can retry the exact start.
		PrepareOneShot(messages []*model.Message, opts ...RunOption) (*PreparedRun, error)

		// Continue loads one exact predecessor suspension, starts a new sessionful
		// workflow with its first pending response, and blocks until completion.
		// WorkflowOptions apply only to the new engine workflow; the predecessor
		// checkpoint remains the sole owner of planner policy and execution state.
		Continue(
			ctx context.Context,
			sessionID, predecessorRunID, runID, turnID string,
			response *api.PendingInputResponse,
			workflowOptions WorkflowOptions,
		) (*RunOutput, error)

		// PrepareContinuation loads and validates one exact stored suspension
		// without starting a workflow or creating a successor run.
		PrepareContinuation(
			ctx context.Context,
			sessionID, predecessorRunID, runID, turnID string,
			response *api.PendingInputResponse,
			workflowOptions WorkflowOptions,
		) (*PreparedRun, error)

		// StartPrepared submits one value returned by Prepare or
		// PrepareContinuation. The prepared run can be stored in trusted application
		// storage and parsed in another process before it is submitted.
		StartPrepared(ctx context.Context, prepared *PreparedRun) (engine.WorkflowHandle, error)

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

	// AgentRoute identifies the workflow and default task queue for one agent.
	// Generated AgentDefinition values include this route so callers and workers
	// use the same workflow identity.
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

	// AgentDefinition is the generated contract shared by callers and workers.
	// It owns the workflow route and every generated tool fact needed to validate
	// a run before the workflow engine accepts it. Its data cannot be changed
	// after construction.
	AgentDefinition struct {
		route           AgentRoute
		specs           []tools.ToolSpec
		specByName      map[tools.Ident]tools.ToolSpec
		metadata        map[tools.Ident]policy.ToolMetadata
		requiredLabels  []string
		executableTools []tools.Ident
		agents          map[agent.Ident]AgentDefinition
	}

	// agentClient binds execution to one generated agent definition.
	agentClient struct {
		r          *Runtime
		definition AgentDefinition
	}
)

// NewAgentDefinition builds an immutable copy of one generated agent contract.
// Generated packages call it once and return the same value to caller clients
// and worker registration.
func NewAgentDefinition(
	route AgentRoute,
	specs []tools.ToolSpec,
	metadata ToolMetadataLookup,
	requiredLabels []string,
	executableTools []tools.Ident,
	children []AgentDefinition,
) AgentDefinition {
	if route.ID == "" || route.WorkflowName == "" || route.DefaultTaskQueue == "" {
		panic("runtime: agent definition requires a complete route")
	}
	if err := validateSpecs(specs, metadata); err != nil {
		panic(fmt.Sprintf("runtime: invalid agent definition: %v", err))
	}
	ownedSpecs := cloneToolSpecs(specs)
	byName := make(map[tools.Ident]tools.ToolSpec, len(ownedSpecs))
	ownedMetadata := make(map[tools.Ident]policy.ToolMetadata, len(ownedSpecs))
	for _, spec := range ownedSpecs {
		if _, exists := byName[spec.Name]; exists {
			panic(fmt.Sprintf("runtime: agent definition has duplicate tool %q", spec.Name))
		}
		byName[spec.Name] = spec
		ownedMetadata[spec.Name] = canonicalToolMetadata(spec, metadata)
	}
	ownedExecutable := append([]tools.Ident(nil), executableTools...)
	slices.Sort(ownedExecutable)
	for index, name := range ownedExecutable {
		if _, ok := byName[name]; !ok {
			panic(fmt.Sprintf("runtime: executable tool %q is not in the agent definition", name))
		}
		if index > 0 && ownedExecutable[index-1] == name {
			panic(fmt.Sprintf("runtime: agent definition has duplicate executable tool %q", name))
		}
	}
	ownedLabels := append([]string(nil), requiredLabels...)
	slices.Sort(ownedLabels)
	for index, label := range ownedLabels {
		if label == "" {
			panic("runtime: agent definition has an empty required label")
		}
		if index > 0 && ownedLabels[index-1] == label {
			panic(fmt.Sprintf("runtime: agent definition has duplicate required label %q", label))
		}
	}
	ownedAgents := make(map[agent.Ident]AgentDefinition, len(children)+1)
	for _, child := range children {
		if !child.valid() {
			panic("runtime: agent definition has an invalid child definition")
		}
		if _, exists := ownedAgents[child.route.ID]; exists {
			panic(fmt.Sprintf("runtime: agent definition has duplicate child agent %q", child.route.ID))
		}
		ownedAgents[child.route.ID] = child
	}
	definition := AgentDefinition{
		route:           route,
		specs:           ownedSpecs,
		specByName:      byName,
		metadata:        ownedMetadata,
		requiredLabels:  ownedLabels,
		executableTools: ownedExecutable,
		agents:          ownedAgents,
	}
	if _, exists := ownedAgents[route.ID]; exists {
		panic(fmt.Sprintf("runtime: agent definition repeats root agent %q", route.ID))
	}
	ownedAgents[route.ID] = definition
	definition.agents = ownedAgents
	return definition
}

// Route returns this definition's workflow route.
func (d AgentDefinition) Route() AgentRoute {
	return d.route
}

// ChildDefinition returns the generated definition for one reachable child
// agent. The boolean is false when the agent is not part of this definition's
// generated composition graph.
func (d AgentDefinition) ChildDefinition(id agent.Ident) (AgentDefinition, bool) {
	child, ok := d.agents[id]
	return child, ok
}

// valid reports whether the definition was created with NewAgentDefinition.
func (d AgentDefinition) valid() bool {
	return d.route.ID != "" && d.route.WorkflowName != "" && d.route.DefaultTaskQueue != "" && d.specByName != nil
}

// spec returns one owned generated tool contract.
func (d AgentDefinition) spec(name tools.Ident) (tools.ToolSpec, bool) {
	spec, ok := d.specByName[name]
	return spec, ok
}

// metadataFor returns one generated policy record owned by the definition.
func (d AgentDefinition) metadataFor(name tools.Ident) (policy.ToolMetadata, bool) {
	metadata, ok := d.metadata[name]
	return metadata, ok
}

// Client returns a typed client for one locally registered agent.
//
// Returns ErrAgentNotFound when id is empty or not present in runtime
// registration.
func (r *Runtime) Client(id agent.Ident) (AgentClient, error) {
	if id == "" {
		return nil, ErrAgentNotFound
	}
	registration, ok := r.agentByID(id)
	if !ok {
		return nil, ErrAgentNotFound
	}
	return &agentClient{r: r, definition: registration.Definition}, nil
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
func (r *Runtime) ClientFor(definition AgentDefinition) (AgentClient, error) {
	if !definition.valid() {
		return nil, ErrAgentNotFound
	}
	return &agentClient{r: r, definition: definition}, nil
}

// MustClientFor is like ClientFor but panics on invalid route metadata.
//
// This is intended for startup paths where route metadata is expected to be
// validated by construction.
func (r *Runtime) MustClientFor(definition AgentDefinition) AgentClient {
	client, err := r.ClientFor(definition)
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
	prepared, err := c.Prepare(sessionID, messages, opts...)
	if err != nil {
		return nil, err
	}
	return c.StartPrepared(ctx, prepared)
}

func (c *agentClient) Prepare(sessionID string, messages []*model.Message, opts ...RunOption) (*PreparedRun, error) {
	start, err := buildSessionRunStart(c.definition.route.ID, sessionID, messages, opts)
	if err != nil {
		return nil, err
	}
	request, err := prepareRunWithDefinition(&start.input, start.launch, c.definition, true)
	if err != nil {
		return nil, err
	}
	return newPreparedRun(c.definition.route.ID, request, start.launch.taskQueue)
}

func (c *agentClient) PrepareOneShot(messages []*model.Message, opts ...RunOption) (*PreparedRun, error) {
	start, err := buildOneShotRunStart(c.definition.route.ID, messages, opts)
	if err != nil {
		return nil, err
	}
	request, err := prepareRunWithDefinition(&start.input, start.launch, c.definition, false)
	if err != nil {
		return nil, err
	}
	return newPreparedRun(c.definition.route.ID, request, start.launch.taskQueue)
}

func (c *agentClient) Continue(
	ctx context.Context,
	sessionID, predecessorRunID, runID, turnID string,
	response *api.PendingInputResponse,
	workflowOptions WorkflowOptions,
) (*RunOutput, error) {
	prepared, err := c.PrepareContinuation(ctx, sessionID, predecessorRunID, runID, turnID, response, workflowOptions)
	if err != nil {
		return nil, err
	}
	handle, err := c.StartPrepared(ctx, prepared)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

func (c *agentClient) PrepareContinuation(
	ctx context.Context,
	sessionID, predecessorRunID, runID, turnID string,
	response *api.PendingInputResponse,
	workflowOptions WorkflowOptions,
) (*PreparedRun, error) {
	input, err := c.r.buildStoredContinuationRunInput(ctx, c.definition, sessionID, predecessorRunID, runID, turnID, response)
	if err != nil {
		return nil, err
	}
	launch, err := encodeWorkflowLaunch(workflowOptions)
	if err != nil {
		return nil, continuationContractError(err)
	}
	request, err := prepareRunWithDefinition(input, launch, c.definition, true)
	if err != nil {
		return nil, continuationContractError(err)
	}
	prepared, err := newPreparedRun(c.definition.route.ID, request, launch.taskQueue)
	if err != nil {
		return nil, continuationContractError(err)
	}
	return prepared, nil
}

func (c *agentClient) StartOneShot(ctx context.Context, messages []*model.Message, opts ...RunOption) (engine.WorkflowHandle, error) {
	prepared, err := c.PrepareOneShot(messages, opts...)
	if err != nil {
		return nil, err
	}
	return c.StartPrepared(ctx, prepared)
}

func (c *agentClient) OneShotRun(ctx context.Context, messages []*model.Message, opts ...RunOption) (*RunOutput, error) {
	handle, err := c.StartOneShot(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return handle.Wait(ctx)
}

// buildSessionRunStart constructs a sessionful workflow input and its separate
// engine launch settings.
func buildSessionRunStart(agentID agent.Ident, sessionID string, messages []*model.Message, opts []RunOption) (runStart, error) {
	start := runStart{input: RunInput{
		AgentID:   agentID,
		SessionID: sessionID,
		Messages:  messages,
	}}
	applyRunOptions(&start, opts)
	launch, err := encodeWorkflowLaunch(start.options)
	if err != nil {
		return runStart{}, err
	}
	start.launch = launch
	return start, nil
}

// buildOneShotRunStart constructs a sessionless workflow input and its
// separate engine launch settings.
func buildOneShotRunStart(agentID agent.Ident, messages []*model.Message, opts []RunOption) (runStart, error) {
	start := runStart{input: RunInput{
		AgentID:  agentID,
		Messages: messages,
	}}
	applyRunOptions(&start, opts)
	launch, err := encodeWorkflowLaunch(start.options)
	if err != nil {
		return runStart{}, err
	}
	start.launch = launch
	return start, nil
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
func (r *Runtime) buildStoredContinuationRunInput(
	ctx context.Context,
	definition AgentDefinition,
	sessionID, predecessorRunID, runID, turnID string,
	response *api.PendingInputResponse,
) (*RunInput, error) {
	if predecessorRunID == "" {
		return nil, continuationContractError(errors.New("predecessor run id is required"))
	}
	suspension, err := r.LoadRunSuspension(ctx, predecessorRunID)
	if err != nil {
		return nil, err
	}
	input, err := buildContinuationRunInput(definition.route.ID, sessionID, runID, turnID, suspension, response)
	if err != nil {
		return nil, continuationContractError(err)
	}
	checkpoint, err := prepareContinuation(input, definition)
	if err != nil {
		return nil, continuationContractError(err)
	}
	if checkpoint.PreviousRunID != predecessorRunID {
		return nil, continuationContractError(errors.New("predecessor run id does not match stored suspension"))
	}
	return input, nil
}

// applyRunOptions applies caller options in order to workflow input and launch
// settings.
func applyRunOptions(start *runStart, opts []RunOption) {
	for _, option := range opts {
		if option == nil {
			panic("runtime: run option is required")
		}
		option.apply(start)
	}
}
