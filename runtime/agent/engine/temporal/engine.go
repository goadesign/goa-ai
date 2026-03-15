// Package temporal implements the engine.Engine adapter backed by Temporal.
// It registers workflows and activities, manages per-queue workers, starts
// executions, and exposes workflow handles for waiting, signaling, and
// cancellation. The adapter wires OpenTelemetry tracing/metrics and keeps
// Temporal-specific worker lifecycle inside this package.
package temporal

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// Options configures the Temporal engine adapter. Either a pre-configured Client
// or ClientOptions must be provided. The adapter automatically wires OTEL
// instrumentation.
//
// Use NewWorker when the process will register workflows/activities and poll task
// queues locally. Use NewClient when the process only needs Temporal client
// capabilities such as starting workflows, querying status, or signaling runs.
type Options struct {
	// Client is an optional pre-configured Temporal client. If nil, the adapter
	// creates a lazy client using ClientOptions, allowing automatic OTEL interceptor
	// installation. Provide a pre-configured client when you need custom interceptors
	// or connection pooling.
	Client client.Client

	// ClientOptions describe how to construct the Temporal client when Client is nil.
	// Required when Client is nil. Only connection-related fields (HostPort, Namespace,
	// etc.) need to be set; OTEL interceptors are configured automatically.
	ClientOptions *client.Options

	// WorkerOptions configures worker defaults for task queue, concurrency, and
	// identity. NewWorker requires TaskQueue to be set and creates one worker per
	// unique task queue. NewClient ignores this field.
	WorkerOptions WorkerOptions

	// ActivityDefaults configures Temporal-owned execution mechanics for each
	// activity class. These defaults apply only when the runtime registration did
	// not already specify the corresponding engine.ActivityOptions field, so
	// semantic attempt budgets remain owned by the runtime while queue-wait and
	// liveness stay adapter-specific.
	ActivityDefaults ActivityDefaults

	// Instrumentation toggles OTEL tracing and metrics for the Temporal client and workers.
	// Tracing and metrics are enabled by default. Set DisableTracing or DisableMetrics to
	// opt out. Customize interceptor behavior via TracerOptions and MetricsOptions.
	Instrumentation InstrumentationOptions

	// Logger emits workflow and worker logs. If nil, a noop logger is used (no output).
	// Provide a logger to observe workflow execution, worker health, and activity progress.
	Logger telemetry.Logger

	// Metrics records workflow-level metrics (execution counts, latencies, failures).
	// If nil, a noop metrics recorder is used. Provide an implementation to emit metrics
	// to your observability stack.
	Metrics telemetry.Metrics

	// Tracer creates workflow-level spans for distributed tracing. If nil, a noop tracer
	// is used. Provide an implementation to emit traces to your observability backend.
	Tracer telemetry.Tracer
}

// WorkerOptions configures the shared worker settings applied to all task queues
// managed by the engine. When workflows or activities target different queues, the
// engine creates one worker per unique queue, each using these shared settings.
//
// TaskQueue is required and defines the default queue used when workflow/activity
// definitions omit a queue specification. The Options field provides fine-grained
// control over worker behavior (concurrency, identity, interceptors) and is forwarded
// directly to Temporal's worker.New constructor.
type WorkerOptions struct {
	// TaskQueue is the default queue name used when workflow/activity definitions
	// omit a queue. Required - at least one default queue must be configured.
	TaskQueue string

	// Options are passed directly to Temporal's worker.New constructor for controlling
	// worker behavior: concurrency limits, worker identity, custom interceptors, etc.
	// Refer to Temporal SDK documentation for available options.
	Options worker.Options
}

// ActivityDefaults groups Temporal-specific defaults for each registered
// activity class. Planner defaults apply to both PlanStart and PlanResume
// activities because both represent planner attempts from the runtime's point
// of view.
type ActivityDefaults struct {
	// Hook configures workflow hook publishing activities.
	Hook ActivityTimeoutDefaults
	// Planner configures PlanStart and PlanResume activities.
	Planner ActivityTimeoutDefaults
	// Tool configures ExecuteTool activities.
	Tool ActivityTimeoutDefaults
}

