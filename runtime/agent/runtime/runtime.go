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
// Example usage: use AgentClient for execution.
//
//	rt := runtime.New(runtime.Options{ Engine: temporalEngine, ... })
//	_ = rt.RegisterAgent(ctx, agentReg)
//	client := rt.MustClient(agent.Ident("service.agent"))
//	out, err := client.Run(ctx, messages, runtime.WithSessionID("s1"))
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"

	"text/template"

	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
)

type (
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
	//  3. Start workflows via AgentClient (Run or Start)
	//
	// The Runtime automatically subscribes to hooks for memory persistence and
	// stream publishing when MemoryStore or Stream are configured.
	Runtime struct {
		// Engine is the workflow backend adapter (Temporal by default).
		Engine engine.Engine
		// MemoryStore persists run transcripts and annotations.
		Memory memory.Store
		// Policy evaluates allowlists and caps per planner turn.
		Policy policy.Engine
		// RunStore tracks run metadata for observability.
		RunStore run.Store
		// Bus is the bus used for streaming runtime events.
		Bus hooks.Bus
		// Stream publishes planner/tool/assistant events to the caller.
		Stream stream.Sink

		logger  telemetry.Logger
		metrics telemetry.Metrics
		tracer  telemetry.Tracer

		mu        sync.RWMutex
		agents    map[string]AgentRegistration
		toolsets  map[string]ToolsetRegistration
		toolSpecs map[tools.Ident]tools.ToolSpec
		// parsed tool payload schemas cached by tool name for hint building
		toolSchemas map[string]map[string]any
		models      map[string]model.Client

		// Per-agent tool specs registered during agent registration for introspection.
		agentToolSpecs map[agent.Ident][]tools.ToolSpec

		handleMu   sync.RWMutex
		runHandles map[string]engine.WorkflowHandle

		// workers holds optional per-agent worker configuration supplied at
		// construction time.
		workers map[string]WorkerConfig

		// registrationClosed prevents late agent registration after the first
		// run is submitted, avoiding dynamic handler registration on running
		// workers (not supported by some engines).
		registrationClosed bool

		// routes stores route-only registrations for agents that are executed
		// inline but whose planners live in other processes. These provide
		// activity names/options and policy/spec metadata so ExecuteAgentInline
		// can orchestrate nested planning via activities across processes.
		// routes removed: inline composition now piggybacks on toolset
		// registration and conventions; no explicit route registry.
	}

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

		// Workers provides per-agent worker configuration. If an agent lacks
		// an entry, the runtime uses a default worker configuration. Engines
		// that do not poll (in-memory) ignore this map.
		Workers map[string]WorkerConfig
	}

	// RuntimeOption configures the runtime via functional options passed to NewWith.
	RuntimeOption func(*Options)

	// WorkerConfig configures the worker for a specific agent. Engines that
	// support background workers (e.g., Temporal) use this configuration to
	// determine queue bindings and concurrency. For in-memory engines this is
	// ignored.
	WorkerConfig struct {
		// Queue overrides the default task queue for this agent's workflow and
		// activities. When empty the generated default queue is used.
		Queue string
	}

	// WorkerOption configures a WorkerConfig.
	WorkerOption func(*WorkerConfig)

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
		// ExecuteToolActivityOptions describes retry/timeout/queue for the ExecuteTool activity.
		// Strong-contract scheduling: when set, these options are applied to all tool activities
		// scheduled by this agent (including nested inline agent runs via ExecuteAgentInlineWithRoute).
		ExecuteToolActivityOptions engine.ActivityOptions
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
		Execute func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error)

		// Specs enumerates the codecs associated with each tool in the set.
		// Used by the runtime for JSON marshaling/unmarshaling and schema validation.
		Specs []tools.ToolSpec

		// TaskQueue optionally overrides the queue used when scheduling this toolset's activities.
		TaskQueue string

		// Inline indicates that tools in this toolset must execute inline within the
		// workflow loop rather than via a separate activity. This is required for
		// agent-as-tool compositions where the Execute implementation invokes
		// ExecuteAgentInline, which relies on an in-scope engine.WorkflowContext.
		//
		// Non-agent toolsets (service/client backed) should leave this false so that
		// tools are scheduled as activities, enabling isolation and retries.
		Inline bool

		// CallHints optionally provides precompiled templates for call display hints
		// keyed by tool ident. When present, RegisterToolset installs these in the
		// global hints registry so sinks can render concise, domain-authored labels.
		CallHints map[tools.Ident]*template.Template

		// ResultHints optionally provides precompiled templates for result previews
		// keyed by tool ident. When present, RegisterToolset installs these in the
		// global hints registry so sinks can render concise result previews.
		ResultHints map[tools.Ident]*template.Template

		// PayloadAdapter normalizes or enriches raw JSON payloads prior to decoding.
		// The adapter is applied exactly once at the activity boundary, or before
		// inline execution for Inline toolsets. When nil, no adaptation is applied.
		PayloadAdapter func(ctx context.Context, meta ToolCallMeta, tool tools.Ident, raw json.RawMessage) (json.RawMessage, error)

		// ResultAdapter normalizes encoded JSON results before they are published or
		// returned to the caller. When nil, no adaptation is applied.
		ResultAdapter func(ctx context.Context, meta ToolCallMeta, tool tools.Ident, raw json.RawMessage) (json.RawMessage, error)

		// DecodeInExecutor instructs the runtime to pass raw JSON payloads through to
		// the executor without pre-decoding. The executor must decode using generated
		// codecs. Defaults to false.
		DecodeInExecutor bool

		// SuppressChildEvents hides child inline tool events for agent-as-tool
		// registrations. When true, only the aggregated parent event is emitted.
		SuppressChildEvents bool

		// TelemetryBuilder can be provided to build or enrich telemetry consistently
		// across transports. When set, the runtime may invoke it with timing/context.
		TelemetryBuilder func(ctx context.Context, meta ToolCallMeta, tool tools.Ident, start, end time.Time, extras map[string]any) *telemetry.ToolTelemetry
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

		// OnMissingFields controls behavior when validation indicates missing fields:
		// "finalize" | "await_clarification" | "resume"
		OnMissingFields MissingFieldsAction
	}
)

