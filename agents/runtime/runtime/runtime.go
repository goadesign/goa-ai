// Package runtime implements the core orchestration engine for goa-ai agents.
// It coordinates workflow execution, planner invocations, tool scheduling, policy
// enforcement, memory persistence, and event streaming. The Runtime instance serves
// as the central registry for agents, toolsets, models, and manages their lifecycle
// through durable workflow execution (typically via Temporal).
//
// Key responsibilities:
//   - Agent and toolset registration with validation
//   - Workflow lifecycle management (start, execute, resume)
//   - Policy enforcement (caps, timeouts, tool filtering)
//   - Memory persistence via hook subscriptions
//   - Event streaming and telemetry integration
//   - Tool execution and JSON codec management
//
// The Runtime is thread-safe and can be used concurrently to register agents
// and execute workflows. Production deployments typically configure the Runtime
// with MongoDB-backed stores (features/memory/mongo, features/run/mongo) and
// Temporal as the workflow engine.
//
// Example usage:
//
//	rt := runtime.New(runtime.Options{
//	    Engine:      temporalEngine,
//	    MemoryStore: memoryStore,
//	    RunStore:    runStore,
//	    Policy:      policyEngine,
//	})
//	rt.RegisterAgent(ctx, agentReg)
//	output, err := rt.Run(ctx, runtime.RunInput{
//	    AgentID:  "service.agent",
//	    Messages: messages,
//	})
package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"goa.design/goa-ai/agents/runtime/engine"
	"goa.design/goa-ai/agents/runtime/hooks"
	"goa.design/goa-ai/agents/runtime/interrupt"
	"goa.design/goa-ai/agents/runtime/memory"
	"goa.design/goa-ai/agents/runtime/model"
	"goa.design/goa-ai/agents/runtime/planner"
	"goa.design/goa-ai/agents/runtime/policy"
	"goa.design/goa-ai/agents/runtime/run"
	"goa.design/goa-ai/agents/runtime/stream"
	"goa.design/goa-ai/agents/runtime/telemetry"
	"goa.design/goa-ai/agents/runtime/tools"
)