// ActivityTimeoutDefaults configures the Temporal-only mechanics that sit
// around a runtime-owned activity attempt budget.
type ActivityTimeoutDefaults struct {
	// QueueWaitTimeout bounds how long a scheduled activity may wait for a worker
	// before Temporal times out the task.
	QueueWaitTimeout time.Duration
	// LivenessTimeout bounds the maximum gap between runtime-emitted heartbeats
	// before Temporal concludes the worker attempt is no longer healthy.
	LivenessTimeout time.Duration
}

// InstrumentationOptions configures how the engine wires OpenTelemetry (OTEL)
// tracing and metrics into the Temporal client and workers. By default, both
// tracing and metrics are enabled automatically using OTEL interceptors provided
// by the Temporal SDK.
//
// Set DisableTracing or DisableMetrics to opt out of automatic instrumentation.
// Use TracerOptions and MetricsOptions to customize the OTEL interceptor behavior
// (e.g., span attributes, metric namespaces, sampling). Refer to Temporal's OTEL
// contrib documentation for available customization options.
type InstrumentationOptions struct {
	// DisableTracing skips installing the OTEL tracing interceptor on the client
	// and workers. When false (default), distributed traces are automatically emitted
	// for workflow/activity executions.
	DisableTracing bool

	// DisableMetrics skips installing the OTEL metrics handler on the client and
	// workers. When false (default), workflow/activity metrics (counts, latencies,
	// failures) are automatically emitted.
	DisableMetrics bool

	// TracerOptions is retained for source compatibility but is ignored by the
	// engine's trace-domain implementation. Traces are emitted by goa-ai's own
	// activity interceptors (new-root spans + OTel links).
	TracerOptions temporalotel.TracerOptions

	// MetricsOptions customize the OTEL metrics handler (metric names, labels, etc.).
	// Only used when DisableMetrics is false. Refer to Temporal SDK OTEL docs.
	MetricsOptions temporalotel.MetricsHandlerOptions
}

// Engine implements engine.Engine using Temporal as the durable execution backend.
// It manages workflow/activity registration, per-queue worker lifecycle, and
// workflow execution handles. Worker-mode engines stage registrations until the
// runtime seals registration, at which point polling begins with a fully
// configured runtime registry.
//
// Thread-safety: All methods are safe for concurrent use. Internal state is protected
// by mutexes.
//
// Lifecycle:
//   - Construct via NewWorker() for worker processes or NewClient() for client-only
//     processes.
//   - Register workflows/activities only on worker engines.
//   - Call engine.SealRegistration via runtime.Seal() once all local registrations
//     are complete so polling begins from a coherent registry.
//   - Call Close() to gracefully stop workers and release the Temporal client when
//     owned here.
type Engine struct {
	client      client.Client
	closeClient bool

	workerMode bool

	defaultQueue     string
	workerOpts       worker.Options
	activityDefaults ActivityDefaults

	logger  telemetry.Logger
	metrics telemetry.Metrics
	tracer  telemetry.Tracer

	mu                sync.Mutex
	registrationSealed bool
	workers           map[string]*workerBundle
	workflows         map[string]engine.WorkflowDefinition
	pendingWorkflows  map[string]struct{}
	activityOptions   map[string]engine.ActivityOptions
	pendingActivities map[string]struct{}

	workflowContexts sync.Map // runID -> engine.WorkflowContext
}

// NewClient constructs a Temporal engine for client-only processes. Client-mode
// engines can start workflows, query status, and signal runs, but they reject
// workflow/activity registration because they do not own workers.
func NewClient(opts Options) (*Engine, error) {
	return newEngine(opts, false)
}

// NewWorker constructs a Temporal engine for worker processes. Worker-mode
// engines accept workflow/activity registration and begin polling only after
// registration has been sealed.
func NewWorker(opts Options) (*Engine, error) {
	return newEngine(opts, true)
}