// MissingFieldsAction controls behavior when a tool validation error indicates
// missing fields.  It is string-backed for JSON friendliness. Empty value means
// unspecified (planner decides).
type MissingFieldsAction string

const (
	// MissingFieldsFinalize instructs the runtime to finalize immediately
	// when fields are missing.
	MissingFieldsFinalize MissingFieldsAction = "finalize"
	// MissingFieldsAwaitClarification instructs the runtime to pause and await user clarification.
	MissingFieldsAwaitClarification MissingFieldsAction = "await_clarification"
	// MissingFieldsResume instructs the runtime to continue without pausing; surface hints to the planner.
	MissingFieldsResume MissingFieldsAction = "resume"
)

var (
	// Typed error sentinels for common invalid states.
	ErrAgentNotFound       = errors.New("agent not found")
	ErrEngineNotConfigured = errors.New("runtime engine not configured")
	ErrInvalidConfig       = errors.New("invalid configuration")
	ErrMissingSessionID    = errors.New("session id is required")
	ErrWorkflowStartFailed = errors.New("workflow start failed")
	ErrRegistrationClosed  = errors.New("registration closed after first run")
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

// WithTurnID sets the TurnID on the constructed RunInput.
func WithTurnID(id string) RunOption {
	return func(in *RunInput) { in.TurnID = id }
}

// WithMetadata merges the provided metadata into the constructed RunInput.
func WithMetadata(meta map[string]any) RunOption {
	return func(in *RunInput) {
		if len(meta) == 0 {
			return
		}
		if in.Metadata == nil {
			in.Metadata = make(map[string]any, len(meta))
		}
		for k, v := range meta {
			in.Metadata[k] = v
		}
	}
}

// WithTaskQueue sets the target task queue on WorkflowOptions for this run.
func WithTaskQueue(name string) RunOption {
	return func(in *RunInput) {
		if in.WorkflowOptions == nil {
			in.WorkflowOptions = &WorkflowOptions{}
		}
		in.WorkflowOptions.TaskQueue = name
	}
}

// WithMemo sets memo on WorkflowOptions for this run.
func WithMemo(m map[string]any) RunOption {
	return func(in *RunInput) {
		if in.WorkflowOptions == nil {
			in.WorkflowOptions = &WorkflowOptions{}
		}
		// merge shallow
		if in.WorkflowOptions.Memo == nil {
			in.WorkflowOptions.Memo = make(map[string]any, len(m))
		}
		for k, v := range m {
			in.WorkflowOptions.Memo[k] = v
		}
	}
}

// WithSearchAttributes sets search attributes on WorkflowOptions for this run.
func WithSearchAttributes(sa map[string]any) RunOption {
	return func(in *RunInput) {
		if in.WorkflowOptions == nil {
			in.WorkflowOptions = &WorkflowOptions{}
		}
		if in.WorkflowOptions.SearchAttributes == nil {
			in.WorkflowOptions.SearchAttributes = make(map[string]any, len(sa))
		}
		maps.Copy(in.WorkflowOptions.SearchAttributes, sa)
	}
}

// WithWorkflowOptions sets workflow engine options on the constructed RunInput.
func WithWorkflowOptions(o *WorkflowOptions) RunOption {
	return func(in *RunInput) { in.WorkflowOptions = o }
}

// WithPerTurnMaxToolCalls sets a per-turn cap on tool executions. Zero means unlimited.
func WithPerTurnMaxToolCalls(n int) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.PerTurnMaxToolCalls = n
	}
}

