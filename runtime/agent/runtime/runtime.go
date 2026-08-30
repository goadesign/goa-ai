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
// with a durable workflow engine, one host-provided runtime store, and a
// durable memory store.
//
// Example usage: use AgentClient for execution.
//
//	rt := runtime.New(runtimeStore, runtime.WithEngine(temporalEngine))
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
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	openaisdk "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
	bedrock "goa.design/goa-ai/features/model/bedrock"
	openai "goa.design/goa-ai/features/model/openai"
	vertexprovider "goa.design/goa-ai/features/model/vertex"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/memory"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/reminder"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	rthints "goa.design/goa-ai/runtime/agent/runtime/hints"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
	"google.golang.org/genai"
)

type (
	// HintOverrideFunc can override the call hint for a tool invocation.
	//
	// Contract:
	//   - Returning (hint, true) selects hint as the DisplayHint, even when a DSL
	//     template exists.
	//   - Returning ("", false) indicates no override applies and the runtime should
	//     use its default behavior.
	//   - The payload value is the typed payload decoded via the tool payload codec
	//     when possible; it may be nil when decoding fails.
	HintOverrideFunc func(ctx context.Context, tool tools.Ident, payload any) (hint string, ok bool)

	// ToolMetadataLookup resolves canonical policy metadata for a tool identifier.
	// Generated registrations should provide this so the runtime can reuse
	// generation-time metadata instead of reconstructing it from specs.
	ToolMetadataLookup func(name tools.Ident) (policy.ToolMetadata, bool)

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
		// PromptRegistry resolves prompt specs and optional scoped overrides.
		PromptRegistry *prompt.Registry
		// Store persists run lifecycle state, continuation checkpoints, and
		// ordered run records in one transaction boundary.
		Store storage.Store
		// Policy evaluates allowlists and caps per planner turn.
		Policy policy.Engine
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

		// captureGenAIMessages records full chat message payloads on chat-turn
		// model spans when enabled via WithCaptureGenAIMessages.
		captureGenAIMessages bool

		mu        sync.RWMutex
		agents    map[agent.Ident]AgentRegistration
		toolsets  map[string]ToolsetRegistration
		toolSpecs map[tools.Ident]tools.ToolSpec
		// toolsetNames maps each global tool name to the one registered toolset
		// that executes it in this runtime.
		toolsetNames map[tools.Ident]string
		// policyToolMetadata stores canonical per-tool policy metadata resolved at
		// registration time from generated lookups or, for manual registrations,
		// derived once from the supplied specs.
		policyToolMetadata map[tools.Ident]policy.ToolMetadata
		// parsed tool payload schemas cached by tool name for hint building
		toolSchemas map[string]map[string]any
		models      map[string]model.Client

		// Per-agent tool specs registered during agent registration for introspection.
		agentToolSpecs map[agent.Ident][]tools.ToolSpec

		// registrationMu serializes agent and toolset registration so one global
		// tool name cannot change contract between validation and storage.
		registrationMu sync.Mutex

		// registrationClosed prevents late agent/toolset registration after the
		// runtime has been explicitly sealed or the first run has been submitted,
		// avoiding dynamic handler registration on active workers.
		registrationClosed bool

		// activationComplete reports whether the runtime activation boundary has
		// completed successfully. Failed Seal attempts leave registration closed
		// but keep this false so callers may retry activation.
		activationComplete bool

		// sealMu serializes activation attempts so repeated Seal calls cannot race
		// and a failed attempt may be retried deterministically.
		sealMu sync.Mutex

		// storageActivityRegistered tracks whether the runtime storage activity has
		// been registered with the engine.
		storageActivityRegistered bool
		// agentChildActivityRegistered tracks whether child prompt preparation is
		// available to every registered workflow.
		agentChildActivityRegistered bool

		// storageActivityTimeout overrides the StartToClose timeout used for
		// `runtime.store`. Zero means use the runtime default.
		storageActivityTimeout time.Duration

		// reminders manages run-scoped system reminders used for backstage
		// guidance (safety, correctness, workflow) injected into prompts by
		// planners. It is internal to the runtime; planners interact with it
		// via PlannerContext.
		reminders *reminder.Engine

		// toolConfirmation configures runtime-enforced confirmation for selected tools.
		// It is used to require explicit operator approval before executing certain tools.
		// See ToolConfirmationConfig for details.
		toolConfirmation *ToolConfirmationConfig

		hintOverrides map[tools.Ident]HintOverrideFunc
	}

	// Options configures the Runtime instance. All fields are optional except Engine
	// for production deployments. Noop implementations are substituted for nil Logger,
	// Metrics, and Tracer. A default in-memory event bus is created if Hooks is nil.
	Options struct {
		// Engine is the workflow backend adapter (Temporal by default).
		Engine engine.Engine
		// MemoryStore persists run transcripts and annotations.
		MemoryStore memory.Store
		// PromptStore resolves scoped prompt overrides. When nil, prompt rendering
		// uses baseline registered PromptSpecs only.
		PromptStore prompt.Store
		// Policy evaluates allowlists and caps per planner turn.
		Policy policy.Engine
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

		// CaptureGenAIMessages records full input and output chat message payloads
		// on chat-turn model spans. Reasoning content is never captured. The
		// attributes can contain user content, tool arguments, and PII; keep
		// disabled unless explicitly troubleshooting.
		CaptureGenAIMessages bool

		// StorageActivityTimeout overrides the StartToClose timeout for
		// `runtime.store`. Zero means use the runtime default.
		StorageActivityTimeout time.Duration

		// ToolConfirmation configures runtime-enforced confirmation overrides for selected
		// tools (for example, requiring explicit operator approval before executing
		// additional tools that are not marked with design-time Confirmation).
		ToolConfirmation *ToolConfirmationConfig

		// HintOverrides optionally overrides DSL-authored call hints for specific tools
		// when streaming tool_start events.
		HintOverrides map[tools.Ident]HintOverrideFunc
	}

	// RuntimeOption configures the runtime via functional options passed to NewWith.
	RuntimeOption func(*Options)

	// AgentRegistration bundles the generated assets for an agent. This struct is
	// produced by codegen and passed to RegisterAgent to make an agent available
	// for execution.
	AgentRegistration struct {
		// Definition is the immutable generated contract shared with callers.
		Definition AgentDefinition
		// Planner is the concrete planner implementation for the agent.
		Planner planner.Planner
		// WorkflowHandler runs the agent workflow on the local worker.
		WorkflowHandler engine.WorkflowFunc
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
		// Policy configures caps, time budgets, and missing-field behavior for the agent.
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

		// ToolMetadataLookup resolves canonical policy metadata for tools in this
		// registration. Generated toolsets should provide this so runtime policy
		// evaluation can consume generation-time metadata directly.
		ToolMetadataLookup ToolMetadataLookup

		// Execute invokes the concrete tool implementation for a given tool call.
		// Returns the durable tool result plus an optional current-batch user
		// clarification request owned by the runtime.
		//
		// For service-based tools, codegen generates this function to call service clients.
		// For agent-tools (Exports), generated registrations set Inline=true and
		// populate AgentTool so the workflow runtime can start nested agents as child
		// workflows and adapt their RunOutput into a ToolResult.
		// For custom/server-side tools, users provide their own implementation.
		Execute func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error)

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
		// keyed by tool ident. The runtime installs these with the toolset so sinks
		// can render concise, domain-authored labels.
		CallHints map[tools.Ident]*template.Template

		// ResultHints optionally provides precompiled templates for result previews
		// keyed by tool ident. Templates receive the runtime-owned preview wrapper
		// where semantic data is available under `.Result` and bounded metadata
		// under `.Bounds`. The runtime installs these with the toolset so sinks can
		// render concise result previews.
		ResultHints map[tools.Ident]*template.Template

		// ResultMaterializer enriches typed tool results before the runtime encodes
		// them for hooks, workflow boundaries, or callers. When nil, the runtime
		// publishes the tool result exactly as produced by the executor.
		ResultMaterializer ResultMaterializer

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

	// RunPolicy configures per-agent runtime behavior (caps and time budgets).
	// These values are evaluated during workflow execution to enforce limits and prevent
	// runaway tool loops or budget overruns.
	RunPolicy struct {
		// MaxToolCalls caps the total number of budgeted (non-bookkeeping) tool
		// invocations per run. Zero means the cap is not configured.
		MaxToolCalls int

		// MaxRecoveryTurns caps consecutive additional planner activities
		// scheduled to recover rejected tool or model output. Successful budgeted
		// tool work resets the count. Zero uses the runtime default of three.
		MaxRecoveryTurns int

		// TimeBudget is the active-time budget for planner and tool work within
		// the run (0 = unlimited). The workflow runtime enforces this deadline;
		// time between continuation workflows does not consume it and no engine
		// run timeout is derived.
		TimeBudget time.Duration

		// FinalizerGrace reserves time to produce a last assistant message after
		// TimeBudget is exhausted. Zero uses the runtime default.
		FinalizerGrace time.Duration

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
	// MissingFieldsAwaitClarification ends the workflow with a user clarification request.
	MissingFieldsAwaitClarification MissingFieldsAction = "await_clarification"
	// MissingFieldsResume instructs the runtime to continue and surface hints to the planner.
	MissingFieldsResume MissingFieldsAction = "resume"
)