type (
	// Options configures the Runtime instance. All fields are optional except Engine
	// for production deployments. Noop implementations are substituted for nil Logger,
	// Metrics, and Tracer. A default in-memory event bus is created if Hooks is nil.
	Options struct {
		// Engine is the workflow backend adapter (Temporal by default).
		Engine engine.Engine
		// MemoryStore persists run transcripts and annotations.
		MemoryStore memory.Store
		// Policy evaluates allowlists and caps per planner turn.
		Policy policy.Engine
		// RunStore tracks run metadata for observability.
		RunStore run.Store
		// Hooks is the Pulse-backed bus used for streaming runtime events.
		Hooks hooks.Bus
		// Stream publishes planner/tool/assistant events to the caller.
		Stream stream.Sink
		// Logger emits structured logs (usually backed by Clue).
		Logger telemetry.Logger
		// Metrics records counters/histograms for runtime operations.
		Metrics telemetry.Metrics
		// Tracer emits spans for planner/tool execution.
		Tracer telemetry.Tracer
	}

	// Runtime orchestrates agent workflows, policy enforcement, memory persistence,
	// and event streaming. It serves as the central registry for agents, toolsets,
	// and models. All public methods are thread-safe and can be called concurrently.
	//
	// The Runtime coordinates with several subsystems:
	//   - Workflow engine (Temporal) for durable execution
	//   - Policy engine for runtime caps and tool filtering
	//   - Memory store for transcript persistence
	//   - Event bus (hooks) for observability and streaming
	//   - Telemetry subsystems (logging, metrics, tracing)
	//
	// Lifecycle:
	//  1. Construct with New()
	//  2. Register agents, toolsets, and models
	//  3. Start workflows via Run() or StartRun()
	//
	// The Runtime automatically subscribes to hooks for memory persistence and
	// stream publishing when MemoryStore or Stream are configured.
	Runtime struct {
		engine  engine.Engine
		memory  memory.Store
		policy  policy.Engine
		runs    run.Store
		hooks   hooks.Bus
		stream  stream.Sink
		logger  telemetry.Logger
		metrics telemetry.Metrics
		tracer  telemetry.Tracer

		mu         sync.RWMutex
		agents     map[string]AgentRegistration
		toolsets   map[string]ToolsetRegistration
		toolSpecs  map[string]tools.ToolSpec
		models     map[string]model.Client
		runHandles map[string]engine.WorkflowHandle
		handleMu   sync.RWMutex
	}

	// AgentRegistration bundles the generated assets for an agent. This struct is
	// produced by codegen and passed to RegisterAgent to make an agent available
	// for execution.
	AgentRegistration struct {
		// ID is the unique agent identifier (service.agent).
		ID string
		// Planner is the concrete planner implementation for the agent.
		Planner planner.Planner
		// Workflow describes the durable workflow registered with the engine.
		Workflow engine.WorkflowDefinition
		// Activities lists the activity handlers (plan/resume/tool) to register.
		Activities []engine.ActivityDefinition
		// Toolsets enumerates tool registrations exposed by this agent package.
		Toolsets []ToolsetRegistration
		// PlanActivityName names the activity used for PlanStart.
		PlanActivityName string
		// PlanActivityOptions describes retry/timeout behavior for the PlanStart activity.
		PlanActivityOptions engine.ActivityOptions
		// ResumeActivityName names the activity used for PlanResume.
		ResumeActivityName string
		// ResumeActivityOptions describes retry/timeout behavior for the PlanResume activity.
		ResumeActivityOptions engine.ActivityOptions
		// ExecuteToolActivity is the logical name of the registered ExecuteTool activity.
		ExecuteToolActivity string
		// Specs provides JSON codecs for every tool declared in the agent design.
		Specs []tools.ToolSpec
		// Policy configures caps/time budget/interrupt settings for the agent.
		Policy RunPolicy
	}

	// ToolsetRegistration holds the metadata and execution logic for a toolset.
	// Users register toolsets by providing an Execute function that handles all
	// tools in the toolset. Codegen auto-generates registrations for service-based
	// tools and agent-tools; users provide registrations for custom/server-side tools.
	//
	// The Execute function is the core dispatch mechanism - it receives tool name
	// and JSON payload, and returns JSON result. This uniform interface allows:
	//   - Service-based tools: codegen generates Execute calling service clients
	//   - Agent-tools: codegen generates Execute calling ExecuteAgentInline
	//   - Custom tools: users provide Execute with their implementation
	//
	// This pattern eliminates runtime type detection - all dispatch happens at
	// build time via codegen, and activities simply call toolset.Execute.
	ToolsetRegistration struct {
		// Name is the qualified toolset name (e.g., "service.toolset_name").
		Name string

		// Description provides human-readable context for tooling.
		Description string

		// Metadata captures structured policy metadata about the toolset.
		Metadata policy.ToolMetadata

		// Execute invokes the concrete tool implementation for a given tool call.
		// Returns a ToolResult containing the payload, telemetry, errors, and retry hints.
		//
		// For service-based tools, codegen generates this function to call service clients.
		// For agent-tools (Exports), codegen generates this to call ExecuteAgentInline
		// and convert RunOutput to ToolResult.
		// For custom/server-side tools, users provide their own implementation.
		Execute func(ctx context.Context, call planner.ToolCallRequest) (planner.ToolResult, error)

		// Specs enumerates the codecs associated with each tool in the set.
		// Used by the runtime for JSON marshaling/unmarshaling and schema validation.
		Specs []tools.ToolSpec

		// TaskQueue optionally overrides the queue used when scheduling this toolset's activities.
		TaskQueue string
	}

	// RunPolicy configures per-agent runtime behavior (caps, time budgets, interrupts).
	// These values are evaluated during workflow execution to enforce limits and prevent
	// runaway tool loops or budget overruns.
	RunPolicy struct {
		// MaxToolCalls caps the total number of tool invocations per run (0 = unlimited).
		MaxToolCalls int
		// MaxConsecutiveFailedToolCalls caps sequential failures before aborting (0 = unlimited).
		MaxConsecutiveFailedToolCalls int
		// TimeBudget is the wall-clock deadline for run completion (0 = unlimited).
		TimeBudget time.Duration
		// InterruptsAllowed indicates whether the workflow can be paused and resumed.
		InterruptsAllowed bool
	}
)

// RunOption configures the RunInput constructed by RunAgent and StartAgent.
// Options allow callers to set optional fields without building RunInput directly.
type RunOption func(*RunInput)

// WithRunID sets the RunID on the constructed RunInput.
func WithRunID(id string) RunOption {
	return func(in *RunInput) { in.RunID = id }
}

// WithSessionID sets the SessionID on the constructed RunInput.
func WithSessionID(id string) RunOption {
	return func(in *RunInput) { in.SessionID = id }
}