// WithRunMaxToolCalls sets a per-run cap on total tool executions.
// Non-zero overrides the agent's DSL RunPolicy default for this run.
// Zero means no override (use the design default, which may be unlimited).
func WithRunMaxToolCalls(n int) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.MaxToolCalls = n
	}
}

// WithRunMaxConsecutiveFailedToolCalls caps consecutive failures before aborting the run.
// Non-zero overrides the agent's DSL RunPolicy default for this run.
// Zero means no override (use the design default, which may be unlimited).
func WithRunMaxConsecutiveFailedToolCalls(n int) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.MaxConsecutiveFailedToolCalls = n
	}
}

// WithRunTimeBudget sets a wall-clock budget for the run. Zero means no override.
func WithRunTimeBudget(d time.Duration) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.TimeBudget = d
	}
}

// WithRunInterruptsAllowed enables human-in-the-loop interruptions for this run.
// When false, no override is applied and the agent registration policy governs.
func WithRunInterruptsAllowed(allowed bool) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.InterruptsAllowed = allowed
	}
}

// WithRestrictToTool restricts candidate tools to a single tool for the run.
func WithRestrictToTool(id tools.Ident) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.RestrictToTool = id
	}
}

// WithAllowedTags filters candidate tools to those whose tags intersect this list.
func WithAllowedTags(tags []string) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.AllowedTags = append([]string(nil), tags...)
	}
}

// WithDeniedTags filters out candidate tools that have any of these tags.
func WithDeniedTags(tags []string) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.DeniedTags = append([]string(nil), tags...)
	}
}

// newFromOptions constructs a Runtime using the provided options. Internal helper
// used by the public New(...RuntimeOption) constructor.
func newFromOptions(opts Options) *Runtime {
	bus := opts.Hooks
	if bus == nil {
		bus = hooks.NewBus()
	}
	eng := opts.Engine
	if eng == nil {
		eng = engineinmem.New()
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
		Engine:         eng,
		Memory:         opts.MemoryStore,
		Policy:         opts.Policy,
		RunStore:       opts.RunStore,
		Bus:            bus,
		Stream:         opts.Stream,
		logger:         logger,
		metrics:        metrics,
		tracer:         tracer,
		agents:         make(map[string]AgentRegistration),
		toolsets:       make(map[string]ToolsetRegistration),
		toolSpecs:      make(map[tools.Ident]tools.ToolSpec),
		toolSchemas:    make(map[string]map[string]any),
		models:         make(map[string]model.Client),
		runHandles:     make(map[string]engine.WorkflowHandle),
		agentToolSpecs: make(map[agent.Ident][]tools.ToolSpec),
		workers:        opts.Workers,
	}
	if rt.Memory != nil {
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
				return rt.Memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.ToolResultReceivedEvent:
				memEvent = memory.Event{
					Type:      memory.EventToolResult,
					Timestamp: time.UnixMilli(evt.Timestamp()),
					Data: map[string]any{
						"tool_call_id":        evt.ToolCallID,
						"parent_tool_call_id": evt.ParentToolCallID,
						"tool_name":           evt.ToolName,
						"result":              evt.Result,
						"duration":            evt.Duration,
						"error":               evt.Error,
					},
				}
				return rt.Memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.AssistantMessageEvent:
				memEvent = memory.Event{
					Type:      memory.EventAssistantMessage,
					Timestamp: time.UnixMilli(evt.Timestamp()),
					Data: map[string]any{
						"message":    evt.Message,
						"structured": evt.Structured,
					},
				}
				return rt.Memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.PlannerNoteEvent:
				memEvent = memory.Event{
					Type:      memory.EventPlannerNote,
					Timestamp: time.UnixMilli(evt.Timestamp()),
					Data: map[string]any{
						"note": evt.Note,
					},
					Labels: evt.Labels,
				}
				return rt.Memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			}
			return nil
		})
		if _, err := bus.Register(memSub); err != nil {
			rt.logger.Warn(context.Background(), "failed to register memory subscriber", "err", err)
		}
	}
	if rt.Stream != nil {
		streamSub, err := stream.NewSubscriber(rt.Stream)
		if err != nil {
			rt.logger.Warn(context.Background(), "failed to create stream subscriber", "err", err)
		} else if _, err := bus.Register(streamSub); err != nil {
			rt.logger.Warn(context.Background(), "failed to register stream subscriber", "err", err)
		}
	}
	return rt
}