func newEngine(opts Options, workerMode bool) (*Engine, error) {
	defaultQueue := opts.WorkerOptions.TaskQueue
	if workerMode && defaultQueue == "" {
		return nil, fmt.Errorf("temporal engine: worker options must include a default task queue")
	}
	logger := opts.Logger
	if logger == nil {
		logger = telemetry.NewNoopLogger()
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = telemetry.NewNoopMetrics()
	}
	tracer := opts.Tracer
	if tracer == nil {
		tracer = telemetry.NewNoopTracer()
	}

	inst := configureInstrumentation(opts.Instrumentation)

	cli := opts.Client
	closeClient := false
	if cli == nil {
		if opts.ClientOptions == nil {
			return nil, fmt.Errorf("temporal engine: client options are required when Client is nil")
		}
		clientOpts := *opts.ClientOptions
		applyClientInstrumentation(&clientOpts, inst)
		lazyClient, err := client.NewLazyClient(clientOpts)
		if err != nil {
			return nil, fmt.Errorf("temporal engine: create client: %w", err)
		}
		cli = lazyClient
		closeClient = true
	}

	workerOpts := opts.WorkerOptions.Options
	if workerMode {
		applyWorkerInstrumentation(&workerOpts, inst)
	}

	e := &Engine{
		client:            cli,
		closeClient:       closeClient,
		workerMode:        workerMode,
		defaultQueue:      defaultQueue,
		workerOpts:        workerOpts,
		activityDefaults:  opts.ActivityDefaults,
		logger:            logger,
		metrics:           metrics,
		tracer:            tracer,
		workers:           make(map[string]*workerBundle),
		workflows:         make(map[string]engine.WorkflowDefinition),
		pendingWorkflows:  make(map[string]struct{}),
		activityOptions:   make(map[string]engine.ActivityOptions),
		pendingActivities: make(map[string]struct{}),
	}
	return e, nil
}

// RegisterWorkflow registers a workflow definition with the Temporal worker for
// the specified task queue. The workflow handler is wrapped to provide the engine's
// WorkflowContext abstraction and lifecycle management (context creation/cleanup).
//
// The workflow's TaskQueue determines which worker handles executions. If empty,
// the engine's default queue is used. A worker for the queue is created if needed.
//
// Returns an error if the workflow name is empty, already registered, or if worker
// creation fails. Registration must complete before calling StartWorkflow.
//
// Thread-safe: Safe to call concurrently with other Register* methods.
func (e *Engine) RegisterWorkflow(_ context.Context, def engine.WorkflowDefinition) error {
	if err := e.requireWorkerMode("register workflows"); err != nil {
		return err
	}
	if def.Name == "" {
		return fmt.Errorf("temporal engine: workflow name cannot be empty")
	}
	queue := def.TaskQueue
	if queue == "" {
		queue = e.defaultQueue
	}
	if err := e.beginWorkflowRegistration(def.Name); err != nil {
		return err
	}
	registered := false
	defer func() {
		if !registered {
			e.abortWorkflowRegistration(def.Name)
		}
	}()
	bundle := e.workerForQueue(queue)

	bundle.registerWorkflow(def.Name, func(tctx workflow.Context, input *api.RunInput) (*api.RunOutput, error) {
		wfCtx := newTemporalWorkflowContext(e, tctx)
		defer e.releaseWorkflowContext(wfCtx.runID)
		return def.Handler(wfCtx, input)
	})
	e.finishWorkflowRegistration(def)
	registered = true
	return nil
}

// RegisterHookActivity registers a typed hook activity with the Temporal engine.
// Hook activities publish workflow-emitted hook events outside of deterministic
// workflow code. The activity accepts *api.HookActivityInput and returns an error.
func (e *Engine) RegisterHookActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.HookActivityInput) error) error {
	if err := e.requireWorkerMode("register hook activities"); err != nil {
		return err
	}
	opts = e.applyActivityClassDefaults(activityKindHook, opts)
	wrapped := func(ctx context.Context, in *api.HookActivityInput) error {
		return fn(e.injectWorkflowContextIntoActivity(ctx), in)
	}
	return e.registerActivityWithCtx(name, opts, wrapped)
}