// WithLabels merges the provided labels into the constructed RunInput.
func WithLabels(labels map[string]string) RunOption {
	return func(in *RunInput) { in.Labels = mergeLabels(in.Labels, labels) }
}

// WithWorkflowOptions sets workflow engine options on the constructed RunInput.
func WithWorkflowOptions(o *WorkflowOptions) RunOption {
	return func(in *RunInput) { in.WorkflowOptions = o }
}

// Default returns a new Runtime with the default options.
func Default() *Runtime {
	return New(Options{})
}

// New constructs a Runtime with the provided options. If Hooks, Logger, Metrics, or
// Tracer are nil, noop implementations are substituted. The returned Runtime is
// immediately usable for agent registration but requires an Engine to start workflows.
//
// The constructor automatically subscribes to hooks for memory persistence (if
// MemoryStore is configured) and stream publishing (if Stream is configured).
func New(opts Options) *Runtime {
	bus := opts.Hooks
	if bus == nil {
		bus = hooks.NewBus()
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = telemetry.NoopMetrics{}
	}
	logger := opts.Logger
	if logger == nil {
		logger = telemetry.NoopLogger{}
	}
	tracer := opts.Tracer
	if tracer == nil {
		tracer = telemetry.NoopTracer{}
	}
	rt := &Runtime{
		engine:     opts.Engine,
		memory:     opts.MemoryStore,
		policy:     opts.Policy,
		runs:       opts.RunStore,
		hooks:      bus,
		stream:     opts.Stream,
		logger:     logger,
		metrics:    metrics,
		tracer:     tracer,
		agents:     make(map[string]AgentRegistration),
		toolsets:   make(map[string]ToolsetRegistration),
		toolSpecs:  make(map[string]tools.ToolSpec),
		models:     make(map[string]model.Client),
		runHandles: make(map[string]engine.WorkflowHandle),
	}
	if rt.memory != nil {
		memSub := hooks.SubscriberFunc(func(ctx context.Context, event hooks.Event) error {
			var memEvent memory.Event
			switch evt := event.(type) {
			case *hooks.ToolCallScheduledEvent:
				memEvent = memory.Event{
					Type:      memory.EventToolCall,
					Timestamp: time.UnixMilli(evt.Timestamp()),
					Data: map[string]any{
						"tool_name": evt.ToolName,
						"payload":   evt.Payload,
						"queue":     evt.Queue,
					},
				}
				return rt.memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.ToolResultReceivedEvent:
				memEvent = memory.Event{
					Type:      memory.EventToolResult,
					Timestamp: time.UnixMilli(evt.Timestamp()),
					Data: map[string]any{
						"tool_name": evt.ToolName,
						"result":    evt.Result,
						"duration":  evt.Duration,
						"error":     evt.Error,
					},
				}
				return rt.memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.AssistantMessageEvent:
				memEvent = memory.Event{
					Type:      memory.EventAssistantMessage,
					Timestamp: time.UnixMilli(evt.Timestamp()),
					Data: map[string]any{
						"message":    evt.Message,
						"structured": evt.Structured,
					},
				}
				return rt.memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.PlannerNoteEvent:
				memEvent = memory.Event{
					Type:      memory.EventPlannerNote,
					Timestamp: time.UnixMilli(evt.Timestamp()),
					Data:      map[string]any{"note": evt.Note},
					Labels:    evt.Labels,
				}
				return rt.memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			}
			return nil
		})
		if _, err := bus.Register(memSub); err != nil {
			rt.logger.Warn(context.Background(), "failed to register memory subscriber", "err", err)
		}
	}
	if rt.stream != nil {
		streamSub, err := hooks.NewStreamSubscriber(rt.stream)
		if err != nil {
			rt.logger.Warn(context.Background(), "failed to create stream subscriber", "err", err)
		} else if _, err := bus.Register(streamSub); err != nil {
			rt.logger.Warn(context.Background(), "failed to register stream subscriber", "err", err)
		}
	}
	return rt
}