// New constructs a Runtime using functional options. It installs sane defaults
// (in-memory engine, noop logger/metrics/tracer, in-process event bus) when not
// provided. The returned Runtime is immediately usable for agent registration.
func New(opts ...RuntimeOption) *Runtime {
	var o Options
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return newFromOptions(o)
}

// WithEngine sets the workflow engine.
func WithEngine(e engine.Engine) RuntimeOption { return func(o *Options) { o.Engine = e } }

// WithMemoryStore sets the memory store.
func WithMemoryStore(m memory.Store) RuntimeOption { return func(o *Options) { o.MemoryStore = m } }

// WithRunStore sets the run metadata store.
func WithRunStore(s run.Store) RuntimeOption { return func(o *Options) { o.RunStore = s } }

// WithPolicy sets the policy engine.
func WithPolicy(p policy.Engine) RuntimeOption { return func(o *Options) { o.Policy = p } }

// WithStream sets the stream sink.
func WithStream(s stream.Sink) RuntimeOption { return func(o *Options) { o.Stream = s } }

// WithHooks sets the event bus.
func WithHooks(b hooks.Bus) RuntimeOption { return func(o *Options) { o.Hooks = b } }

// WithLogger sets the logger.
func WithLogger(l telemetry.Logger) RuntimeOption { return func(o *Options) { o.Logger = l } }

// WithMetrics sets the metrics recorder.
func WithMetrics(m telemetry.Metrics) RuntimeOption { return func(o *Options) { o.Metrics = m } }

// WithTracer sets the tracer.
func WithTracer(t telemetry.Tracer) RuntimeOption { return func(o *Options) { o.Tracer = t } }

// WithWorker configures the worker for a specific agent. Engines that support
// worker polling use this configuration to bind the agent to a specific queue.
// If unspecified, a default worker configuration is used.
func WithWorker(id agent.Ident, cfg WorkerConfig) RuntimeOption {
	return func(o *Options) {
		if o.Workers == nil {
			o.Workers = make(map[string]WorkerConfig)
		}
		o.Workers[string(id)] = cfg
	}
}

// WithQueue returns a WorkerOption that sets the queue name on a WorkerConfig.
func WithQueue(name string) WorkerOption {
	return func(c *WorkerConfig) { c.Queue = name }
}