const (
	// Opinionated defaults applied when activity timeouts are unspecified.
	defaultPlanActivityTimeout        = 2 * time.Minute
	defaultResumeActivityTimeout      = 2 * time.Minute
	defaultExecuteToolActivityTimeout = 2 * time.Minute
	defaultAgentChildActivityTimeout  = 2 * time.Minute
	defaultStorageActivityTimeout     = 15 * time.Second
)

// defaultRetriedActivityPolicy returns the runtime's standard infrastructure
// retry policy for activities whose logical work is now replay-safe by
// contract. Planner/tool business errors still surface in typed results rather
// than escaping as activity failures.
func defaultRetriedActivityPolicy() engine.RetryPolicy {
	return engine.RetryPolicy{
		MaxAttempts:        3,
		InitialInterval:    time.Second,
		BackoffCoefficient: 2,
	}
}

var (
	// Typed error sentinels for common invalid states.
	ErrAgentNotFound       = errors.New("agent not found")
	ErrEngineNotConfigured = errors.New("runtime engine not configured")
	ErrInvalidConfig       = errors.New("invalid configuration")
	ErrMissingSessionID    = errors.New("session id is required")
	ErrSessionNotAllowed   = errors.New("session id is not allowed")
	// ErrContinuationRejected means the saved checkpoint or submitted response
	// cannot start a successor workflow. The runtime returns it before asking the
	// workflow engine to start that successor.
	ErrContinuationRejected = errors.New("continuation rejected before workflow start")
	ErrWorkflowStartFailed  = errors.New("workflow start failed")
	ErrRegistrationClosed   = errors.New("registration closed after first run")
	ErrMissingLabels        = errors.New("run start: missing required labels")
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

// WithRenderedPrompts records prompts whose rendered text is already included
// in the run input. The accepted workflow stores them before planner work.
func WithRenderedPrompts(events []prompt.RenderEvent) RunOption {
	return func(in *RunInput) {
		in.RenderedPrompts = clonePromptRenderEvents(events)
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
// Budget is the semantic run budget; Plan and Tools are attempt budgets. Zero-
// valued fields are ignored.
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

// WithLimitTerminalPlans sets the complete terminal tool-call set used when
// this run reaches a configured time, tool-call, or recovery-turn limit.
func WithLimitTerminalPlans(plans LimitTerminalPlans) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.LimitTerminalPlans = cloneLimitTerminalPlans(&plans)
	}
}

// WithRunMaxToolCalls sets a per-run cap on total tool executions.
// The caller must supply a positive override value; omit the option to use the
// agent's design default.
func WithRunMaxToolCalls(n int) RunOption {
	if n <= 0 {
		panic("runtime: WithRunMaxToolCalls requires n > 0")
	}
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.MaxToolCalls = n
	}
}

// WithRunMaxRecoveryTurns caps consecutive replacement planner activities for
// one run. The caller must supply a positive override value; omit the option
// to use the agent's design default.
func WithRunMaxRecoveryTurns(n int) RunOption {
	if n <= 0 {
		panic("runtime: WithRunMaxRecoveryTurns requires n > 0")
	}
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.MaxRecoveryTurns = n
	}
}

// WithRunCompletionTool requires one budgeted tool to succeed before the run
// can complete. Its first successful execution ends the run without a final
// planner response; attempts and correctable failures retain normal cap and
// recovery semantics.
func WithRunCompletionTool(id tools.Ident) RunOption {
	if id == "" {
		panic("runtime: WithRunCompletionTool requires a tool identifier")
	}
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.CompletionTool = id
	}
}

// WithRunTimeBudget sets the active-time budget for planner and tool work.
// Time between continuation workflows does not consume the budget, and the
// runtime does not derive an engine run timeout. Zero means no override.
func WithRunTimeBudget(d time.Duration) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.TimeBudget = d
	}
}

// WithRunFinalizerGrace reserves time to produce a final assistant message after
// the run's semantic TimeBudget is exhausted. Zero means no override.
func WithRunFinalizerGrace(d time.Duration) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.FinalizerGrace = d
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

// WithTagPolicyClauses sets explicit tag-policy clauses on the run policy.
func WithTagPolicyClauses(clauses []TagPolicyClause) RunOption {
	return func(in *RunInput) {
		if in.Policy == nil {
			in.Policy = &PolicyOverrides{}
		}
		in.Policy.TagClauses = cloneTagPolicyClauses(clauses)
	}
}