// RegisterAgent validates the registration, registers workflows and activities with
// the engine, and stores the agent metadata for later lookup. Returns an error if
// required fields are missing or if engine registration fails.
//
// All agents must be registered before workflows can be started. Generated code
// calls this during initialization.
func (r *Runtime) RegisterAgent(ctx context.Context, reg AgentRegistration) error {
	if reg.ID == "" {
		return errors.New("agent registration missing ID")
	}
	if reg.Planner == nil {
		return errors.New("agent registration missing planner")
	}
	if reg.Workflow.Handler == nil {
		return errors.New("agent registration missing workflow handler")
	}
	if reg.ExecuteToolActivity == "" {
		return errors.New("agent registration missing execute tool activity")
	}
	if reg.PlanActivityName == "" {
		return errors.New("agent registration missing plan activity")
	}
	if reg.ResumeActivityName == "" {
		return errors.New("agent registration missing resume activity")
	}
	if r.engine == nil {
		return errors.New("runtime engine not configured")
	}

	if err := r.engine.RegisterWorkflow(ctx, reg.Workflow); err != nil {
		return err
	}
	for _, act := range reg.Activities {
		if act.Handler == nil {
			continue
		}
		if err := r.engine.RegisterActivity(ctx, act); err != nil {
			return err
		}
	}

	r.mu.Lock()
	r.agents[reg.ID] = reg
	r.addToolSpecsLocked(reg.Specs)
	for _, ts := range reg.Toolsets {
		r.addToolsetLocked(ts)
	}
	r.mu.Unlock()

	return nil
}