// RegisterAgent validates the registration, registers workflows and activities with
// the engine, and stores the agent metadata for later lookup. Returns an error if
// required fields are missing or if engine registration fails.
//
// All agents must be registered before workflows can be started. Generated code
// calls this during initialization.
func (r *Runtime) RegisterAgent(ctx context.Context, reg AgentRegistration) error {
	r.mu.RLock()
	if r.registrationClosed {
		r.mu.RUnlock()
		return ErrRegistrationClosed
	}
	r.mu.RUnlock()
	if reg.ID == "" {
		return fmt.Errorf("%w: missing agent ID", ErrInvalidConfig)
	}
	if reg.Planner == nil {
		return fmt.Errorf("%w: missing planner", ErrInvalidConfig)
	}
	if reg.Workflow.Handler == nil {
		return fmt.Errorf("%w: missing workflow handler", ErrInvalidConfig)
	}
	if reg.ExecuteToolActivity == "" {
		return fmt.Errorf("%w: missing execute tool activity name", ErrInvalidConfig)
	}
	if reg.PlanActivityName == "" {
		return fmt.Errorf("%w: missing plan activity name", ErrInvalidConfig)
	}
	if reg.ResumeActivityName == "" {
		return fmt.Errorf("%w: missing resume activity name", ErrInvalidConfig)
	}
	if r.Engine == nil {
		return ErrEngineNotConfigured
	}

	// Apply per-agent worker overrides before engine registration.
	if cfg, ok := r.workers[reg.ID]; ok {
		if q := strings.TrimSpace(cfg.Queue); q != "" {
			reg.Workflow.TaskQueue = q
			for i := range reg.Activities {
				reg.Activities[i].Options.Queue = q
			}
			reg.PlanActivityOptions.Queue = q
			reg.ResumeActivityOptions.Queue = q
		}
	}

	// Register untyped workflow; Temporal adapter wraps with workflow.Context and
	// we coerce input to *RunInput inside WorkflowHandler. This preserves engine
	// boundaries and avoids leaking Temporal types here.
	if err := r.Engine.RegisterWorkflow(ctx, reg.Workflow); err != nil {
		return err
	}
	for _, act := range reg.Activities {
		if act.Handler == nil {
			continue
		}
		if err := r.Engine.RegisterActivity(ctx, act); err != nil {
			return err
		}
	}

	r.mu.Lock()
	r.agents[reg.ID] = reg
	r.addToolSpecsLocked(reg.Specs)
	if len(reg.Specs) > 0 {
		// store a shallow copy to avoid external mutation
		cp := make([]tools.ToolSpec, len(reg.Specs))
		copy(cp, reg.Specs)
		r.agentToolSpecs[agent.Ident(reg.ID)] = cp
	}
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

	// Install optional hint templates into the global registry for sinks.
	if len(ts.CallHints) > 0 {
		rthints.RegisterCallHints(ts.CallHints)
	}
	if len(ts.ResultHints) > 0 {
		rthints.RegisterResultHints(ts.ResultHints)
	}
	return nil
}

// RegisterAgentRoute registers route-only metadata for an agent so that
// ExecuteAgentInline can orchestrate the agent via activities even when the
// planner is not locally registered. Safe to call multiple times; later calls
// replace previous metadata.
// RegisterAgentRoute removed: toolset registration piggybacks provider metadata
// and conventions are used as fallback for activity names/queues.

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