// RegisterPlannerActivity registers a typed planner activity
// (PlanStart/PlanResume) with the Temporal engine. It binds a Go function that
// accepts *api.PlanActivityInput and returns *api.PlanActivityOutput to a
// logical activity name for use in agent workflows.  The activity is registered
// on the specified task queue (opts.Queue), falling back to the engine's
// default queue if unspecified. Registered activities can be invoked from
// workflows via ExecuteActivity using the provided name.
//
// Returns an error if the activity name is empty or if registration fails due to
// worker configuration.
//
// Thread-safe: Safe to call concurrently with other Register* methods.
func (e *Engine) RegisterPlannerActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error {
	if err := e.requireWorkerMode("register planner activities"); err != nil {
		return err
	}
	opts = e.applyActivityClassDefaults(activityKindPlanner, opts)
	// Wrap to inject originating WorkflowContext into activity context so runtime code
	// can start child workflows (agent-as-tool) with engine-owned context.
	wrapped := func(ctx context.Context, in *api.PlanActivityInput) (*api.PlanActivityOutput, error) {
		return fn(e.injectWorkflowContextIntoActivity(ctx), in)
	}
	return e.registerActivityWithCtx(name, opts, wrapped)
}

// RegisterExecuteToolActivity registers a typed execute_tool activity with the
// Temporal engine.  This method binds a Go function that accepts *api.ToolInput
// and returns *api.ToolOutput to a logical activity name for use within agent
// workflows. The activity is registered on the specified task queue
// (opts.Queue), or falls back to the engine's default queue if unspecified.
// Registered activities are accessible from workflows via ExecuteActivity using
// the provided name.  Returns an error if the activity name is empty or
// registration fails due to worker configuration.
//
// Thread-safe: Safe to call concurrently with other Register* methods.
func (e *Engine) RegisterExecuteToolActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error {
	if err := e.requireWorkerMode("register execute_tool activities"); err != nil {
		return err
	}
	opts = e.applyActivityClassDefaults(activityKindTool, opts)
	// Wrap to inject originating WorkflowContext into activity context so runtime code
	// can start child workflows (agent-as-tool) with engine-owned context.
	wrapped := func(ctx context.Context, in *api.ToolInput) (*api.ToolOutput, error) {
		return fn(e.injectWorkflowContextIntoActivity(ctx), in)
	}
	return e.registerActivityWithCtx(name, opts, wrapped)
}

// StartWorkflow launches a new workflow execution on Temporal using the specified
// workflow definition and input. It constructs Temporal-specific start options from
// the request (ID, queue, retry policy) and executes the workflow asynchronously.
//
// The workflow's task queue is resolved in order: req.TaskQueue → def.TaskQueue →
// engine.defaultQueue. A base context is stored for activity execution correlation.
//
// Returns a WorkflowHandle for waiting, signaling, or cancelling the execution.
// Returns an error if the workflow name is not registered, the ID conflicts with
// an existing workflow, or if Temporal client execution fails.
//
// Thread-safe: Safe to call concurrently.
//
//nolint:unparam // engine.Engine requires returning a workflow handle.
func (e *Engine) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	if req.Workflow == "" {
		return nil, fmt.Errorf("temporal engine: workflow name is required")
	}
	def, err := e.workflowDefinition(req.Workflow)
	if err != nil {
		return nil, err
	}

	queue := req.TaskQueue
	if queue == "" {
		queue = def.TaskQueue
	}
	if queue == "" {
		queue = e.defaultQueue
	}

	opts := client.StartWorkflowOptions{
		ID:        req.ID,
		TaskQueue: queue,
	}
	if len(req.Memo) > 0 {
		opts.Memo = req.Memo
	}
	if len(req.SearchAttributes) > 0 {
		typedSearchAttributes, err := convertSearchAttributes(req.SearchAttributes)
		if err != nil {
			return nil, err
		}
		opts.TypedSearchAttributes = typedSearchAttributes
	}
	if req.RunTimeout > 0 {
		// Apply as WorkflowRunTimeout. Temporal also supports WorkflowExecutionTimeout;
		// we set RunTimeout here to bound total execution wall time.
		opts.WorkflowRunTimeout = req.RunTimeout
	}
	if rp := convertRetryPolicy(req.RetryPolicy); rp != nil {
		opts.RetryPolicy = rp
	}

	run, err := e.client.ExecuteWorkflow(ctx, opts, def.Name, req.Input)
	if err != nil {
		return nil, err
	}

	return &workflowHandle{
		run:    run,
		client: e.client,
	}, nil
}