// RegisterToolset registers a toolset outside of agent registration. Useful for
// feature modules that expose shared toolsets. Returns an error if required fields
// (Name, Execute) are missing.
func (r *Runtime) RegisterToolset(ts ToolsetRegistration) error {
	if ts.Name == "" {
		return errors.New("toolset name is required")
	}
	if ts.Execute == nil {
		return errors.New("toolset execute function is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addToolsetLocked(ts)
	return nil
}

// RegisterModel registers a ModelClient by identifier for planner lookup. Planners
// can retrieve registered models via AgentContext.ModelClient(). Returns an error
// if the ID is empty or the client is nil.
func (r *Runtime) RegisterModel(id string, client model.Client) error {
	if id == "" {
		return errors.New("model id is required")
	}
	if client == nil {
		return errors.New("model client is required")
	}
	r.mu.Lock()
	r.models[id] = client
	r.mu.Unlock()
	return nil
}

// Toolset returns a registered toolset by ID if present. The boolean indicates
// whether the toolset was found.
func (r *Runtime) Toolset(id string) (ToolsetRegistration, bool) {
	return r.LookupToolset(id)
}

// LookupToolset retrieves a registered toolset by name. Returns false if the
// toolset is not registered.
func (r *Runtime) LookupToolset(id string) (ToolsetRegistration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ts, ok := r.toolsets[id]
	return ts, ok
}

// Agent returns the registered agent by ID if present. The boolean indicates
// whether the agent was found.
func (r *Runtime) Agent(id string) (AgentRegistration, bool) {
	r.mu.RLock()
	agent, ok := r.agents[id]
	r.mu.RUnlock()
	return agent, ok
}

// ExecuteAgentInline runs an agent's complete planning loop inline within the
// current workflow context. This is the entry point for agent-as-tool execution,
// where one agent invokes another agent as a tool call.
//
// Unlike ExecuteWorkflow (which starts a new durable workflow), ExecuteAgentInline
// runs the nested agent synchronously in the same workflow execution. This provides:
//   - Deterministic workflow replay (nested execution is part of parent workflow history)
//   - Zero overhead (no separate workflow or marshaling)
//   - Natural composition (nested agent completes before parent continues)
//
// The nested agent runs its full plan/execute/resume loop:
//  1. Calls PlanStart with the provided messages
//  2. Executes any tool calls (which may themselves be agent-tools)
//  3. Calls PlanResume after tool results
//  4. Repeats until the agent returns a final response
//
// Parent-child tracking: If nestedRunCtx.TurnID is set, all events from the nested
// agent will be tagged with that TurnID and sequenced relative to the parent's events.
// The nested agent inherits the parent's turn sequencer for consistent event ordering.
//
// Policy and caps: The nested agent uses its own RunPolicy (defined in its Goa design).
// It does NOT inherit the parent's remaining tool budget - each agent enforces its own caps.
//
// Memory: The nested agent has its own memory scope (separate runID). Tool calls and
// results are persisted under the nested runID, allowing the nested agent to be
// replayed or debugged independently.
//
// Parameters:
//   - wfCtx: The parent workflow context. The nested agent shares this context for
//     deterministic execution and can schedule its own activities.
//   - agentID: The fully qualified agent identifier (e.g., "service.agent_name").
//   - messages: The conversation messages to pass to the nested agent's planner.
//   - nestedRunCtx: Run context for the nested execution, including the nested runID
//     and optional parent tool call ID for tracking.
//
// Returns the nested agent's final output or an error if planning or execution fails.
// Tool-level errors (e.g., a tool call failed) are captured in the agent's output,
// not returned as errors - only infrastructure failures return errors.
func (r *Runtime) ExecuteAgentInline(
	wfCtx engine.WorkflowContext,
	agentID string,
	messages []planner.AgentMessage,
	nestedRunCtx run.Context,
) (RunOutput, error) {
	ctx := wfCtx.Context()

	var parentTracker *childTracker
	if nestedRunCtx.ParentToolCallID != "" {
		parentTracker = newChildTracker(nestedRunCtx.ParentToolCallID)
	}

	// Look up agent registration
	reg, ok := r.Agent(agentID)
	if !ok {
		return RunOutput{}, fmt.Errorf("agent %q not registered", agentID)
	}

	// Create agent context with nested memory scope
	reader := r.memoryReader(ctx, agentID, nestedRunCtx.RunID)
	agentCtx := newAgentContext(agentContextOptions{
		runtime: r,
		agentID: agentID,
		runID:   nestedRunCtx.RunID,
		memory:  reader,
		turnID:  nestedRunCtx.TurnID,
	})

	// Create planner input
	planInput := planner.PlanInput{
		Messages:   messages,
		RunContext: nestedRunCtx,
		Agent:      agentCtx,
	}

	// Call PlanStart to get initial plan
	initialPlan, err := r.planStart(ctx, reg, planInput)
	if err != nil {
		return RunOutput{}, fmt.Errorf("plan start: %w", err)
	}

	// Initialize caps from agent policy
	caps := policy.CapsState{
		RemainingToolCalls:                  reg.Policy.MaxToolCalls,
		RemainingConsecutiveFailedToolCalls: reg.Policy.MaxConsecutiveFailedToolCalls,
	}

	// Calculate deadline
	var deadline time.Time
	if reg.Policy.TimeBudget > 0 {
		deadline = time.Now().Add(reg.Policy.TimeBudget)
	}

	// Create or inherit turn sequencer
	var seq *turnSequencer
	if nestedRunCtx.TurnID != "" {
		seq = &turnSequencer{turnID: nestedRunCtx.TurnID}
	}

	nestedInput := RunInput{
		AgentID:   agentID,
		RunID:     nestedRunCtx.RunID,
		SessionID: nestedRunCtx.SessionID,
		TurnID:    nestedRunCtx.TurnID,
		Messages:  messages,
		Labels:    nestedRunCtx.Labels,
	}

	return r.runLoop(wfCtx, reg, &nestedInput, planInput, initialPlan, caps, deadline, 1, seq, parentTracker, nil)
}

// Options exposes the runtime dependencies, useful for generated code hooking
// and introspection.
func (r *Runtime) Options() Options {
	return Options{
		Engine:      r.engine,
		MemoryStore: r.memory,
		Policy:      r.policy,
		RunStore:    r.runs,
		Hooks:       r.hooks,
		Stream:      r.stream,
		Logger:      r.logger,
		Metrics:     r.metrics,
		Tracer:      r.tracer,
	}
}

// Run starts the agent workflow synchronously and waits for the final output.
// Generated Goa transports call this helper to offer a simple request/response API.
// Returns an error if the workflow fails to start or execute.
func (r *Runtime) Run(ctx context.Context, input RunInput) (RunOutput, error) {
	h, err := r.StartRun(ctx, input)
	if err != nil {
		return RunOutput{}, err
	}
	var out RunOutput
	if err := h.Wait(ctx, &out); err != nil {
		return RunOutput{}, err
	}
	return out, nil
}

// RunAgent starts a run for the given agent and messages and waits for completion.
// It is a high-level convenience over Run that constructs RunInput from arguments
// and applies optional RunOptions.
func (r *Runtime) RunAgent(
	ctx context.Context,
	agentID string,
	messages []planner.AgentMessage,
	opts ...RunOption,
) (RunOutput, error) {
	in := RunInput{AgentID: agentID, Messages: messages}
	for _, o := range opts {
		o(&in)
	}
	return r.Run(ctx, in)
}

// StartAgent starts a run for the given agent and messages and returns the workflow handle.
// It is a high-level convenience over StartRun that constructs RunInput from arguments
// and applies optional RunOptions.
func (r *Runtime) StartAgent(
	ctx context.Context,
	agentID string,
	messages []planner.AgentMessage,
	opts ...RunOption,
) (engine.WorkflowHandle, error) {
	in := RunInput{AgentID: agentID, Messages: messages}
	for _, o := range opts {
		o(&in)
	}
	return r.StartRun(ctx, in)
}

// StartRun launches the agent workflow asynchronously and returns a workflow handle
// so callers can wait, signal, or cancel execution. The RunID is generated if not
// provided in the input. Returns an error if the agent is not registered or if the
// workflow fails to start.
func (r *Runtime) StartRun(ctx context.Context, input RunInput) (engine.WorkflowHandle, error) {
	if input.AgentID == "" {
		return nil, errors.New("agent id is required")
	}
	reg, ok := r.Agent(input.AgentID)
	if !ok {
		return nil, fmt.Errorf("agent %q is not registered", input.AgentID)
	}
	if input.RunID == "" {
		input.RunID = generateRunID(input.AgentID)
	}
	r.recordRunStatus(ctx, &input, run.StatusPending, nil)
	req := engine.WorkflowStartRequest{
		ID:        input.RunID,
		Workflow:  reg.Workflow.Name,
		TaskQueue: reg.Workflow.TaskQueue,
		Input:     input,
	}
	if opts := input.WorkflowOptions; opts != nil {
		if opts.TaskQueue != "" {
			req.TaskQueue = opts.TaskQueue
		}
		req.Memo = cloneMetadata(opts.Memo)
		req.SearchAttributes = cloneMetadata(opts.SearchAttributes)
		if !isZeroRetryPolicy(opts.RetryPolicy) {
			req.RetryPolicy = opts.RetryPolicy
		}
	}
	handle, err := r.engine.StartWorkflow(ctx, req)
	if err != nil {
		return nil, err
	}
	r.storeWorkflowHandle(input.RunID, handle)
	return handle, nil
}

// PauseRun requests the underlying workflow to pause via the standard pause signal.
// Returns an error if the run is unknown or signaling fails.
func (r *Runtime) PauseRun(ctx context.Context, req interrupt.PauseRequest) error {
	if req.RunID == "" {
		return errors.New("run id is required")
	}
	handle, ok := r.workflowHandle(req.RunID)
	if !ok {
		return fmt.Errorf("run %q not found", req.RunID)
	}
	return handle.Signal(ctx, interrupt.SignalPause, req)
}

// ResumeRun notifies the workflow that execution can continue. The resume payload
// can include optional annotations/messages for the planner to consume.
func (r *Runtime) ResumeRun(ctx context.Context, req interrupt.ResumeRequest) error {
	if req.RunID == "" {
		return errors.New("run id is required")
	}
	handle, ok := r.workflowHandle(req.RunID)
	if !ok {
		return fmt.Errorf("run %q not found", req.RunID)
	}
	return handle.Signal(ctx, interrupt.SignalResume, req)
}

// addToolsetLocked registers a toolset and its specs without acquiring the lock.
// Caller must hold r.mu.
func (r *Runtime) addToolsetLocked(ts ToolsetRegistration) {
	r.toolsets[ts.Name] = ts
	r.addToolSpecsLocked(ts.Specs)
}

// addToolSpecsLocked registers tool specs without acquiring the lock.
// Caller must hold r.mu.
func (r *Runtime) addToolSpecsLocked(specs []tools.ToolSpec) {
	for _, spec := range specs {
		if spec.Name != "" {
			r.toolSpecs[spec.Name] = spec
		}
	}
}

// toolSpec retrieves a tool spec by fully qualified name. Thread-safe.
func (r *Runtime) toolSpec(name string) (tools.ToolSpec, bool) {
	r.mu.RLock()
	spec, ok := r.toolSpecs[name]
	r.mu.RUnlock()
	return spec, ok
}

func (r *Runtime) storeWorkflowHandle(runID string, handle engine.WorkflowHandle) {
	r.handleMu.Lock()
	if r.runHandles == nil {
		r.runHandles = make(map[string]engine.WorkflowHandle)
	}
	if handle == nil {
		delete(r.runHandles, runID)
	} else {
		r.runHandles[runID] = handle
	}
	r.handleMu.Unlock()
}

func (r *Runtime) workflowHandle(runID string) (engine.WorkflowHandle, bool) {
	r.handleMu.RLock()
	h, ok := r.runHandles[runID]
	r.handleMu.RUnlock()
	return h, ok
}