// agentByID returns the registered agent by ID if present. The boolean indicates
// whether the agent was found. Intended for internal/runtime use and codegen.
func (r *Runtime) agentByID(id string) (AgentRegistration, bool) {
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
	messages []*planner.AgentMessage,
	nestedRunCtx run.Context,
) (*RunOutput, error) {
	ctx := wfCtx.Context()

	var parentTracker *childTracker
	if nestedRunCtx.ParentToolCallID != "" {
		parentTracker = newChildTracker(nestedRunCtx.ParentToolCallID)
	}

	reg, ok := r.agentByID(agentID)
	if !ok {
		return nil, fmt.Errorf("agent %q not registered", agentID)
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

	// Build initial plan. If a local planner is registered, invoke directly; otherwise
	// schedule the plan activity so engines can route to remote workers.
	planInput := &planner.PlanInput{
		Messages:   messages,
		RunContext: nestedRunCtx,
		Agent:      agentCtx,
		Events:     newPlannerEvents(r, agentID, nestedRunCtx.RunID),
	}
	var initialPlan *planner.PlanResult
	if reg.Planner != nil {
		// Local planner available: call directly (test-friendly and efficient)
		var err error
		initialPlan, err = r.planStart(ctx, reg, planInput)
		if err != nil {
			return nil, fmt.Errorf("plan start: %w", err)
		}
	} else {
		startReq := PlanActivityInput{
			AgentID:    agentID,
			RunID:      nestedRunCtx.RunID,
			Messages:   planInput.Messages,
			RunContext: planInput.RunContext,
		}
		if reg.PlanActivityName == "" {
			return nil, fmt.Errorf("agent %q missing plan activity for inline execution", agentID)
		}
		var err error
		initialPlan, err = r.runPlanActivity(wfCtx, reg.PlanActivityName, reg.PlanActivityOptions, startReq)
		if err != nil {
			return nil, fmt.Errorf("plan activity failed: %w", err)
		}
	}
	if initialPlan == nil {
		return nil, fmt.Errorf("plan start returned nil result")
	}

	// Initialize caps from agent policy
	caps := initialCaps(reg.Policy)

	// Calculate deadline
	var deadline time.Time
	if reg.Policy.TimeBudget > 0 {
		deadline = wfCtx.Now().Add(reg.Policy.TimeBudget)
	}

	// Turn sequencer for nested run
	var seq *turnSequencer
	if nestedRunCtx.TurnID != "" {
		seq = &turnSequencer{
			turnID: nestedRunCtx.TurnID,
		}
	}
	nestedInput := RunInput{
		AgentID:   agentID,
		RunID:     nestedRunCtx.RunID,
		SessionID: nestedRunCtx.SessionID,
		TurnID:    nestedRunCtx.TurnID,
		Messages:  messages,
		Labels:    nestedRunCtx.Labels,
	}
	out, err := r.runLoop(wfCtx, reg, &nestedInput, planInput, initialPlan, caps, deadline, 1, seq, parentTracker, nil)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// ExecuteAgentInlineWithRoute runs an agent inline using explicit route and activity
// names without relying on local registration. Strong contract: no conventions.
func (r *Runtime) ExecuteAgentInlineWithRoute(
	wfCtx engine.WorkflowContext,
	route AgentRoute,
	planActivityName, resumeActivityName, executeToolActivity string,
	messages []*planner.AgentMessage,
	nestedRunCtx run.Context,
) (*RunOutput, error) {
	if route.ID == "" || route.WorkflowName == "" || route.DefaultTaskQueue == "" {
		return nil, fmt.Errorf("inline route is incomplete")
	}
	if planActivityName == "" || resumeActivityName == "" || executeToolActivity == "" {
		return nil, fmt.Errorf("inline activity names are required")
	}
	ctx := wfCtx.Context()
	reader := r.memoryReader(ctx, string(route.ID), nestedRunCtx.RunID)
	agentCtx := newAgentContext(agentContextOptions{
		runtime: r,
		agentID: string(route.ID),
		runID:   nestedRunCtx.RunID,
		memory:  reader,
		turnID:  nestedRunCtx.TurnID,
	})
	planInput := &planner.PlanInput{
		Messages:   messages,
		RunContext: nestedRunCtx,
		Agent:      agentCtx,
		Events:     newPlannerEvents(r, string(route.ID), nestedRunCtx.RunID),
	}

	// Always schedule plan activity on the provider queue
	startReq := PlanActivityInput{
		AgentID:    string(route.ID),
		RunID:      nestedRunCtx.RunID,
		Messages:   planInput.Messages,
		RunContext: planInput.RunContext,
	}
	initial, err := r.runPlanActivity(wfCtx, planActivityName, engine.ActivityOptions{
		Queue: route.DefaultTaskQueue,
	}, startReq)
	if err != nil {
		return nil, err
	}
	if initial == nil {
		return nil, fmt.Errorf("plan start returned nil result")
	}
	caps := initialCaps(RunPolicy{})
	var deadline time.Time
	var seq *turnSequencer
	if nestedRunCtx.TurnID != "" {
		seq = &turnSequencer{
			turnID: nestedRunCtx.TurnID,
		}
	}
	nestedInput := RunInput{
		AgentID:   string(route.ID),
		RunID:     nestedRunCtx.RunID,
		SessionID: nestedRunCtx.SessionID,
		TurnID:    nestedRunCtx.TurnID,
		Messages:  messages,
		Labels:    nestedRunCtx.Labels,
	}
	// Build a synthetic registration for the run loop (policy/specs not needed)
	reg := AgentRegistration{
		ID:                         string(route.ID),
		Workflow:                   engine.WorkflowDefinition{Name: route.WorkflowName, TaskQueue: route.DefaultTaskQueue},
		PlanActivityName:           planActivityName,
		ResumeActivityName:         resumeActivityName,
		ExecuteToolActivity:        executeToolActivity,
		PlanActivityOptions:        engine.ActivityOptions{Queue: route.DefaultTaskQueue},
		ResumeActivityOptions:      engine.ActivityOptions{Queue: route.DefaultTaskQueue},
		ExecuteToolActivityOptions: engine.ActivityOptions{Queue: route.DefaultTaskQueue},
	}
	return r.runLoop(wfCtx, reg, &nestedInput, planInput, initial, caps, deadline, 1, seq, nil, nil)
}

// StartRun launches the agent workflow asynchronously and returns a workflow handle
// so callers can wait, signal, or cancel execution. The RunID is generated if not
// provided in the input. Returns an error if the agent is not registered or if the
// workflow fails to start.
func (r *Runtime) startRun(ctx context.Context, input *RunInput) (engine.WorkflowHandle, error) {
	if input.AgentID == "" {
		return nil, fmt.Errorf("%w: missing agent id", ErrAgentNotFound)
	}
	reg, ok := r.agentByID(input.AgentID)
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrAgentNotFound, input.AgentID)
	}
	return r.startRunOn(ctx, input, reg.Workflow.Name, reg.Workflow.TaskQueue)
}