// Close gracefully shuts down the Temporal client if the engine created it
// (via ClientOptions). If a pre-configured Client was provided to New(), Close
// does nothing, leaving client lifecycle management to the caller.
//
// Returns nil (error signature maintained for interface compatibility).
//
// Thread-safe: Safe to call concurrently, but typically called once during shutdown.
//
//nolint:unparam // Error return maintained for interface compatibility.
func (e *Engine) Close() error {
	e.stopWorkers()
	if e.closeClient && e.client != nil {
		e.client.Close()
	}
	return nil
}

// SealRegistration closes the registration phase for worker-mode engines and
// starts polling for every queue registered so far. Client-mode engines do not
// stage registrations, so sealing is a no-op.
//
//nolint:unparam // engine.RegistrationSealer requires an error result.
func (e *Engine) SealRegistration(context.Context) error {
	if !e.workerMode {
		return nil
	}
	e.mu.Lock()
	if e.registrationSealed {
		e.mu.Unlock()
		return nil
	}
	e.registrationSealed = true
	bundles := make([]*workerBundle, 0, len(e.workers))
	for _, bundle := range e.workers {
		bundles = append(bundles, bundle)
	}
	e.mu.Unlock()
	for _, bundle := range bundles {
		bundle.start()
	}
	return nil
}

// injectWorkflowContextIntoActivity attaches the originating WorkflowContext to the
// activity context when available so runtime code can access engine-owned workflow
// operations (e.g., starting child workflows for agent-as-tool).
func (e *Engine) injectWorkflowContextIntoActivity(ctx context.Context) context.Context {
	info := activity.GetInfo(ctx)
	with := engine.WithActivityContext(ctx)
	with = engine.WithActivityHeartbeatRecorder(with, temporalHeartbeatRecorder{ctx: ctx})
	with = engine.WithActivityHeartbeatTimeout(with, info.HeartbeatTimeout)
	if v, ok := e.workflowContexts.Load(info.WorkflowExecution.RunID); ok {
		if wf, ok2 := v.(engine.WorkflowContext); ok2 {
			with = engine.WithWorkflowContext(with, wf)
		}
	}
	return with
}

type temporalHeartbeatRecorder struct {
	ctx context.Context
}

func (r temporalHeartbeatRecorder) RecordHeartbeat(details ...any) {
	activity.RecordHeartbeat(r.ctx, details...)
}

type activityKind string

const (
	activityKindHook    activityKind = "hook"
	activityKindPlanner activityKind = "planner"
	activityKindTool    activityKind = "tool"
)

// applyActivityClassDefaults overlays Temporal-owned queue-wait and liveness
// defaults onto a runtime-owned registration when those mechanics were left
// unspecified by the caller.
func (e *Engine) applyActivityClassDefaults(kind activityKind, opts engine.ActivityOptions) engine.ActivityOptions {
	defaults := e.activityClassDefaultsFor(kind)
	if opts.ScheduleToStartTimeout == 0 {
		opts.ScheduleToStartTimeout = defaults.QueueWaitTimeout
	}
	if opts.HeartbeatTimeout == 0 {
		opts.HeartbeatTimeout = defaults.LivenessTimeout
	}
	return opts
}

