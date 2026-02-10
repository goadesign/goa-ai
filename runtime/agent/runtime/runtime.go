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
// with a durable workflow engine (Temporal) and a durable memory store.
//
// Example usage: use AgentClient for execution.
//
//	rt := runtime.New(runtime.Options{ Engine: temporalEngine, ... })
//	if err := rt.RegisterAgent(ctx, agentReg); err != nil {
//		log.Fatal(err)
//	}
//	client := rt.MustClient(agent.Ident("service.agent"))
//	out, err := client.Run(ctx, "s1", messages)
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

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	bedrock "goa.design/goa-ai/features/model/bedrock"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	engtemporal "goa.design/goa-ai/runtime/agent/engine/temporal"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/interrupt"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
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
		// SessionStore persists session lifecycle state and run metadata.
		SessionStore session.Store
		// Policy evaluates allowlists and caps per planner turn.
		Policy policy.Engine
		// RunEventStore is the canonical append-only run event log.
		RunEventStore runlog.Store
		// Bus is the bus used for streaming runtime events.
		Bus hooks.Bus
		// Stream publishes planner/tool/assistant events to the caller.
		Stream stream.Sink
		// streamSubscriber forwards hook events to Stream. It is invoked from
		// hookActivity so stream emission can be made fatal while a session is active.
		streamSubscriber *stream.Subscriber

		logger  telemetry.Logger
		metrics telemetry.Metrics
		tracer  telemetry.Tracer

		mu        sync.RWMutex
		agents    map[agent.Ident]AgentRegistration
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
		workers map[agent.Ident]WorkerConfig

		// registrationClosed prevents late agent registration after the first
		// run is submitted, avoiding dynamic handler registration on running
		// workers (not supported by some engines).
		registrationClosed bool

		// hookActivityRegistered tracks whether the runtime hook activity has
		// been registered with the engine.
		hookActivityRegistered bool

		// reminders manages run-scoped system reminders used for backstage
		// guidance (safety, correctness, workflow) injected into prompts by
		// planners. It is internal to the runtime; planners interact with it
		// via PlannerContext.
		reminders *reminder.Engine

		// toolConfirmation configures runtime-enforced confirmation for selected tools.
		// It is used to require explicit operator approval before executing certain tools.
		// See ToolConfirmationConfig for details.
		toolConfirmation *ToolConfirmationConfig
	}

	// Options configures the Runtime instance. All fields are optional except Engine
	// for production deployments. Noop implementations are substituted for nil Logger,
	// Metrics, and Tracer. A default in-memory event bus is created if Hooks is nil.
	Options struct {
		// Engine is the workflow backend adapter (Temporal by default).
		Engine engine.Engine
		// MemoryStore persists run transcripts and annotations.
		MemoryStore memory.Store
		// SessionStore persists session lifecycle state and run metadata.
		SessionStore session.Store
		// Policy evaluates allowlists and caps per planner turn.
		Policy policy.Engine
		// RunEventStore is the canonical append-only run event log.
		RunEventStore runlog.Store
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
		Workers map[agent.Ident]WorkerConfig

		// ToolConfirmation configures runtime-enforced confirmation overrides for selected
		// tools (for example, requiring explicit operator approval before executing
		// additional tools that are not marked with design-time Confirmation).
		ToolConfirmation *ToolConfirmationConfig
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
		ID agent.Ident
		// Planner is the concrete planner implementation for the agent.
		Planner planner.Planner
		// Workflow describes the durable workflow registered with the engine.
		Workflow engine.WorkflowDefinition
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
		// When set, these options are applied to all service-backed tool activities
		// scheduled by this agent. Agent-as-tool executions run as child workflows.
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
	// The Execute function is the core dispatch mechanism for toolsets that run
	// inside activities or other non-workflow contexts. For inline toolsets, the
	// runtime may invoke Execute directly from the workflow loop.
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
		// For agent-tools (Exports), generated registrations set Inline=true and
		// populate AgentTool so the workflow runtime can start nested agents as child
		// workflows and adapt their RunOutput into a ToolResult.
		// For custom/server-side tools, users provide their own implementation.
		Execute func(ctx context.Context, call *planner.ToolRequest) (*planner.ToolResult, error)

		// Specs enumerates the codecs associated with each tool in the set.
		// Used by the runtime for JSON marshaling/unmarshaling and schema validation.
		Specs []tools.ToolSpec

		// TaskQueue optionally overrides the queue used when scheduling this toolset's activities.
		TaskQueue string

		// Inline indicates that tools in this toolset execute inside the workflow
		// context (not as activities). For agent-as-tool, the executor needs a
		// WorkflowContext to start the provider as a child workflow. Service-backed
		// toolsets should leave this false so calls run as activities (isolation/retries).
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

		// TelemetryBuilder can be provided to build or enrich telemetry consistently
		// across transports. When set, the runtime may invoke it with timing/context.
		TelemetryBuilder func(ctx context.Context, meta ToolCallMeta, tool tools.Ident, start, end time.Time, extras map[string]any) *telemetry.ToolTelemetry

		// AgentTool, when non-nil, carries configuration for agent-as-tool toolsets.
		// It is populated by NewAgentToolsetRegistration so the workflow runtime can
		// start nested agent runs directly (fan-out/fan-in) without relying on the
		// synchronous Execute callback.
		AgentTool *AgentToolConfig
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

		// FinalizerGrace reserves time to produce a last assistant message after the
		// budget is exhausted. When set, the runtime stops scheduling new work once
		// the remaining time is less than or equal to this value and requests a final
		// response from the planner. Zero means no reserved window; defaults may apply.
		FinalizerGrace time.Duration

		// InterruptsAllowed indicates whether the workflow can be paused and resumed.
		InterruptsAllowed bool

		// OnMissingFields controls behavior when validation indicates missing fields:
		// "finalize" | "await_clarification" | "resume"
		OnMissingFields MissingFieldsAction

		// History, when non-nil, transforms the message history before each planner
		// invocation (PlanStart and PlanResume). It can truncate or compress history
		// while preserving system prompts and logical turn boundaries.
		History HistoryPolicy

		// Cache configures automatic prompt cache checkpoint placement.
		Cache CachePolicy
	}

	// CachePolicy configures automatic cache checkpoint placement for an agent.
	// The runtime applies this policy to model requests by populating
	// model.Request.Cache when it is nil so planners do not need to thread
	// CacheOptions through every call site. Providers that do not support
	// caching ignore these options.
	CachePolicy struct {
		// AfterSystem places a checkpoint after all system messages.
		AfterSystem bool

		// AfterTools places a checkpoint after tool definitions. Not all
		// providers support tool-level checkpoints (e.g., Nova does not).
		AfterTools bool
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

const (
	// Opinionated defaults applied when activity timeouts are unspecified.
	defaultPlanActivityTimeout        = 30 * time.Second
	defaultResumeActivityTimeout      = 30 * time.Second
	defaultExecuteToolActivityTimeout = 2 * time.Minute
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

// RunOption configures optional fields on RunInput for Run and Start. Required
// values such as SessionID are positional arguments on AgentClient methods and
// must not be set via RunOption.
type RunOption func(*RunInput)

// WithRunID sets the RunID on the constructed RunInput.
func WithRunID(id string) RunOption {
	return func(in *RunInput) { in.RunID = id }
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

// WithTiming sets run-level timing overrides in a single structured option.
// Zero-valued fields are ignored.
func WithTiming(t Timing) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		if t.Budget > 0 {
			in.Policy.TimeBudget = t.Budget
		}
		if t.Plan > 0 {
			in.Policy.PlanTimeout = t.Plan
		}
		if t.Tools > 0 {
			in.Policy.ToolTimeout = t.Tools
		}
		if len(t.PerToolTimeout) > 0 {
			if in.Policy.PerToolTimeout == nil {
				in.Policy.PerToolTimeout = make(map[tools.Ident]time.Duration, len(t.PerToolTimeout))
			}
			for k, v := range t.PerToolTimeout {
				in.Policy.PerToolTimeout[k] = v
			}
		}
	}
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

// WithRunFinalizerGrace reserves time to produce a final assistant message after
// the run's TimeBudget is exhausted. Zero means no override.
func WithRunFinalizerGrace(d time.Duration) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.FinalizerGrace = d
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
	if opts.ToolConfirmation != nil {
		if err := opts.ToolConfirmation.validate(); err != nil {
			panic(err)
		}
	}
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
	if opts.RunEventStore == nil {
		opts.RunEventStore = runloginmem.New()
	}
	if opts.SessionStore == nil {
		opts.SessionStore = sessioninmem.New()
	}
	rt := &Runtime{
		Engine:           eng,
		Memory:           opts.MemoryStore,
		SessionStore:     opts.SessionStore,
		Policy:           opts.Policy,
		RunEventStore:    opts.RunEventStore,
		Bus:              bus,
		Stream:           opts.Stream,
		logger:           logger,
		metrics:          metrics,
		tracer:           tracer,
		agents:           make(map[agent.Ident]AgentRegistration),
		toolsets:         make(map[string]ToolsetRegistration),
		toolSpecs:        make(map[tools.Ident]tools.ToolSpec),
		toolSchemas:      make(map[string]map[string]any),
		models:           make(map[string]model.Client),
		runHandles:       make(map[string]engine.WorkflowHandle),
		agentToolSpecs:   make(map[agent.Ident][]tools.ToolSpec),
		workers:          opts.Workers,
		reminders:        reminder.NewEngine(),
		toolConfirmation: opts.ToolConfirmation,
	}
	// Install runtime-owned toolsets before any agent registration so planners
	// and transcripts can rely on a stable tool vocabulary.
	rt.mu.Lock()
	rt.addToolsetLocked(toolUnavailableToolsetRegistration())
	rt.mu.Unlock()
	if rt.SessionStore != nil {
		sessionSub := hooks.SubscriberFunc(func(ctx context.Context, event hooks.Event) error {
			var (
				status   session.RunStatus
				metadata map[string]any
			)
			ts := time.UnixMilli(event.Timestamp()).UTC()
			switch evt := event.(type) {
			case *hooks.RunStartedEvent:
				status = session.RunStatusRunning
				return rt.SessionStore.UpsertRun(ctx, session.RunMeta{
					AgentID:   evt.AgentID(),
					RunID:     evt.RunID(),
					SessionID: evt.SessionID(),
					Status:    status,
					UpdatedAt: ts,
					Labels:    evt.RunContext.Labels,
					Metadata:  nil,
					StartedAt: time.Time{},
				})
			case *hooks.RunPausedEvent:
				status = session.RunStatusPaused
				return rt.SessionStore.UpsertRun(ctx, session.RunMeta{
					AgentID:   evt.AgentID(),
					RunID:     evt.RunID(),
					SessionID: evt.SessionID(),
					Status:    status,
					UpdatedAt: ts,
					Labels:    evt.Labels,
					Metadata:  evt.Metadata,
				})
			case *hooks.RunResumedEvent:
				status = session.RunStatusRunning
				return rt.SessionStore.UpsertRun(ctx, session.RunMeta{
					AgentID:   evt.AgentID(),
					RunID:     evt.RunID(),
					SessionID: evt.SessionID(),
					Status:    status,
					UpdatedAt: ts,
					Labels:    evt.Labels,
				})
			case *hooks.RunCompletedEvent:
				switch evt.Status {
				case "success":
					status = session.RunStatusCompleted
				case "failed":
					status = session.RunStatusFailed
				case "canceled":
					status = session.RunStatusCanceled
				default:
					return fmt.Errorf("unexpected run completed status %q", evt.Status)
				}
				if evt.PublicError != "" {
					metadata = map[string]any{
						"public_error": evt.PublicError,
					}
					if evt.ErrorProvider != "" {
						metadata["error_provider"] = evt.ErrorProvider
					}
					if evt.ErrorOperation != "" {
						metadata["error_operation"] = evt.ErrorOperation
					}
					if evt.ErrorKind != "" {
						metadata["error_kind"] = evt.ErrorKind
					}
					if evt.ErrorCode != "" {
						metadata["error_code"] = evt.ErrorCode
					}
					if evt.HTTPStatus != 0 {
						metadata["http_status"] = evt.HTTPStatus
					}
					metadata["retryable"] = evt.Retryable
				}
				return rt.SessionStore.UpsertRun(ctx, session.RunMeta{
					AgentID:   evt.AgentID(),
					RunID:     evt.RunID(),
					SessionID: evt.SessionID(),
					Status:    status,
					UpdatedAt: ts,
					Metadata:  metadata,
				})
			default:
				return nil
			}
		})
		if _, err := bus.Register(sessionSub); err != nil {
			rt.logger.Warn(context.Background(), "failed to register session subscriber", "err", err)
		}
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
						"tool_call_id":            evt.ToolCallID,
						"parent_tool_call_id":     evt.ParentToolCallID,
						"tool_name":               evt.ToolName,
						"payload":                 evt.Payload,
						"queue":                   evt.Queue,
						"expected_children_total": evt.ExpectedChildrenTotal,
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
						"bounds":              evt.Bounds,
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
			case *hooks.ThinkingBlockEvent:
				memEvent = memory.Event{
					Type:      memory.EventThinking,
					Timestamp: time.UnixMilli(evt.Timestamp()),
					Data: map[string]any{
						"text":          evt.Text,
						"signature":     evt.Signature,
						"redacted":      evt.Redacted,
						"content_index": evt.ContentIndex,
						"final":         evt.Final,
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
		streamSub, err := stream.NewSubscriber(newHintingSink(rt, rt.Stream))
		if err != nil {
			rt.logger.Warn(context.Background(), "failed to create stream subscriber", "err", err)
		} else {
			rt.streamSubscriber = streamSub
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

// WithSessionStore sets the session store.
func WithSessionStore(s session.Store) RuntimeOption { return func(o *Options) { o.SessionStore = s } }

// WithRunEventStore sets the canonical run event store.
func WithRunEventStore(s runlog.Store) RuntimeOption { return func(o *Options) { o.RunEventStore = s } }

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

// WithToolConfirmation configures runtime-enforced confirmation for selected tools.
func WithToolConfirmation(cfg *ToolConfirmationConfig) RuntimeOption {
	return func(o *Options) { o.ToolConfirmation = cfg }
}

// WithWorker configures the worker for a specific agent. Engines that support
// worker polling use this configuration to bind the agent to a specific queue.
// If unspecified, a default worker configuration is used.
func WithWorker(id agent.Ident, cfg WorkerConfig) RuntimeOption {
	return func(o *Options) {
		if o.Workers == nil {
			o.Workers = make(map[agent.Ident]WorkerConfig)
		}
		o.Workers[id] = cfg
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
	if err := r.ensureHookActivityRegistered(ctx); err != nil {
		return err
	}

	// Apply per-agent worker overrides before engine registration.
	if cfg, ok := r.workers[reg.ID]; ok {
		if q := cfg.Queue; q != "" {
			reg.Workflow.TaskQueue = q
			reg.PlanActivityOptions.Queue = q
			reg.ResumeActivityOptions.Queue = q
		}
	}

	// Apply opinionated default timeouts when unspecified. These keep activities bounded
	// even when designs omit explicit values.
	if reg.PlanActivityOptions.Timeout == 0 {
		reg.PlanActivityOptions.Timeout = defaultPlanActivityTimeout
	}
	if reg.ResumeActivityOptions.Timeout == 0 {
		reg.ResumeActivityOptions.Timeout = defaultResumeActivityTimeout
	}
	if reg.ExecuteToolActivityOptions.Timeout == 0 {
		reg.ExecuteToolActivityOptions.Timeout = defaultExecuteToolActivityTimeout
	}

	// Register untyped workflow; Temporal adapter wraps with workflow.Context and
	// we coerce input to *RunInput inside WorkflowHandler. This preserves engine
	// boundaries and avoids leaking Temporal types here.
	if err := r.Engine.RegisterWorkflow(ctx, reg.Workflow); err != nil {
		return err
	}
	// Register typed activities for planner (start/resume) and execute_tool.
	if reg.PlanActivityName != "" {
		if err := r.Engine.RegisterPlannerActivity(ctx,
			reg.PlanActivityName,
			reg.PlanActivityOptions,
			r.PlanStartActivity); err != nil {
			return err
		}
	}
	if reg.ResumeActivityName != "" {
		if err := r.Engine.RegisterPlannerActivity(ctx,
			reg.ResumeActivityName,
			reg.ResumeActivityOptions,
			r.PlanResumeActivity,
		); err != nil {
			return err
		}
	}
	if reg.ExecuteToolActivity != "" {
		if err := r.Engine.RegisterExecuteToolActivity(ctx,
			reg.ExecuteToolActivity,
			reg.ExecuteToolActivityOptions,
			r.ExecuteToolActivity,
		); err != nil {
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
		r.agentToolSpecs[reg.ID] = cp
	}
	for _, ts := range reg.Toolsets {
		if err := validateAgentToolsetSpecs(ts); err != nil {
			r.mu.Unlock()
			return err
		}
		r.addToolsetLocked(ts)
	}
	r.mu.Unlock()

	return nil
}

func (r *Runtime) ensureHookActivityRegistered(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.hookActivityRegistered {
		return nil
	}
	opts := engine.ActivityOptions{
		Timeout: 15 * time.Second,
		RetryPolicy: engine.RetryPolicy{
			MaxAttempts: 1,
		},
	}
	if err := r.Engine.RegisterHookActivity(ctx, hookActivityName, opts, r.hookActivity); err != nil {
		return err
	}
	r.hookActivityRegistered = true
	return nil
}

// RegisterToolset registers a toolset outside of agent registration. Useful for
// feature modules that expose shared toolsets. Returns an error if required fields
// (Name, Execute) are missing.
func (r *Runtime) RegisterToolset(ts ToolsetRegistration) error {
	r.mu.RLock()
	if r.registrationClosed {
		r.mu.RUnlock()
		return ErrRegistrationClosed
	}
	r.mu.RUnlock()
	if ts.Name == "" {
		return errors.New("toolset name is required")
	}
	if ts.Execute == nil {
		return errors.New("toolset execute function is required")
	}
	if err := validateAgentToolsetSpecs(ts); err != nil {
		return err
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

func validateAgentToolsetSpecs(ts ToolsetRegistration) error {
	if ts.AgentTool == nil {
		return nil
	}
	if len(ts.Specs) == 0 {
		agentID := ""
		if ts.AgentTool != nil {
			agentID = string(ts.AgentTool.AgentID)
		}
		if agentID != "" {
			return fmt.Errorf("%w: agent toolset %q (agent=%s) requires tool specs/codecs", ErrInvalidConfig, ts.Name, agentID)
		}
		return fmt.Errorf("%w: agent toolset %q requires tool specs/codecs", ErrInvalidConfig, ts.Name)
	}
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

// ModelClient returns a registered model client by ID, if present.
// Callers should check the boolean return to confirm presence.
func (r *Runtime) ModelClient(id string) (model.Client, bool) {
	if id == "" {
		return nil, false
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	m, ok := r.models[id]
	return m, ok
}

// BedrockConfig configures the bedrock-backed model client created by the runtime.
type BedrockConfig struct {
	DefaultModel   string
	HighModel      string
	SmallModel     string
	MaxTokens      int
	ThinkingBudget int
	Temperature    float32
}

// NewBedrockModelClient constructs a model.Client backed by AWS Bedrock using the
// runtime's own ledger access. The caller supplies the AWS Bedrock runtime client
// and model configuration; the runtime wires the appropriate ledger source (Temporal
// workflow query or in-memory no-op) so the client can rehydrate messages by RunID.
func (r *Runtime) NewBedrockModelClient(awsrt *bedrockruntime.Client, cfg BedrockConfig) (model.Client, error) {
	opts := bedrock.Options{
		Runtime:        awsrt,
		DefaultModel:   cfg.DefaultModel,
		HighModel:      cfg.HighModel,
		SmallModel:     cfg.SmallModel,
		MaxTokens:      cfg.MaxTokens,
		ThinkingBudget: cfg.ThinkingBudget,
		Temperature:    cfg.Temperature,
		Logger:         r.logger,
	}
	if eng, ok := r.Engine.(*engtemporal.Engine); ok {
		return bedrock.New(awsrt, opts, bedrock.NewTemporalLedgerSource(eng.TemporalClient()))
	}
	// Engines without durable queries: construct without ledger rehydration.
	return bedrock.New(awsrt, opts, nil)
}

// agentByID returns the registered agent by ID if present. The boolean indicates
// whether the agent was found. Intended for internal/runtime use and codegen.
func (r *Runtime) agentByID(id agent.Ident) (AgentRegistration, bool) {
	r.mu.RLock()
	agent, ok := r.agents[id]
	r.mu.RUnlock()
	return agent, ok
}

// ExecuteAgentChildWithRoute starts a provider agent as a child workflow using the
// explicit route metadata (workflow name and task queue). The child executes its own
// plan/execute loop and returns a RunOutput which is adapted by callers.
func (r *Runtime) ExecuteAgentChildWithRoute(
	wfCtx engine.WorkflowContext,
	route AgentRoute,
	messages []*model.Message,
	nestedRunCtx run.Context,
) (*RunOutput, error) {
	if route.ID == "" || route.WorkflowName == "" || route.DefaultTaskQueue == "" {
		return nil, fmt.Errorf("child route is incomplete")
	}
	input := RunInput{
		AgentID:          route.ID,
		RunID:            nestedRunCtx.RunID,
		SessionID:        nestedRunCtx.SessionID,
		TurnID:           nestedRunCtx.TurnID,
		ParentToolCallID: nestedRunCtx.ParentToolCallID,
		ParentRunID:      nestedRunCtx.ParentRunID,
		ParentAgentID:    nestedRunCtx.ParentAgentID,
		Tool:             nestedRunCtx.Tool,
		ToolArgs:         nestedRunCtx.ToolArgs,
		Messages:         messages,
		Labels:           nestedRunCtx.Labels,
	}
	handle, err := wfCtx.StartChildWorkflow(wfCtx.Context(), engine.ChildWorkflowRequest{
		ID:        input.RunID,
		Workflow:  route.WorkflowName,
		TaskQueue: route.DefaultTaskQueue,
		Input:     &input,
		// RunTimeout left to engine defaults; parent may cap via policy if desired.
	})
	if err != nil {
		return nil, err
	}
	out, err := handle.Get(wfCtx.Context())
	if err != nil {
		return nil, err
	}
	return out, nil
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
		input.AgentID = route.ID
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
		input.RunID = generateRunID(string(input.AgentID))
	}
	if strings.TrimSpace(input.SessionID) == "" {
		return nil, ErrMissingSessionID
	}
	sess, err := r.SessionStore.LoadSession(ctx, input.SessionID)
	if err != nil {
		return nil, err
	}
	if sess.Status == session.StatusEnded {
		return nil, session.ErrSessionEnded
	}
	req := engine.WorkflowStartRequest{
		ID:        input.RunID,
		Workflow:  workflowName,
		TaskQueue: defaultQueue,
		Input:     input,
	}
	// Compute an engine-level TTL for the workflow to prevent indefinite runs.
	// Use agent/run policy when available and cap by a hard maximum.
	{
		const hardCap = 15 * time.Minute
		// Headroom ensures the workflow can publish terminal events and complete
		// deterministic cleanup even when finalization runs right up to the deadline.
		const headroom = 30 * time.Second
		var (
			policyBudget time.Duration
			grace        time.Duration
			resumeTO     time.Duration
		)
		if input.Policy != nil && input.Policy.TimeBudget > 0 {
			policyBudget = input.Policy.TimeBudget
			grace = input.Policy.FinalizerGrace
			resumeTO = input.Policy.PlanTimeout
		} else if reg, ok := r.agentByID(input.AgentID); ok {
			policyBudget = reg.Policy.TimeBudget
			grace = reg.Policy.FinalizerGrace
			resumeTO = reg.ResumeActivityOptions.Timeout
			if input.Policy != nil && input.Policy.PlanTimeout > 0 {
				resumeTO = input.Policy.PlanTimeout
			}
		}
		if grace == 0 {
			grace = defaultFinalizerGrace
		}
		if resumeTO == 0 {
			// Keep this aligned with the Temporal engine default activity timeout.
			resumeTO = time.Minute
		}
		if grace < resumeTO {
			grace = resumeTO
		}
		req.RunTimeout = hardCap
		if policyBudget > 0 {
			if t := policyBudget + grace + headroom; t > 0 && t < req.RunTimeout {
				req.RunTimeout = t
			}
		}
	}
	if opts := input.WorkflowOptions; opts != nil {
		if opts.TaskQueue != "" {
			req.TaskQueue = opts.TaskQueue
		}
		req.Memo = cloneMetadata(opts.Memo)
		req.SearchAttributes = cloneMetadata(opts.SearchAttributes)
		// Convert API retry policy to engine retry policy.
		rp := engine.RetryPolicy{
			MaxAttempts:        opts.RetryPolicy.MaxAttempts,
			InitialInterval:    opts.RetryPolicy.InitialInterval,
			BackoffCoefficient: opts.RetryPolicy.BackoffCoefficient,
		}
		if !isZeroRetryPolicy(rp) {
			req.RetryPolicy = rp
		}
	}
	if req.SearchAttributes == nil {
		req.SearchAttributes = make(map[string]any, 1)
	}
	if v, ok := req.SearchAttributes["SessionID"]; ok && v != input.SessionID {
		return nil, fmt.Errorf("workflow search attribute SessionID=%v does not match session id %q", v, input.SessionID)
	}
	req.SearchAttributes["SessionID"] = input.SessionID
	now := time.Now().UTC()
	if err := r.SessionStore.UpsertRun(ctx, session.RunMeta{
		AgentID:   string(input.AgentID),
		RunID:     input.RunID,
		SessionID: input.SessionID,
		Status:    session.RunStatusPending,
		StartedAt: now,
		UpdatedAt: now,
		Labels:    cloneLabels(input.Labels),
		Metadata:  cloneMetadata(input.Metadata),
	}); err != nil {
		return nil, err
	}
	handle, err := r.Engine.StartWorkflow(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkflowStartFailed, err)
	}
	r.storeWorkflowHandle(input.RunID, handle)
	return handle, nil
}

// CancelRun requests cancellation of the workflow identified by runID.
//
// Cancellation must work across process restarts, so it is implemented via the
// engine's cancel-by-ID capability rather than relying on in-process workflow
// handles.
//
// CancelRun is idempotent: if the workflow does not exist (already completed,
// canceled, or never started), CancelRun returns nil.
func (r *Runtime) CancelRun(ctx context.Context, runID string) error {
	if runID == "" {
		return errors.New("run id is required")
	}
	canceler, ok := r.Engine.(engine.Canceler)
	if !ok || canceler == nil {
		return fmt.Errorf("engine does not support cancel-by-id")
	}
	if err := canceler.CancelByID(ctx, runID); err != nil {
		if errors.Is(err, engine.ErrWorkflowNotFound) {
			return nil
		}
		return err
	}
	return nil
}

// PauseRun requests the underlying workflow to pause via the standard pause signal.
// Returns an error if the run is unknown or signaling fails.
func (r *Runtime) PauseRun(ctx context.Context, req interrupt.PauseRequest) error {
	if req == nil {
		return errors.New("pause request is required")
	}
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
	if req == nil {
		return errors.New("resume request is required")
	}
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
	if ans == nil {
		return errors.New("clarification answer is required")
	}
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
	if rs == nil {
		return errors.New("tool results set is required")
	}
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

// ProvideConfirmation sends a typed confirmation decision to a waiting run.
func (r *Runtime) ProvideConfirmation(ctx context.Context, dec interrupt.ConfirmationDecision) error {
	if dec == nil {
		return errors.New("confirmation decision is required")
	}
	if strings.TrimSpace(dec.RunID) == "" {
		return errors.New("run id is required")
	}
	if s, ok := r.Engine.(engine.Signaler); ok {
		return s.SignalByID(ctx, dec.RunID, "", interrupt.SignalProvideConfirmation, dec)
	}
	handle, ok := r.workflowHandle(dec.RunID)
	if !ok {
		return fmt.Errorf("run %q not found", dec.RunID)
	}
	return handle.Signal(ctx, interrupt.SignalProvideConfirmation, dec)
}

// ListRunEvents returns a forward page of canonical run events for the given run.
func (r *Runtime) ListRunEvents(ctx context.Context, runID, cursor string, limit int) (runlog.Page, error) {
	return r.RunEventStore.List(ctx, runID, cursor, limit)
}

// GetRunSnapshot derives a compact snapshot of the run state by replaying the
// canonical run log.
func (r *Runtime) GetRunSnapshot(ctx context.Context, runID string) (*run.Snapshot, error) {
	const pageSize = 512

	var (
		cursor = ""
		events []*runlog.Event
	)
	for {
		page, err := r.RunEventStore.List(ctx, runID, cursor, pageSize)
		if err != nil {
			return nil, err
		}
		events = append(events, page.Events...)
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	return newRunSnapshot(events)
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
		out = append(out, id)
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

// addReminder registers a reminder for the given run. It is a no-op when the
// reminders engine is not configured.
func (r *Runtime) addReminder(runID string, rem reminder.Reminder) {
	if r.reminders == nil || runID == "" {
		return
	}
	r.reminders.AddReminder(runID, rem)
}

// removeReminder removes a reminder by ID for the given run. It is a no-op
// when the reminders engine is not configured.
func (r *Runtime) removeReminder(runID, id string) {
	if r.reminders == nil || runID == "" || id == "" {
		return
	}
	r.reminders.RemoveReminder(runID, id)
}

// ToolSchema returns the parsed JSON schema for the tool payload when available.
func (r *Runtime) ToolSchema(name tools.Ident) (map[string]any, bool) {
	r.mu.RLock()
	spec, ok := r.toolSpecs[name]
	r.mu.RUnlock()
	if !ok || len(spec.Payload.Schema) == 0 {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(spec.Payload.Schema, &m); err != nil {
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
	reg, ok := r.agents[agentID]
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
	if delta.FinalizerGrace > 0 {
		reg.Policy.FinalizerGrace = delta.FinalizerGrace
	}
	if delta.InterruptsAllowed {
		reg.Policy.InterruptsAllowed = true
	}
	r.agents[agentID] = reg
	return nil
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