// startRunWithMeta launches the agent workflow using client-supplied metadata
// rather than a locally registered agent. This enables remote caller processes
// to start runs when workers are registered in another process.
func (r *Runtime) startRunWithRoute(ctx context.Context, input *RunInput, route AgentRoute) (engine.WorkflowHandle, error) {
	if route.ID == "" || route.WorkflowName == "" {
		return nil, fmt.Errorf("%w: missing route for agent client", ErrAgentNotFound)
	}
	if input.AgentID == "" {
		input.AgentID = string(route.ID)
	}
	return r.startRunOn(ctx, input, route.WorkflowName, route.DefaultTaskQueue)
}

// startRunOn contains common start logic for both locally-registered and remote-route clients.
func (r *Runtime) startRunOn(ctx context.Context, input *RunInput, workflowName, defaultQueue string) (engine.WorkflowHandle, error) {
	// Close registration on first run submission to avoid dynamic handler registration after workers may have started.
	r.mu.Lock()
	r.registrationClosed = true
	r.mu.Unlock()
	if input.RunID == "" {
		input.RunID = generateRunID(input.AgentID)
	}
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, ErrMissingSessionID
	}
	r.recordRunStatus(ctx, input, run.StatusPending, nil)
	req := engine.WorkflowStartRequest{
		ID:        input.RunID,
		Workflow:  workflowName,
		TaskQueue: defaultQueue,
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
	handle, err := r.Engine.StartWorkflow(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkflowStartFailed, err)
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
	if s, ok := r.Engine.(engine.Signaler); ok {
		return s.SignalByID(ctx, req.RunID, "", interrupt.SignalPause, req)
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
	if s, ok := r.Engine.(engine.Signaler); ok {
		return s.SignalByID(ctx, req.RunID, "", interrupt.SignalResume, req)
	}
	handle, ok := r.workflowHandle(req.RunID)
	if !ok {
		return fmt.Errorf("run %q not found", req.RunID)
	}
	return handle.Signal(ctx, interrupt.SignalResume, req)
}

// ProvideClarification sends a typed clarification answer to a waiting run.
func (r *Runtime) ProvideClarification(ctx context.Context, ans interrupt.ClarificationAnswer) error {
	if ans.RunID == "" {
		return errors.New("run id is required")
	}
	if s, ok := r.Engine.(engine.Signaler); ok {
		return s.SignalByID(ctx, ans.RunID, "", interrupt.SignalProvideClarification, ans)
	}
	handle, ok := r.workflowHandle(ans.RunID)
	if !ok {
		return fmt.Errorf("run %q not found", ans.RunID)
	}
	return handle.Signal(ctx, interrupt.SignalProvideClarification, ans)
}

// ProvideToolResults sends a set of external tool results to a waiting run.
func (r *Runtime) ProvideToolResults(ctx context.Context, rs interrupt.ToolResultsSet) error {
	if rs.RunID == "" {
		return errors.New("run id is required")
	}
	if s, ok := r.Engine.(engine.Signaler); ok {
		return s.SignalByID(ctx, rs.RunID, "", interrupt.SignalProvideToolResults, rs)
	}
	handle, ok := r.workflowHandle(rs.RunID)
	if !ok {
		return fmt.Errorf("run %q not found", rs.RunID)
	}
	return handle.Signal(ctx, interrupt.SignalProvideToolResults, rs)
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
		r.toolSpecs[spec.Name] = spec
		// Cache parsed payload schema for hint building
		if len(spec.Payload.Schema) > 0 {
			var m map[string]any
			if err := json.Unmarshal(spec.Payload.Schema, &m); err == nil {
				r.toolSchemas[string(spec.Name)] = m
			}
		}
	}
}

