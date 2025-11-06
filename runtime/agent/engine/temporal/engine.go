package temporal

import (
	"context"
	"fmt"
	"sync"

	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/client"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
	"go.temporal.io/sdk/interceptor"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

// Options configures the Temporal engine adapter for registering workflows,
// activities, and managing worker lifecycle. Either a pre-configured Client
// or ClientOptions must be provided. The adapter automatically wires OTEL
// instrumentation, manages per-queue workers, and optionally auto-starts
// workers on first workflow execution.
//
// Default behavior includes auto-starting workers and enabling tracing/metrics.
// Set DisableWorkerAutoStart to manually control worker lifecycle via Worker().
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

	// WorkerOptions configures worker defaults for task queue, concurrency, and identity.
	// TaskQueue must be set and defines the default queue used when workflow/activity
	// definitions omit a queue. A worker is created per unique task queue.
	WorkerOptions WorkerOptions

	// Instrumentation toggles OTEL tracing and metrics for the Temporal client and workers.
	// Tracing and metrics are enabled by default. Set DisableTracing or DisableMetrics to
	// opt out. Customize interceptor behavior via TracerOptions and MetricsOptions.
	Instrumentation InstrumentationOptions

	// DisableWorkerAutoStart disables automatic worker startup on first workflow execution.
	// When false (default), workers start automatically so callers don't need to call
	// Worker().Start(). Set to true when you need manual control over worker lifecycle
	// or want to register all workflows/activities before starting workers.
	DisableWorkerAutoStart bool

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

	// TracerOptions customize the OTEL tracing interceptor (span attributes, filters,
	// etc.). Only used when DisableTracing is false. Refer to Temporal SDK OTEL docs.
	TracerOptions temporalotel.TracerOptions

	// MetricsOptions customize the OTEL metrics handler (metric names, labels, etc.).
	// Only used when DisableMetrics is false. Refer to Temporal SDK OTEL docs.
	MetricsOptions temporalotel.MetricsHandlerOptions
}

// Engine implements engine.Engine using Temporal as the durable execution backend.
// It manages workflow/activity registration, per-queue worker lifecycle, and provides
// workflow execution handles. The engine creates one worker per unique task queue and
// automatically wires OTEL instrumentation for tracing and metrics.
//
// Thread-safety: All methods are safe for concurrent use. Internal state is protected
// by mutexes. Workers are lazily created and started on-demand (unless auto-start is
// disabled).
//
// Lifecycle: Construct via New(), register workflows/activities, then either let workers
// auto-start or manually call Worker().Start(). Call Close() to gracefully shut down all
// workers and the Temporal client.
type Engine struct {
	client      client.Client
	closeClient bool

	defaultQueue      string
	workerOpts        worker.Options
	autoStartDisabled bool

	logger  telemetry.Logger
	metrics telemetry.Metrics
	tracer  telemetry.Tracer

	mu              sync.Mutex
	workers         map[string]*workerBundle
	workersStarted  bool
	workflows       map[string]engine.WorkflowDefinition
	activityOptions map[string]engine.ActivityOptions

	workflowContexts sync.Map // runID -> engine.WorkflowContext
	baseContexts     sync.Map // runID -> context.Context
}