// newFromOptions constructs a Runtime using the provided options. Internal helper
// used by the public New(...RuntimeOption) constructor.
func newFromOptions(store storage.Store, opts Options) *Runtime {
	if store == nil {
		panic("runtime: storage store is required")
	}
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
	rt := &Runtime{
		Engine:                 eng,
		Memory:                 opts.MemoryStore,
		PromptRegistry:         prompt.NewRegistry(opts.PromptStore),
		Store:                  store,
		Policy:                 opts.Policy,
		Bus:                    bus,
		Stream:                 opts.Stream,
		storageActivityTimeout: opts.StorageActivityTimeout,
		logger:                 logger,
		metrics:                metrics,
		tracer:                 tracer,
		captureGenAIMessages:   opts.CaptureGenAIMessages,
		agents:                 make(map[agent.Ident]AgentRegistration),
		toolsets:               make(map[string]ToolsetRegistration),
		toolSpecs:              make(map[tools.Ident]tools.ToolSpec),
		toolsetNames:           make(map[tools.Ident]string),
		policyToolMetadata:     make(map[tools.Ident]policy.ToolMetadata),
		toolSchemas:            make(map[string]map[string]any),
		models:                 make(map[string]model.Client),
		agentToolSpecs:         make(map[agent.Ident][]tools.ToolSpec),
		reminders:              reminder.NewEngine(),
		toolConfirmation:       opts.ToolConfirmation,
		hintOverrides:          opts.HintOverrides,
	}
	// Install runtime-owned toolsets before any agent registration so planners
	// and transcripts can rely on a stable tool vocabulary.
	rt.mu.Lock()
	rt.addToolsetLocked(toolUnavailableToolsetRegistration())
	rt.mu.Unlock()
	if _, err := bus.Register(hooks.SubscriberFunc(rt.recordGenAITelemetryEvent)); err != nil {
		panic(fmt.Errorf("register GenAI telemetry subscriber: %w", err))
	}
	if rt.Memory != nil {
		memSub := hooks.SubscriberFunc(func(ctx context.Context, event hooks.Event) error {
			var memEvent memory.Event
			switch evt := event.(type) {
			case *hooks.ToolCallScheduledEvent:
				memEvent = memory.NewEvent(time.UnixMilli(evt.Timestamp()), memory.ToolCallData{
					ToolCallID:            evt.ToolCallID,
					ParentToolCallID:      evt.ParentToolCallID,
					ToolName:              evt.ToolName,
					PayloadJSON:           evt.Payload,
					Queue:                 evt.Queue,
					ExpectedChildrenTotal: evt.ExpectedChildrenTotal,
				}, nil)
				return rt.Memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.ToolResultReceivedEvent:
				errorMessage := ""
				if evt.Failure != nil {
					errorMessage = evt.Failure.Error.Error()
				}
				memEvent = memory.NewEvent(time.UnixMilli(evt.Timestamp()), memory.ToolResultData{
					ToolCallID:       evt.ToolCallID,
					ParentToolCallID: evt.ParentToolCallID,
					ToolName:         evt.ToolName,
					ResultJSON:       evt.ResultJSON,
					Preview:          evt.ResultPreview,
					Bounds:           evt.Bounds,
					Duration:         evt.Duration,
					ErrorMessage:     errorMessage,
				}, nil)
				return rt.Memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.AssistantMessageEvent:
				memEvent = memory.NewEvent(time.UnixMilli(evt.Timestamp()), memory.AssistantMessageData{
					Message:    evt.Message,
					Structured: evt.Structured,
				}, nil)
				return rt.Memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.ThinkingBlockEvent:
				memEvent = memory.NewEvent(time.UnixMilli(evt.Timestamp()), memory.ThinkingData{
					Text:         evt.Text,
					Signature:    evt.Signature,
					Redacted:     evt.Redacted,
					ContentIndex: evt.ContentIndex,
					Final:        evt.Final,
				}, nil)
				return rt.Memory.AppendEvents(ctx, evt.AgentID(), evt.RunID(), memEvent)
			case *hooks.PlannerNoteEvent:
				memEvent = memory.NewEvent(time.UnixMilli(evt.Timestamp()), memory.PlannerNoteData{
					Note: evt.Note,
				}, evt.Labels)
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

// New constructs a Runtime using the required host-owned store and functional
// options. It installs an in-memory engine, no-op telemetry, and an in-process
// event bus when callers do not provide them.
func New(store storage.Store, opts ...RuntimeOption) *Runtime {
	var o Options
	for _, fn := range opts {
		if fn != nil {
			fn(&o)
		}
	}
	return newFromOptions(store, o)
}

// WithEngine sets the workflow engine.
func WithEngine(e engine.Engine) RuntimeOption { return func(o *Options) { o.Engine = e } }

// WithStorageActivityTimeout sets the StartToClose timeout for `runtime.store`.
// This activity applies every runtime Store command outside workflow code.
//
// d must be greater than zero.
func WithStorageActivityTimeout(d time.Duration) RuntimeOption {
	if d <= 0 {
		panic("runtime: storage activity timeout must be greater than zero")
	}
	return func(o *Options) { o.StorageActivityTimeout = d }
}

// WithMemoryStore sets the memory store.
func WithMemoryStore(m memory.Store) RuntimeOption { return func(o *Options) { o.MemoryStore = m } }

// WithPromptStore sets the prompt override store.
func WithPromptStore(s prompt.Store) RuntimeOption { return func(o *Options) { o.PromptStore = s } }

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

// WithCaptureGenAIMessages enables recording of full input and output chat
// message payloads on chat-turn model spans. Reasoning content is never
// captured. The captured attributes can contain user content, tool arguments,
// and PII, so callers must opt in explicitly and should never enable this by
// default in production.
func WithCaptureGenAIMessages(enabled bool) RuntimeOption {
	return func(o *Options) { o.CaptureGenAIMessages = enabled }
}

// WithToolConfirmation configures runtime-enforced confirmation for selected tools.
func WithToolConfirmation(cfg *ToolConfirmationConfig) RuntimeOption {
	return func(o *Options) { o.ToolConfirmation = cfg }
}

// WithHintOverrides configures per-tool call hint overrides.
//
// When provided, overrides take precedence over DSL-authored CallHint templates
// when streaming tool_start events. Only tools present in the map are considered.
func WithHintOverrides(m map[tools.Ident]HintOverrideFunc) RuntimeOption {
	return func(o *Options) { o.HintOverrides = m }
}

// Seal closes the registration phase and activates engines that stage worker
// handlers until the runtime is fully configured. Worker deployments should call
// Seal after registering all toolsets and agents, before serving traffic. When
// the engine supports staged workers, Seal returns only after activation
// succeeds or ctx ends.
//
// Successful Seal calls are idempotent. The first call closes registration so
// later RegisterAgent/RegisterToolset calls fail fast even if activation later
// fails. Callers may retry Seal after a context-limited activation failure.
func (r *Runtime) Seal(ctx context.Context) error {
	r.registrationMu.Lock()
	defer r.registrationMu.Unlock()

	r.mu.Lock()
	alreadyActivated := r.activationComplete
	r.registrationClosed = true
	r.mu.Unlock()
	if alreadyActivated {
		return nil
	}

	r.sealMu.Lock()
	defer r.sealMu.Unlock()

	r.mu.Lock()
	if r.activationComplete {
		r.mu.Unlock()
		return nil
	}
	r.registrationClosed = true
	if err := r.validateAgentExecutionSupportLocked(); err != nil {
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()

	if sealer, ok := r.Engine.(engine.RegistrationSealer); ok {
		if err := sealer.SealRegistration(ctx); err != nil {
			return err
		}
	}

	r.mu.Lock()
	r.activationComplete = true
	r.mu.Unlock()
	return nil
}

// RegisterAgent validates the registration, registers workflows and activities with
// the engine, and stores the agent metadata for later lookup. Returns an error if
// required fields are missing or if engine registration fails.
//
// All agents must be registered before workflows can be started. Generated code
// calls this during initialization.
func (r *Runtime) RegisterAgent(ctx context.Context, reg AgentRegistration) error {
	r.registrationMu.Lock()
	defer r.registrationMu.Unlock()

	r.mu.RLock()
	if r.registrationClosed {
		r.mu.RUnlock()
		return ErrRegistrationClosed
	}
	r.mu.RUnlock()
	if !reg.Definition.valid() {
		return fmt.Errorf("%w: missing agent definition", ErrInvalidConfig)
	}
	if reg.Planner == nil {
		return fmt.Errorf("%w: missing planner", ErrInvalidConfig)
	}
	if reg.WorkflowHandler == nil {
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
	if err := validateRunPolicy(reg.Policy); err != nil {
		return err
	}
	if err := validateSpecs(reg.Definition.specs, reg.Definition.metadataFor); err != nil {
		return err
	}
	if err := r.validateToolSpecRegistrations(toolSpecRegistration{
		specs:  reg.Definition.specs,
		lookup: reg.Definition.metadataFor,
	}); err != nil {
		return err
	}
	if r.Engine == nil {
		return ErrEngineNotConfigured
	}
	if err := r.ensureStorageActivityRegistered(ctx); err != nil {
		return err
	}
	if err := r.ensureAgentChildActivityRegistered(ctx); err != nil {
		return err
	}

	// Apply runtime-owned attempt defaults after queue rebasing. Engine-specific
	// queue-wait and liveness mechanics are derived inside the engine adapter.
	if reg.PlanActivityOptions.StartToCloseTimeout == 0 {
		reg.PlanActivityOptions.StartToCloseTimeout = defaultPlanActivityTimeout
	}
	if reg.ResumeActivityOptions.StartToCloseTimeout == 0 {
		reg.ResumeActivityOptions.StartToCloseTimeout = defaultResumeActivityTimeout
	}
	if reg.ExecuteToolActivityOptions.StartToCloseTimeout == 0 {
		reg.ExecuteToolActivityOptions.StartToCloseTimeout = defaultExecuteToolActivityTimeout
	}

	// Register untyped workflow; Temporal adapter wraps with workflow.Context and
	// we coerce input to *RunInput inside WorkflowHandler. This preserves engine
	// boundaries and avoids leaking Temporal types here.
	if err := r.Engine.RegisterWorkflow(ctx, engine.WorkflowDefinition{
		Name:      reg.Definition.route.WorkflowName,
		TaskQueue: reg.Definition.route.DefaultTaskQueue,
		Handler:   reg.WorkflowHandler,
	}); err != nil {
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
	r.agents[reg.Definition.route.ID] = reg
	r.addToolSpecsLocked(reg.Definition.specs, reg.Definition.metadataFor)
	if len(reg.Definition.specs) > 0 {
		// store a shallow copy to avoid external mutation
		cp := make([]tools.ToolSpec, len(reg.Definition.specs))
		copy(cp, reg.Definition.specs)
		r.agentToolSpecs[reg.Definition.route.ID] = cp
	}
	r.mu.Unlock()

	return nil
}

// validateAgentExecutionSupportLocked proves that every tool an agent may
// execute has one registered toolset before workers begin polling.
func (r *Runtime) validateAgentExecutionSupportLocked() error {
	for _, registration := range r.agents {
		for _, name := range registration.Definition.executableTools {
			if _, ok := r.toolsetNames[name]; !ok {
				return fmt.Errorf(
					"%w: agent %q has no executor for tool %q",
					ErrInvalidConfig,
					registration.Definition.route.ID,
					name,
				)
			}
		}
	}
	return nil
}

func (r *Runtime) ensureStorageActivityRegistered(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.storageActivityRegistered {
		return nil
	}
	timeout := defaultStorageActivityTimeout
	if r.storageActivityTimeout > 0 {
		timeout = r.storageActivityTimeout
	}
	opts := engine.ActivityOptions{
		StartToCloseTimeout: timeout,
		RetryPolicy:         defaultRetriedActivityPolicy(),
	}
	if opts.StartToCloseTimeout == 0 {
		opts.StartToCloseTimeout = timeout
	}
	if err := r.Engine.RegisterStorageActivity(ctx, storageActivityName, opts, r.executeStorageCommand); err != nil {
		return err
	}
	r.storageActivityRegistered = true
	return nil
}

// ensureAgentChildActivityRegistered installs the one runtime-owned activity
// that prepares child inputs for all agent workflows.
func (r *Runtime) ensureAgentChildActivityRegistered(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.agentChildActivityRegistered {
		return nil
	}
	opts := engine.ActivityOptions{
		StartToCloseTimeout: defaultAgentChildActivityTimeout,
		RetryPolicy:         defaultRetriedActivityPolicy(),
	}
	if err := r.Engine.RegisterAgentChildActivity(ctx, agentChildActivityName, opts, r.prepareAgentChildActivity); err != nil {
		return err
	}
	r.agentChildActivityRegistered = true
	return nil
}

// RegisterToolset registers a toolset outside of agent registration. Feature
// modules use it to expose shared tools. Agent-tool specs are accepted only
// with inline child-agent execution, matching agent identities, and a complete
// child-workflow route. Any invalid spec rejects the whole registration.
func (r *Runtime) RegisterToolset(ts ToolsetRegistration) error {
	r.registrationMu.Lock()
	defer r.registrationMu.Unlock()

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
	if err := validateToolsetSpecs(ts); err != nil {
		return err
	}
	r.mu.RLock()
	_, exists := r.toolsets[ts.Name]
	r.mu.RUnlock()
	if exists {
		return fmt.Errorf("%w: toolset %q is already registered", ErrInvalidConfig, ts.Name)
	}
	if err := r.validateToolsetRoutes(ts); err != nil {
		return err
	}
	if err := r.validateToolSpecRegistrations(toolSpecRegistration{
		specs:  ts.Specs,
		lookup: ts.ToolMetadataLookup,
	}); err != nil {
		return err
	}
	ts = cloneToolsetRegistration(ts)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.addToolsetLocked(ts)
	return nil
}

func validateToolsetSpecs(ts ToolsetRegistration) error {
	if err := validateSpecs(ts.Specs, ts.ToolMetadataLookup); err != nil {
		return err
	}
	if err := validateAgentToolRegistration(ts); err != nil {
		return err
	}
	if ts.AgentTool == nil {
		return nil
	}
	if len(ts.Specs) == 0 {
		agentID := ""
		if ts.AgentTool != nil && ts.AgentTool.Definition.valid() {
			agentID = string(ts.AgentTool.Definition.route.ID)
		}
		if agentID != "" {
			return fmt.Errorf("%w: agent toolset %q (agent=%s) requires tool specs/codecs", ErrInvalidConfig, ts.Name, agentID)
		}
		return fmt.Errorf("%w: agent toolset %q requires tool specs/codecs", ErrInvalidConfig, ts.Name)
	}
	return nil
}

// validateAgentToolRegistration requires every spec and its toolset execution
// configuration to agree on whether the tool runs a child agent. It rejects the
// whole registration when any agent tool lacks one complete child-workflow
// route. Generated specs identify agent tools explicitly; names and toolset
// labels do not determine this behavior.
func validateAgentToolRegistration(ts ToolsetRegistration) error {
	for _, spec := range ts.Specs {
		if ts.AgentTool != nil && !spec.IsAgentTool {
			return fmt.Errorf(
				"%w: agent toolset %q requires tool %q to be marked as an agent tool",
				ErrInvalidConfig,
				ts.Name,
				spec.Name,
			)
		}
		if !spec.IsAgentTool {
			continue
		}
		if ts.AgentTool == nil {
			return fmt.Errorf(
				"%w: agent tool %q requires agent-tool execution configuration",
				ErrInvalidConfig,
				spec.Name,
			)
		}
		if !ts.Inline {
			return fmt.Errorf(
				"%w: agent tool %q requires inline child-agent execution",
				ErrInvalidConfig,
				spec.Name,
			)
		}
		if spec.AgentID == "" {
			return fmt.Errorf(
				"%w: agent tool %q requires a generated agent id",
				ErrInvalidConfig,
				spec.Name,
			)
		}
		cfg := ts.AgentTool
		if !cfg.Definition.valid() {
			return fmt.Errorf(
				"%w: agent tool %q requires a generated agent definition",
				ErrInvalidConfig,
				spec.Name,
			)
		}
		specAgentID := agent.Ident(spec.AgentID)
		if specAgentID != cfg.Definition.route.ID {
			return fmt.Errorf(
				"%w: agent tool %q agent id %q does not match definition %q",
				ErrInvalidConfig,
				spec.Name,
				specAgentID,
				cfg.Definition.route.ID,
			)
		}
	}
	return nil
}

// validateToolsetRoutes rejects two local executors for the same global tool
// name. A planner call names only the tool, so the runtime could not choose
// between different registrations.
func (r *Runtime) validateToolsetRoutes(ts ToolsetRegistration) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, spec := range ts.Specs {
		if registered, ok := r.toolsetNames[spec.Name]; ok && registered != ts.Name {
			return fmt.Errorf(
				"%w: tool %q is already executed by toolset %q and cannot also be registered by %q",
				ErrInvalidConfig,
				spec.Name,
				registered,
				ts.Name,
			)
		}
	}
	return nil
}

func validateSpecs(specs []tools.ToolSpec, lookup ToolMetadataLookup) error {
	for _, spec := range specs {
		if IsGeneratedContinuationToolName(spec.Name) {
			return fmt.Errorf(
				"%w: tool name %q matches the runtime-generated continuation format %q followed by %d lowercase hexadecimal characters",
				ErrInvalidConfig,
				spec.Name,
				continuationToolNamePrefix,
				continuationToolNameHexLength,
			)
		}
		if spec.TerminalRun && !spec.Bookkeeping {
			return fmt.Errorf("%w: terminal tool %q must also declare bookkeeping", ErrInvalidConfig, spec.Name)
		}
		if lookup == nil {
			if strings.TrimSpace(defaultToolTitle(spec.Name)) == "" {
				return fmt.Errorf("%w: tool %q must have a non-empty display title", ErrInvalidConfig, spec.Name)
			}
			continue
		}
		meta, ok := lookup(spec.Name)
		if !ok {
			return fmt.Errorf("%w: missing policy metadata for tool %q", ErrInvalidConfig, spec.Name)
		}
		if meta.ID != spec.Name {
			return fmt.Errorf("%w: policy metadata id %q does not match tool %q", ErrInvalidConfig, meta.ID, spec.Name)
		}
		if strings.TrimSpace(meta.Title) == "" {
			return fmt.Errorf("%w: policy metadata title for tool %q is required", ErrInvalidConfig, spec.Name)
		}
		if meta.BudgetClass != toolBudgetClass(spec.Bookkeeping) {
			return fmt.Errorf(
				"%w: policy metadata budget class %q does not match tool %q bookkeeping=%t",
				ErrInvalidConfig,
				meta.BudgetClass,
				spec.Name,
				spec.Bookkeeping,
			)
		}
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
	if err := model.ValidateClient(client); err != nil {
		return fmt.Errorf("register model %q: %w", id, err)
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
	// DefaultModel is the primary model identifier used for default-class requests.
	DefaultModel string
	// HighModel is the model identifier used for high-reasoning requests.
	HighModel string
	// SmallModel is the model identifier used for small/cheap requests.
	SmallModel string
	// MaxTokens is the default completion token cap.
	MaxTokens int
	// Temperature is the default sampling temperature.
	Temperature float32
}

// OpenAIConfig configures the OpenAI-backed model client created by the runtime.
type OpenAIConfig struct {
	// APIKey authenticates requests to the OpenAI-compatible endpoint.
	APIKey string
	// BaseURL optionally overrides the default OpenAI API base URL.
	BaseURL string
	// DefaultModel is the primary model identifier used for default-class requests.
	DefaultModel string
	// HighModel is the model identifier used for high-reasoning requests.
	HighModel string
	// SmallModel is the model identifier used for small/cheap requests.
	SmallModel string
	// MaxTokens is the default completion token cap.
	MaxTokens int
	// Temperature is the default sampling temperature.
	Temperature float32
	// ThinkingEffort selects the OpenAI reasoning effort for thinking-enabled requests.
	ThinkingEffort string
}

// VertexConfig configures the Vertex-backed model clients created by the
// runtime. ProjectID and Location identify the Vertex endpoint; model IDs
// are provider-specific (Gemini model names for the Gemini factory, Vertex
// Claude publisher IDs for the Anthropic factory).
type VertexConfig struct {
	// ProjectID is the GCP project hosting the Vertex AI endpoint.
	ProjectID string
	// Location is the Vertex AI region (or "global") serving the endpoint.
	Location string
	// DefaultModel is the primary model identifier used for default-class requests.
	DefaultModel string
	// HighModel is the model identifier used for high-reasoning requests.
	HighModel string
	// SmallModel is the model identifier used for small/cheap requests.
	SmallModel string
	// MaxTokens is the default completion token cap.
	MaxTokens int
	// ThinkingBudget is the default thinking-token budget.
	ThinkingBudget int
	// Temperature is the default sampling temperature.
	Temperature float32
}

// NewBedrockModelClient constructs a model.Client backed by AWS Bedrock.
// Callers must supply the complete canonical transcript in Request.Messages.
func (r *Runtime) NewBedrockModelClient(awsrt *bedrockruntime.Client, cfg BedrockConfig) (model.Client, error) {
	opts := bedrock.Options{
		Runtime:      awsrt,
		DefaultModel: cfg.DefaultModel,
		HighModel:    cfg.HighModel,
		SmallModel:   cfg.SmallModel,
		MaxTokens:    cfg.MaxTokens,
		Temperature:  cfg.Temperature,
		Logger:       r.logger,
	}
	return bedrock.New(awsrt, opts)
}

// NewOpenAIModelClient constructs a model.Client backed by the OpenAI Responses
// API using runtime-owned client construction. Callers must supply the complete
// canonical transcript in Request.Messages.
func (r *Runtime) NewOpenAIModelClient(cfg OpenAIConfig) (model.Client, error) {
	apiKey := strings.TrimSpace(cfg.APIKey)
	if apiKey == "" {
		return nil, errors.New("openai: api key is required")
	}
	requestOptions := []option.RequestOption{
		option.WithAPIKey(apiKey),
	}
	if baseURL := strings.TrimSpace(cfg.BaseURL); baseURL != "" {
		requestOptions = append(requestOptions, option.WithBaseURL(baseURL))
	}
	client := openaisdk.NewClient(requestOptions...)
	service := client.Responses
	return openai.New(openai.Options{
		Client:              &service,
		DefaultModel:        cfg.DefaultModel,
		HighModel:           cfg.HighModel,
		SmallModel:          cfg.SmallModel,
		MaxCompletionTokens: cfg.MaxTokens,
		Temperature:         cfg.Temperature,
		ThinkingEffort:      cfg.ThinkingEffort,
	})
}

// NewVertexGeminiModelClient creates a Gemini-on-Vertex model client using
// Application Default Credentials. The client is not registered; call
// RegisterModel with the returned client.
func (r *Runtime) NewVertexGeminiModelClient(ctx context.Context, cfg VertexConfig) (model.Client, error) {
	if strings.TrimSpace(cfg.ProjectID) == "" {
		return nil, errors.New("vertex: project id is required")
	}
	if strings.TrimSpace(cfg.Location) == "" {
		return nil, errors.New("vertex: location is required")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Backend:  genai.BackendVertexAI,
		Project:  cfg.ProjectID,
		Location: cfg.Location,
	})
	if err != nil {
		return nil, err
	}
	return vertexprovider.New(client.Models, vertexprovider.Options{
		DefaultModel:   cfg.DefaultModel,
		HighModel:      cfg.HighModel,
		SmallModel:     cfg.SmallModel,
		MaxTokens:      cfg.MaxTokens,
		Temperature:    cfg.Temperature,
		ThinkingBudget: cfg.ThinkingBudget,
	})
}

// NewVertexAnthropicModelClient creates a Claude-on-Vertex model client
// using Application Default Credentials. The client is not registered; call
// RegisterModel with the returned client.
func (r *Runtime) NewVertexAnthropicModelClient(ctx context.Context, cfg VertexConfig) (model.Client, error) {
	return vertexprovider.NewAnthropicClient(ctx, vertexprovider.AnthropicOptions{
		ProjectID:      cfg.ProjectID,
		Region:         cfg.Location,
		DefaultModel:   cfg.DefaultModel,
		HighModel:      cfg.HighModel,
		SmallModel:     cfg.SmallModel,
		MaxTokens:      cfg.MaxTokens,
		Temperature:    float64(cfg.Temperature),
		ThinkingBudget: int64(cfg.ThinkingBudget),
	})
}

// agentByID returns the registered agent by ID if present. The boolean indicates
// whether the agent was found. Intended for internal/runtime use and codegen.
func (r *Runtime) agentByID(id agent.Ident) (AgentRegistration, bool) {
	r.mu.RLock()
	agent, ok := r.agents[id]
	r.mu.RUnlock()
	return agent, ok
}

// ExecuteAgentChild starts a provider agent as a child workflow using its
// generated definition. The child executes its own plan and tool loop and
// returns the output adapted by the caller.
func (r *Runtime) ExecuteAgentChild(
	wfCtx engine.WorkflowContext,
	definition AgentDefinition,
	messages []*model.Message,
	nestedRunCtx run.Context,
) (*RunOutput, error) {
	return r.executeAgentChild(wfCtx, definition, agentChildRequest{
		messages:   messages,
		runContext: nestedRunCtx,
	})
}

// executeAgentChild starts one fully assembled child request. Prompt versions
// travel with the rendered messages and are stored by the child.
func (r *Runtime) executeAgentChild(
	wfCtx engine.WorkflowContext,
	definition AgentDefinition,
	request agentChildRequest,
) (*RunOutput, error) {
	input, err := agentChildRunInput(definition, request)
	if err != nil {
		return nil, err
	}
	route := definition.route
	handle, err := wfCtx.StartChildWorkflow(wfCtx.Context(), engine.ChildWorkflowRequest{
		ID:        input.RunID,
		Workflow:  route.WorkflowName,
		TaskQueue: route.DefaultTaskQueue,
		Input:     input,
		// RunTimeout left to engine defaults; parent may cap via policy if desired.
	})
	if err != nil {
		return nil, err
	}
	out, err := handle.Get(wfCtx.Detached().Context())
	if err != nil {
		return nil, err
	}
	if err := validateWorkflowOutput(out, route.ID, input.RunID); err != nil {
		return nil, err
	}
	return out, nil
}

// agentChildRunInput builds and validates the immutable input submitted for a
// child workflow. Every child start path uses this function before asking the
// workflow engine to accept the child ID.
func agentChildRunInput(definition AgentDefinition, request agentChildRequest) (*RunInput, error) {
	if !definition.valid() {
		return nil, errors.New("child agent definition is required")
	}
	nested := request.runContext
	if nested.ParentRunID == "" || nested.ParentAgentID == "" ||
		nested.ParentToolCallID == "" || nested.Tool == "" {
		return nil, errors.New("child run context is incomplete")
	}
	if err := validateRequiredLabels(definition, nested.Labels); err != nil {
		return nil, err
	}
	return &RunInput{
		AgentID:          definition.route.ID,
		RunID:            nested.RunID,
		SessionID:        nested.SessionID,
		TurnID:           nested.TurnID,
		ParentToolCallID: nested.ParentToolCallID,
		ParentRunID:      nested.ParentRunID,
		ParentAgentID:    nested.ParentAgentID,
		Tool:             nested.Tool,
		ToolArgs:         nested.ToolArgs,
		Messages:         request.messages,
		RenderedPrompts:  clonePromptRenderEvents(request.renderedPrompts),
		Labels:           nested.Labels,
	}, nil
}

// startRunWithDefinition contains common start logic for local and remote
// clients. Both use the same generated definition.
//
// When requireSession is true, the caller must provide a stable run ID and an
// active session. The accepted workflow writes running or canceled lifecycle
// metadata from its first durable record after the engine accepts the start.
//
// When requireSession is false, the run is one-shot: SessionID must stay empty,
// its runtime records do not belong to a session, and no SessionID search
// attribute is set.
func (r *Runtime) startRunWithDefinition(ctx context.Context, input *RunInput, definition AgentDefinition, requireSession bool) (engine.WorkflowHandle, error) {
	if !definition.valid() {
		return nil, fmt.Errorf("%w: invalid agent definition", ErrAgentNotFound)
	}
	if input.AgentID == "" {
		input.AgentID = definition.route.ID
	}
	if input.AgentID != definition.route.ID {
		return nil, fmt.Errorf("%w: input agent %q does not match definition %q", ErrAgentNotFound, input.AgentID, definition.route.ID)
	}
	// Close registration on first run submission so local start paths cannot race
	// later handler mutations. Worker deployments should call Seal during startup;
	// local starters still converge on the same sealed contract here.
	if err := r.Seal(ctx); err != nil {
		return nil, err
	}
	if err := validateWorkflowRunInput(input); err != nil {
		if input != nil && input.Continuation != nil {
			return nil, continuationContractError(err)
		}
		return nil, err
	}
	if requireSession {
		if strings.TrimSpace(input.SessionID) == "" {
			return nil, ErrMissingSessionID
		}
	} else if strings.TrimSpace(input.SessionID) != "" {
		return nil, ErrSessionNotAllowed
	}
	if input.RunID == "" {
		if requireSession {
			return nil, errors.New("run id is required for a sessionful workflow")
		}
		input.RunID = generateRunID(string(input.AgentID))
	}
	if err := transcript.ValidatePlannerTranscript(input.Messages); err != nil {
		return nil, fmt.Errorf("runtime: invalid transcript: %w", err)
	}
	runLabels := input.Labels
	effectivePolicy := input.Policy
	if input.Continuation != nil {
		checkpoint, err := prepareContinuation(input, definition)
		if err != nil {
			return nil, continuationContractError(err)
		}
		runLabels = checkpoint.Context.Labels
		effectivePolicy = checkpoint.Policy
	}
	if err := validateRequiredLabels(definition, runLabels); err != nil {
		if input.Continuation != nil {
			return nil, continuationContractError(err)
		}
		return nil, err
	}
	if effectivePolicy != nil {
		if err := validateCompletionToolPolicyForDefinition(definition, effectivePolicy); err != nil {
			if input.Continuation != nil {
				return nil, continuationContractError(err)
			}
			return nil, err
		}
		if err := validateLimitTerminalPlansForDefinition(definition, effectivePolicy.LimitTerminalPlans); err != nil {
			if input.Continuation != nil {
				return nil, continuationContractError(err)
			}
			return nil, err
		}
	}
	req := engine.WorkflowStartRequest{
		ID:        input.RunID,
		Workflow:  definition.route.WorkflowName,
		TaskQueue: definition.route.DefaultTaskQueue,
		Input:     input,
		// RunTimeout is intentionally left zero (engine-unbounded): active-time
		// enforcement is owned by the workflow's Budget and Hard deadlines
		// (run_timing.go, workflow_loop.go). External-input requests end the
		// workflow and store the remaining durations for the next workflow, so an
		// engine-level ceiling would only add a competing mid-turn deadline.
	}
	if opts := input.WorkflowOptions; opts != nil {
		if opts.TaskQueue != "" {
			req.TaskQueue = opts.TaskQueue
		}
		req.Memo = cloneMetadata(opts.Memo)
		req.SearchAttributes = cloneMetadata(opts.SearchAttributes)
	}
	if requireSession {
		if v, ok := req.SearchAttributes["SessionID"]; ok && v != input.SessionID {
			return nil, fmt.Errorf("workflow search attribute SessionID=%v does not match session id %q", v, input.SessionID)
		}
	} else if req.SearchAttributes != nil {
		if _, ok := req.SearchAttributes["SessionID"]; ok {
			return nil, fmt.Errorf("workflow search attribute SessionID is not allowed for one-shot runs")
		}
	}
	handle, err := r.Engine.StartWorkflow(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrWorkflowStartFailed, err)
	}
	return handle, nil
}

// validateRequiredLabels fails fast, before any workflow or activity runs,
// when the caller-supplied run labels omit a key that a label-backed
// Inject() field requires. The generated definition carries the complete list
// to local workers and remote callers, so both reject the same invalid start.
func validateRequiredLabels(definition AgentDefinition, labels map[string]string) error {
	if len(definition.requiredLabels) == 0 {
		return nil
	}
	var missing []string
	for _, key := range definition.requiredLabels {
		if _, ok := labels[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%w: agent %q requires label(s) %v; call WithLabels(...) with these keys when starting the run",
		ErrMissingLabels, definition.route.ID, missing)
}

// CancelRun requests cancellation of the workflow identified by req.RunID.
//
// Cancellation must work across process restarts, so the engine sends the
// request to the workflow identified by RunID. The workflow stores the reason
// before the engine stops it.
//
// CancelRun is idempotent: if the workflow does not exist (already completed,
// canceled, or never started), CancelRun returns nil.
func (r *Runtime) CancelRun(ctx context.Context, req CancelRequest) error {
	if req.RunID == "" {
		return errors.New("run id is required")
	}
	if req.Reason == "" {
		return errors.New("cancel reason is required")
	}
	requester, ok := r.Engine.(engine.CancellationRequester)
	if !ok || requester == nil {
		return errors.New("engine does not support durable cancellation")
	}
	err := requester.RequestCancellation(ctx, engine.CancellationRequest{
		RunID:  req.RunID,
		Reason: req.Reason,
	})
	if err == nil {
		return nil
	}
	var conflict *engine.CancellationConflictError
	if errors.As(err, &conflict) {
		return &CancellationReasonConflictError{RunID: req.RunID, Reason: req.Reason}
	}
	if errors.Is(err, engine.ErrWorkflowNotFound) || errors.Is(err, engine.ErrWorkflowCompleted) {
		// A closed engine history cannot accept another command. The durable run
		// state distinguishes an already settled run from an active run whose
		// workflow disappeared unexpectedly.
		meta, loadErr := r.Store.LoadRun(ctx, req.RunID)
		if errors.Is(loadErr, session.ErrRunNotFound) {
			return nil
		}
		if loadErr != nil {
			return loadErr
		}
		if !session.IsTerminalRunStatus(meta.Status) {
			return fmt.Errorf("runtime: active run %q has no engine workflow: %w", req.RunID, err)
		}
		if meta.CancellationReason != "" && meta.CancellationReason != req.Reason {
			return &CancellationReasonConflictError{RunID: req.RunID, Reason: req.Reason}
		}
		return nil
	}
	return err
}

// ListRunEvents returns a forward page of canonical run events for the given run.
func (r *Runtime) ListRunEvents(ctx context.Context, runID, cursor string, limit int) (runlog.Page, error) {
	return r.Store.ListRunRecords(ctx, runID, cursor, limit)
}

// GetRunSnapshot derives a compact snapshot of the run state by replaying the
// canonical run log.
func (r *Runtime) GetRunSnapshot(ctx context.Context, runID string) (*run.Snapshot, error) {
	return r.loadRunSnapshot(ctx, runID)
}

// loadRunSnapshot replays the canonical run log without changing stored state.
func (r *Runtime) loadRunSnapshot(ctx context.Context, runID string) (*run.Snapshot, error) {
	const pageSize = 512

	var (
		cursor = ""
		events []*runlog.Event
	)
	for {
		page, err := r.Store.ListRunRecords(ctx, runID, cursor, pageSize)
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

// isTerminalRunEventType reports the two durable events that permanently end
// one workflow execution.
func isTerminalRunEventType(eventType runlog.Type) bool {
	return eventType == hooks.RunCompleted || eventType == hooks.RunSuspended
}

// addToolsetLocked registers a toolset, specs, metadata, and hints without
// acquiring the lock.
// Caller must hold r.mu.
func (r *Runtime) addToolsetLocked(ts ToolsetRegistration) {
	r.toolsets[ts.Name] = ts
	if r.toolsetNames == nil {
		r.toolsetNames = make(map[tools.Ident]string)
	}
	for _, spec := range ts.Specs {
		r.toolsetNames[spec.Name] = ts.Name
	}
	r.addToolSpecsLocked(ts.Specs, ts.ToolMetadataLookup)
	if len(ts.CallHints) > 0 {
		rthints.RegisterCallHints(ts.CallHints)
	}
	if len(ts.ResultHints) > 0 {
		rthints.RegisterResultHints(ts.ResultHints)
	}
}

// addToolSpecsLocked registers tool specs without acquiring the lock.
// Caller must hold r.mu.
func (r *Runtime) addToolSpecsLocked(specs []tools.ToolSpec, lookup ToolMetadataLookup) {
	if r.toolSpecs == nil {
		r.toolSpecs = make(map[tools.Ident]tools.ToolSpec)
	}
	if r.policyToolMetadata == nil {
		r.policyToolMetadata = make(map[tools.Ident]policy.ToolMetadata)
	}
	for _, spec := range specs {
		if _, exists := r.toolSpecs[spec.Name]; exists {
			continue
		}
		r.toolSpecs[spec.Name] = spec
		r.policyToolMetadata[spec.Name] = canonicalToolMetadata(spec, lookup)
	}
}

// toolSpec retrieves a tool spec by fully qualified name. Thread-safe.
func (r *Runtime) toolSpec(name tools.Ident) (tools.ToolSpec, bool) {
	r.mu.RLock()
	spec, ok := r.toolSpecs[name]
	r.mu.RUnlock()
	return spec, ok
}

// toolsetForTool returns the executable registration that owns name in this
// runtime.
func (r *Runtime) toolsetForTool(name tools.Ident) (string, ToolsetRegistration, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	toolsetName, ok := r.toolsetNames[name]
	if !ok {
		return "", ToolsetRegistration{}, false
	}
	toolset, ok := r.toolsets[toolsetName]
	return toolsetName, toolset, ok
}

func (r *Runtime) policyMetadata(name tools.Ident) (policy.ToolMetadata, bool) {
	r.mu.RLock()
	meta, ok := r.policyToolMetadata[name]
	r.mu.RUnlock()
	if ok {
		return meta, true
	}
	if _, ok := r.toolSpec(name); !ok {
		return policy.ToolMetadata{}, false
	}
	panic(fmt.Sprintf("runtime: missing canonical policy metadata for tool %q", name))
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

// ToolSpec returns a detached snapshot of the registered ToolSpec for the given
// tool name. Mutating the returned spec does not change runtime behavior.
func (r *Runtime) ToolSpec(name tools.Ident) (tools.ToolSpec, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	spec, ok := r.toolSpecs[name]
	if !ok {
		return tools.ToolSpec{}, false
	}
	return cloneToolSpec(spec), true
}

// ToolSpecsForAgent returns detached snapshots of the ToolSpecs registered by
// the given agent. Mutating the returned specs does not change runtime behavior.
func (r *Runtime) ToolSpecsForAgent(agentID agent.Ident) []tools.ToolSpec {
	r.mu.RLock()
	defer r.mu.RUnlock()
	specs := r.agentToolSpecs[agentID]
	if len(specs) == 0 {
		return nil
	}
	return cloneToolSpecs(specs)
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
// Only non-zero fields are applied. Overrides affect
// subsequent runs and are local to this runtime instance.
func (r *Runtime) OverridePolicy(agentID agent.Ident, delta RunPolicy) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := validateMaxRecoveryTurns(delta.MaxRecoveryTurns); err != nil {
		return err
	}
	reg, ok := r.agents[agentID]
	if !ok {
		return ErrAgentNotFound
	}
	if delta.MaxToolCalls > 0 {
		reg.Policy.MaxToolCalls = delta.MaxToolCalls
	}
	if delta.MaxRecoveryTurns > 0 {
		reg.Policy.MaxRecoveryTurns = delta.MaxRecoveryTurns
	}
	if delta.TimeBudget > 0 {
		reg.Policy.TimeBudget = delta.TimeBudget
	}
	if delta.FinalizerGrace > 0 {
		reg.Policy.FinalizerGrace = delta.FinalizerGrace
	}
	r.agents[agentID] = reg
	return nil
}

// validateRunPolicy rejects configuration values outside the runtime-owned
// transition vocabulary before an agent registration becomes executable.
func validateRunPolicy(policy RunPolicy) error {
	if err := validateMaxRecoveryTurns(policy.MaxRecoveryTurns); err != nil {
		return err
	}
	switch policy.OnMissingFields {
	case "", MissingFieldsFinalize, MissingFieldsAwaitClarification, MissingFieldsResume:
		return nil
	default:
		return fmt.Errorf("%w: unknown missing-fields action %q", ErrInvalidConfig, policy.OnMissingFields)
	}
}

// validateMaxRecoveryTurns accepts zero as an omitted public setting and
// rejects negative values. Registration resolves omission to the runtime
// default; per-run and in-process overrides leave the current setting unchanged.
func validateMaxRecoveryTurns(value int) error {
	if value < 0 {
		return fmt.Errorf("%w: max recovery turns cannot be negative", ErrInvalidConfig)
	}
	return nil
}