// toolSpec retrieves a tool spec by fully qualified name. Thread-safe.
func (r *Runtime) toolSpec(name tools.Ident) (tools.ToolSpec, bool) {
	r.mu.RLock()
	spec, ok := r.toolSpecs[name]
	r.mu.RUnlock()
	return spec, ok
}

// ListAgents returns the IDs of registered agents.
func (r *Runtime) ListAgents() []agent.Ident {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.agents) == 0 {
		return nil
	}
	out := make([]agent.Ident, 0, len(r.agents))
	for id := range r.agents {
		out = append(out, agent.Ident(id))
	}
	return out
}

// ListToolsets returns the names of registered toolsets.
func (r *Runtime) ListToolsets() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.toolsets) == 0 {
		return nil
	}
	out := make([]string, 0, len(r.toolsets))
	for id := range r.toolsets {
		out = append(out, id)
	}
	return out
}

// ToolSpec returns the registered ToolSpec for the given tool name.
func (r *Runtime) ToolSpec(name tools.Ident) (tools.ToolSpec, bool) {
	return r.toolSpec(name)
}

// ToolSpecsForAgent returns the ToolSpecs registered by the given agent.
func (r *Runtime) ToolSpecsForAgent(agentID agent.Ident) []tools.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	specs := r.agentToolSpecs[agentID]
	if len(specs) == 0 {
		return nil
	}
	out := make([]tools.ToolSpec, len(specs))
	copy(out, specs)
	return out
}

// ToolSchema returns the parsed JSON schema for the tool payload when available.
func (r *Runtime) ToolSchema(name tools.Ident) (map[string]any, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.toolSchemas[string(name)]
	if !ok {
		return nil, false
	}
	// shallow copy to avoid external mutation
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out, true
}

// OverridePolicy applies a best-effort in-process override of the registered agent policy.
// Only non-zero fields are applied (and InterruptsAllowed when true). Overrides affect
// subsequent runs and are local to this runtime instance.
func (r *Runtime) OverridePolicy(agentID agent.Ident, delta RunPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	reg, ok := r.agents[string(agentID)]
	if !ok {
		return ErrAgentNotFound
	}
	if delta.MaxToolCalls > 0 {
		reg.Policy.MaxToolCalls = delta.MaxToolCalls
	}
	if delta.MaxConsecutiveFailedToolCalls > 0 {
		reg.Policy.MaxConsecutiveFailedToolCalls = delta.MaxConsecutiveFailedToolCalls
	}
	if delta.TimeBudget > 0 {
		reg.Policy.TimeBudget = delta.TimeBudget
	}
	if delta.InterruptsAllowed {
		reg.Policy.InterruptsAllowed = true
	}
	r.agents[string(agentID)] = reg
	return nil
}

// SubscribeRun registers a filtered stream subscriber for the given runID and returns
// a function that closes the subscription and the sink.
func (r *Runtime) SubscribeRun(ctx context.Context, runID string, sink stream.Sink) (func(), error) {
	// Reuse the standard stream subscriber and filter by run ID.
	sub, err := stream.NewSubscriber(sink)
	if err != nil {
		return nil, err
	}
	filtered := hooks.SubscriberFunc(func(c context.Context, evt hooks.Event) error {
		if evt.RunID() != runID {
			return nil
		}
		return sub.HandleEvent(c, evt)
	})
	s, err := r.Bus.Register(filtered)
	if err != nil {
		return nil, err
	}
	closeFn := func() {
		_ = s.Close()
		_ = sink.Close(ctx)
	}
	return closeFn, nil
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