// activityClassDefaultsFor returns the Temporal adapter defaults for the given
// activity class.
func (e *Engine) activityClassDefaultsFor(kind activityKind) ActivityTimeoutDefaults {
	switch kind {
	case activityKindHook:
		return e.activityDefaults.Hook
	case activityKindPlanner:
		return e.activityDefaults.Planner
	case activityKindTool:
		return e.activityDefaults.Tool
	default:
		return ActivityTimeoutDefaults{}
	}
}

// registerActivityWithCtx registers an activity function on the appropriate queue,
// records its options, and returns any worker configuration error.
func (e *Engine) registerActivityWithCtx(name string, opts engine.ActivityOptions, fn any) error {
	if name == "" {
		return fmt.Errorf("temporal engine: activity name cannot be empty")
	}
	queue := opts.Queue
	if queue == "" {
		queue = e.defaultQueue
	}
	if err := e.beginActivityRegistration(name); err != nil {
		return err
	}
	registered := false
	defer func() {
		if !registered {
			e.abortActivityRegistration(name)
		}
	}()
	bundle := e.workerForQueue(queue)
	bundle.registerActivity(name, fn)
	e.finishActivityRegistration(name, opts)
	registered = true
	return nil
}

func (e *Engine) requireWorkerMode(action string) error {
	if e.workerMode {
		return nil
	}
	return fmt.Errorf("temporal engine: client mode cannot %s; use temporal.NewWorker", action)
}

func (e *Engine) workflowDefinition(name string) (engine.WorkflowDefinition, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	def, ok := e.workflows[name]
	if !ok {
		return engine.WorkflowDefinition{}, fmt.Errorf("temporal engine: workflow %q is not registered", name)
	}
	return def, nil
}

// beginWorkflowRegistration reserves a workflow name so duplicate concurrent
// registrations fail before any worker or registry mutation occurs.
func (e *Engine) beginWorkflowRegistration(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.workflows[name]; exists {
		return fmt.Errorf("temporal engine: workflow %q already registered", name)
	}
	if _, exists := e.pendingWorkflows[name]; exists {
		return fmt.Errorf("temporal engine: workflow %q already registered", name)
	}
	e.pendingWorkflows[name] = struct{}{}
	return nil
}

// finishWorkflowRegistration commits a workflow definition after worker
// registration succeeds and clears the temporary reservation.
func (e *Engine) finishWorkflowRegistration(def engine.WorkflowDefinition) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.pendingWorkflows, def.Name)
	e.workflows[def.Name] = def
}

// abortWorkflowRegistration releases a reserved workflow name after a failed
// registration attempt so later retries can proceed.
func (e *Engine) abortWorkflowRegistration(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.pendingWorkflows, name)
}

// beginActivityRegistration reserves an activity name so duplicate concurrent
// registrations fail before any worker or option mutation occurs.
func (e *Engine) beginActivityRegistration(name string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if _, exists := e.activityOptions[name]; exists {
		return fmt.Errorf("temporal engine: activity %q already registered", name)
	}
	if _, exists := e.pendingActivities[name]; exists {
		return fmt.Errorf("temporal engine: activity %q already registered", name)
	}
	e.pendingActivities[name] = struct{}{}
	return nil
}

// finishActivityRegistration commits activity defaults after worker registration
// succeeds and clears the temporary reservation.
func (e *Engine) finishActivityRegistration(name string, opts engine.ActivityOptions) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.pendingActivities, name)
	e.activityOptions[name] = opts
}

// abortActivityRegistration releases a reserved activity name after a failed
// registration attempt so later retries can proceed.
func (e *Engine) abortActivityRegistration(name string) {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.pendingActivities, name)
}

func (e *Engine) trackWorkflowContext(runID string, wf engine.WorkflowContext) {
	if runID == "" {
		return
	}
	e.workflowContexts.Store(runID, wf)
}

func (e *Engine) releaseWorkflowContext(runID string) {
	if runID == "" {
		return
	}
	e.workflowContexts.Delete(runID)
}