// New constructs a Temporal engine adapter. Either Client or ClientOptions must
// be provided. The default task queue in WorkerOptions must also be configured.
func New(opts Options) (*Engine, error) {
	defaultQueue := opts.WorkerOptions.TaskQueue
	if defaultQueue == "" {
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

	inst, err := configureInstrumentation(opts.Instrumentation)
	if err != nil {
		return nil, err
	}

	cli := opts.Client
	closeClient := false
	if cli == nil {
		if opts.ClientOptions == nil {
			return nil, fmt.Errorf("temporal engine: client options are required when Client is nil")
		}
		clientOpts := client.Options{}
		if opts.ClientOptions != nil {
			clientOpts = *opts.ClientOptions
		}
		applyClientInstrumentation(&clientOpts, inst)
		cli, err = client.NewLazyClient(clientOpts)
		if err != nil {
			return nil, fmt.Errorf("temporal engine: create client: %w", err)
		}
		closeClient = true
	}

	workerOpts := opts.WorkerOptions.Options
	applyWorkerInstrumentation(&workerOpts, inst)

	e := &Engine{
		client:            cli,
		closeClient:       closeClient,
		defaultQueue:      defaultQueue,
		workerOpts:        workerOpts,
		autoStartDisabled: opts.DisableWorkerAutoStart,
		logger:            logger,
		metrics:           metrics,
		tracer:            tracer,
		workers:           make(map[string]*workerBundle),
		workflows:         make(map[string]engine.WorkflowDefinition),
		activityOptions:   make(map[string]engine.ActivityOptions),
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
	if def.Name == "" {
		return fmt.Errorf("temporal engine: workflow name cannot be empty")
	}
	queue := def.TaskQueue
	if queue == "" {
		queue = e.defaultQueue
	}
	bundle, err := e.workerForQueue(queue)
	if err != nil {
		return err
	}

	bundle.registerWorkflow(def.Name, func(tctx workflow.Context, input any) (any, error) {
		wfCtx := newTemporalWorkflowContext(e, tctx)
		defer e.releaseWorkflowContext(wfCtx.RunID())
		return def.Handler(wfCtx, input)
	})

	e.mu.Lock()
	defer e.mu.Unlock()
	if _, exists := e.workflows[def.Name]; exists {
		return fmt.Errorf("temporal engine: workflow %q already registered", def.Name)
	}
	e.workflows[def.Name] = def
	return nil
}

// RegisterWorkflowTyped registers a strongly-typed workflow, allowing Temporal to decode
// the input directly into *T.
// (Typed workflow registration removed; use RegisterWorkflow.)

// RegisterActivity registers an activity handler with the Temporal worker for
// the specified task queue. The activity handler is wrapped to inject the workflow
// context and telemetry context when available, enabling activities to access
// workflow metadata and observability tools.
//
// The activity's Queue (from Options) determines which worker handles executions.
// If empty, the engine's default queue is used. Activity-specific retry policies
// and timeouts are stored for runtime use.
//
// Returns an error if the activity name is empty or if worker creation fails.
// Registration must complete before the activity can be invoked from workflows.
//
// Thread-safe: Safe to call concurrently with other Register* methods.
func (e *Engine) RegisterActivity(_ context.Context, def engine.ActivityDefinition) error {
	if def.Name == "" {
		return fmt.Errorf("temporal engine: activity name cannot be empty")
	}
	queue := def.Options.Queue
	if queue == "" {
		queue = e.defaultQueue
	}
	bundle, err := e.workerForQueue(queue)
	if err != nil {
		return err
	}

	bundle.registerActivity(def.Name, func(actx context.Context, input any) (any, error) {
		runID, wfCtx := e.lookupWorkflowContext(actx)
		if wfCtx != nil {
			actx = engine.WithWorkflowContext(actx, wfCtx)
		} else if runID != "" {
			e.logger.Warn(actx, "workflow context not found for activity", "run_id", runID, "activity", def.Name)
		}
		if base := e.workflowBaseContext(runID); base != nil {
			actx = telemetry.MergeContext(actx, base)
		}
		return def.Handler(actx, input)
	})

	e.mu.Lock()
	e.activityOptions[def.Name] = def.Options
	e.mu.Unlock()
	return nil
}

// StartWorkflow launches a new workflow execution on Temporal using the specified
// workflow definition and input. It constructs Temporal-specific start options from
// the request (ID, queue, retry policy) and executes the workflow asynchronously.
//
// If auto-start is enabled (default), workers are automatically started before execution.
// The workflow's task queue is resolved in order: req.TaskQueue → def.TaskQueue →
// engine.defaultQueue. A base context is stored for activity execution correlation.
//
// Returns a WorkflowHandle for waiting, signaling, or cancelling the execution.
// Returns an error if the workflow name is not registered, the ID conflicts with
// an existing workflow, or if Temporal client execution fails.
//
// Thread-safe: Safe to call concurrently.
func (e *Engine) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	if req.Workflow == "" {
		return nil, fmt.Errorf("temporal engine: workflow name is required")
	}
	def, err := e.workflowDefinition(req.Workflow)
	if err != nil {
		return nil, err
	}

	if !e.autoStartDisabled {
		e.ensureWorkersStarted()
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
	if rp := convertRetryPolicy(req.RetryPolicy); rp != nil {
		opts.RetryPolicy = rp
	}

	run, err := e.client.ExecuteWorkflow(ctx, opts, def.Name, req.Input)
	if err != nil {
		return nil, err
	}
	e.baseContexts.Store(run.GetRunID(), context.WithoutCancel(ctx))

	return &workflowHandle{
		run:    run,
		client: e.client,
	}, nil
}

// Worker returns a controller for managing the lifecycle of all workers managed
// by this engine. Use this to manually start or stop workers when DisableWorkerAutoStart
// is enabled. When auto-start is active (default), workers start automatically on
// first workflow execution, making this method optional.
//
// The controller provides Start() to launch all registered workers and Stop() to
// gracefully shut them down. Multiple calls to Worker() return controllers for the
// same underlying engine, so start/stop operations affect all workers globally.
//
// Thread-safe: Safe to call concurrently.
func (e *Engine) Worker() *WorkerController {
	return &WorkerController{engine: e}
}

// Close gracefully shuts down the Temporal client if the engine created it
// (via ClientOptions). If a pre-configured Client was provided to New(), Close
// does nothing, leaving client lifecycle management to the caller.
//
// Call Close during application shutdown after stopping workers via Worker().Stop().
// Closing the client while workers are active may cause workflow/activity failures.
//
// Returns nil (error signature maintained for interface compatibility).
//
// Thread-safe: Safe to call concurrently, but typically called once during shutdown.
//
//nolint:unparam // Error return maintained for interface compatibility.
func (e *Engine) Close() error {
	if e.closeClient && e.client != nil {
		e.client.Close()
	}
	return nil
}

func (e *Engine) workerForQueue(queue string) (*workerBundle, error) {
	if queue == "" {
		queue = e.defaultQueue
	}
	if queue == "" {
		return nil, fmt.Errorf("temporal engine: no task queue configured")
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	if bundle, ok := e.workers[queue]; ok {
		return bundle, nil
	}

	w := worker.New(e.client, queue, e.workerOpts)
	bundle := &workerBundle{
		queue:  queue,
		worker: w,
		logger: e.logger,
	}
	e.workers[queue] = bundle
	if e.workersStarted {
		bundle.start()
	}
	return bundle, nil
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

func (e *Engine) ensureWorkersStarted() {
	e.mu.Lock()
	if e.workersStarted {
		e.mu.Unlock()
		return
	}
	e.workersStarted = true
	bundles := make([]*workerBundle, 0, len(e.workers))
	for _, b := range e.workers {
		bundles = append(bundles, b)
	}
	e.mu.Unlock()
	for _, b := range bundles {
		b.start()
	}
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
	e.baseContexts.Delete(runID)
}

func (e *Engine) lookupWorkflowContext(ctx context.Context) (string, engine.WorkflowContext) {
	info := activity.GetInfo(ctx)
	runID := info.WorkflowExecution.RunID
	if runID == "" {
		return "", nil
	}
	if wf, ok := e.workflowContexts.Load(runID); ok {
		if typed, ok := wf.(engine.WorkflowContext); ok {
			return runID, typed
		}
	}
	return runID, nil
}

func (e *Engine) workflowBaseContext(runID string) context.Context {
	if runID == "" {
		return nil
	}
	if base, ok := e.baseContexts.Load(runID); ok {
		if ctx, ok := base.(context.Context); ok {
			return ctx
		}
	}
	return nil
}

// WorkerController manages worker lifecycle (start/stop) for all task queues
// managed by the Temporal engine. It provides manual control over when workers
// begin polling Temporal for workflow and activity tasks.
//
// Obtain a controller via Engine.Worker(). When auto-start is disabled, call
// Start() after registering all workflows/activities. When auto-start is enabled
// (default), Start() is optional - workers start automatically on first workflow
// execution.
//
// Call Stop() during graceful shutdown to drain in-flight tasks and disconnect
// workers from Temporal. Multiple controllers for the same engine share state,
// so stop operations affect all workers globally.
//
// Thread-safety: Start() and Stop() are safe to call concurrently.
type WorkerController struct {
	engine *Engine
}

// Start launches all registered workers. Subsequent worker registrations will
// be auto-started as they are created.
//
//nolint:unparam // Error return maintained for future extensibility.
func (c *WorkerController) Start() error {
	c.engine.ensureWorkersStarted()
	return nil
}

// Stop gracefully stops all workers managed by the engine.
func (c *WorkerController) Stop() {
	c.engine.mu.Lock()
	bundles := make([]*workerBundle, 0, len(c.engine.workers))
	for _, b := range c.engine.workers {
		bundles = append(bundles, b)
	}
	c.engine.mu.Unlock()

	for _, b := range bundles {
		b.stop()
	}
}

type workerBundle struct {
	queue  string
	worker worker.Worker
	logger telemetry.Logger

	startOnce sync.Once
}

func (b *workerBundle) start() {
	b.startOnce.Do(func() {
		go func() {
			if err := b.worker.Run(worker.InterruptCh()); err != nil {
				b.logger.Error(context.Background(), "temporal worker exited", "queue", b.queue, "err", err)
			}
		}()
	})
}

func (b *workerBundle) stop() {
	b.worker.Stop()
}

func (b *workerBundle) registerWorkflow(name string, fn any) {
	b.worker.RegisterWorkflowWithOptions(fn, workflow.RegisterOptions{Name: name})
}

// typed registration reuses the same underlying RegisterWorkflowWithOptions since
// Temporal infers payload type from the function signature.

func (b *workerBundle) registerActivity(name string, fn any) {
	b.worker.RegisterActivityWithOptions(fn, activity.RegisterOptions{Name: name})
}

type instrumentation struct {
	tracer  interceptor.Interceptor
	metrics client.MetricsHandler
}

func configureInstrumentation(opts InstrumentationOptions) (*instrumentation, error) {
	inst := &instrumentation{}
	if !opts.DisableTracing {
		tracer, err := temporalotel.NewTracingInterceptor(opts.TracerOptions)
		if err != nil {
			return nil, fmt.Errorf("temporal engine: configure tracing interceptor: %w", err)
		}
		inst.tracer = tracer
	}
	if !opts.DisableMetrics {
		inst.metrics = temporalotel.NewMetricsHandler(opts.MetricsOptions)
	}
	if inst.tracer == nil && inst.metrics == nil {
		return nil, nil
	}
	return inst, nil
}

func applyClientInstrumentation(opts *client.Options, inst *instrumentation) {
	if inst == nil {
		return
	}
	if inst.tracer != nil {
		opts.Interceptors = append(opts.Interceptors, inst.tracer)
	}
	if inst.metrics != nil && opts.MetricsHandler == nil {
		opts.MetricsHandler = inst.metrics
	}
}

func applyWorkerInstrumentation(opts *worker.Options, inst *instrumentation) {
	if inst == nil {
		return
	}
	if inst.tracer != nil {
		opts.Interceptors = append(opts.Interceptors, inst.tracer)
	}
}

type workflowHandle struct {
	run    client.WorkflowRun
	client client.Client
}

func (h *workflowHandle) Wait(ctx context.Context, result any) error {
	return h.run.Get(ctx, result)
}

func (h *workflowHandle) Signal(ctx context.Context, name string, payload any) error {
	return h.client.SignalWorkflow(ctx, h.run.GetID(), h.run.GetRunID(), name, payload)
}

// SignalByID sends a signal to a workflow by its workflow ID/run ID directly.
func (e *Engine) SignalByID(ctx context.Context, workflowID, runID, name string, payload any) error {
	if workflowID == "" {
		return fmt.Errorf("workflow id is required")
	}
	return e.client.SignalWorkflow(ctx, workflowID, runID, name, payload)
}

func (h *workflowHandle) Cancel(ctx context.Context) error {
	return h.client.CancelWorkflow(ctx, h.run.GetID(), h.run.GetRunID())
}
