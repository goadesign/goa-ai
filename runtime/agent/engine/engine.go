// Package engine defines the workflow engine abstractions and adapters for
// durable execution backends. It provides a pluggable interface so generated
// code can target Temporal, custom engines, or in-memory implementations
// without modification.
package engine

import (
	"context"
	"time"

	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	// Engine abstracts workflow registration and execution so adapters (Temporal,
	// in-memory, or custom) can be swapped without touching generated code.
	// Implementations translate these generic types into backend-specific primitives.
	Engine interface {
		// RegisterWorkflow registers a workflow definition with the engine. This is
		// typically called during service initialization before starting the worker pool.
		// Returns an error if the workflow name is already registered or if
		// registration fails.
		RegisterWorkflow(ctx context.Context, def WorkflowDefinition) error

		// RegisterActivity registers an activity definition with the engine. Activities
		// are short-lived tasks invoked from workflows. This must be called during
		// initialization before starting workers. Returns an error if the activity
		// name conflicts or registration fails.
		RegisterActivity(ctx context.Context, def ActivityDefinition) error

		// StartWorkflow initiates a new workflow execution and returns a handle for
		// interacting with it. The workflow ID in req must be unique for the engine
		// instance. Returns an error if the workflow name is not registered, the ID
		// conflicts with a running workflow, or if scheduling fails.
		StartWorkflow(ctx context.Context, req WorkflowStartRequest) (WorkflowHandle, error)
	}

	// WorkflowDefinition binds a workflow handler to a logical name and default queue.
	// Generated code creates these during agent registration.
	WorkflowDefinition struct {
		// Name is the logical identifier registered with the engine (e.g., "AgentWorkflow").
		Name string
		// TaskQueue is the default queue used when starting new workflows. Workers
		// subscribe to this queue to receive workflow tasks.
		TaskQueue string
		// Handler is the workflow function invoked by the engine when the workflow executes.
		Handler WorkflowFunc
	}

	// WorkflowFunc is the generated workflow entry point. It receives a WorkflowContext
	// and arbitrary input, returning a result or error. The function must be deterministic:
	// it should produce the same execution sequence given the same inputs and activity results.
	WorkflowFunc func(ctx WorkflowContext, input any) (any, error)

	// WorkflowContext exposes engine operations to workflow handlers within the
	// deterministic execution environment of a workflow. It wraps engine-specific
	// contexts (Temporal workflow.Context, in-memory contexts, etc.) and provides
	// a uniform API for activity execution, signal handling, and observability.
	//
	// Implementations must ensure deterministic replay: operations that interact
	// with the workflow engine (ExecuteActivity, SignalChannel) must produce
	// deterministic results when replayed. Direct I/O, random number generation,
	// or system time access within workflows violates determinism and causes
	// workflow failures.
	//
	// Thread-safety: WorkflowContext is bound to a single workflow execution and
	// must not be shared across goroutines. Activity and signal operations are
	// serialized by the workflow engine.
	//
	// Lifecycle: Created by the engine when a workflow starts and remains valid
	// until the workflow completes or fails. Do not cache WorkflowContext outside
	// the workflow function scope.
	WorkflowContext interface {
		// Context returns the Go context for the workflow. In deterministic engines
		// (like Temporal), this is a special replay-aware context. Use this for activity
		// execution and cancellation propagation.
		Context() context.Context

		// WorkflowID returns the unique identifier for this workflow execution.
		WorkflowID() string

		// RunID returns the engine-assigned run identifier, used for observability
		// and run-level correlation.
		RunID() string

		// ExecuteActivity schedules an activity for execution and waits for its result.
		// The result parameter is populated with the activity's return value. Returns
		// an error if the activity fails after retries or if scheduling fails.
		ExecuteActivity(ctx context.Context, req ActivityRequest, result any) error

		// ExecuteActivityAsync schedules an activity without blocking and returns a Future.
		// The Future can be resolved later via Get() to retrieve the result. This enables
		// parallel execution of multiple activities. Returns an error only if the activity
		// cannot be scheduled (e.g., invalid request); execution errors are returned via Future.Get().
		ExecuteActivityAsync(ctx context.Context, req ActivityRequest) (Future, error)

		// SignalChannel returns a channel for the given signal name. Workflow code can
		// poll or block on this channel to react to external events (pause/resume, human
		// inputs, etc.) delivered via the workflow engine's signaling mechanism.
		SignalChannel(name string) SignalChannel

		// Logger returns a logger scoped to this workflow execution.
		Logger() telemetry.Logger

		// Metrics returns a metrics recorder for emitting workflow-scoped metrics.
		Metrics() telemetry.Metrics

		// Tracer returns a tracer for creating spans within the workflow.
		Tracer() telemetry.Tracer

		// Now returns the current workflow time in a deterministic manner. Implementations
		// must return a time source that is replay-safe (e.g., Temporal's workflow.Now).
		Now() time.Time
	}

	// Future represents a pending activity result that will become available after
	// the activity completes. Futures enable parallel activity execution: workflows
	// can launch multiple activities via ExecuteActivityAsync and collect results
	// later using Get(), which blocks until the activity finishes.
	//
	// Thread-safety: Futures are bound to a single workflow execution and must not
	// be shared across workflow executions. Calling Get() multiple times is safe
	// and returns the same result/error on each call.
	//
	// Lifecycle: Valid from creation until the workflow completes. Get() must be
	// called before the workflow exits; abandoned futures leak workflow resources
	// in some engines. IsReady() enables polling without blocking.
	Future interface {
		// Get blocks until the activity completes and populates result with the return value.
		// Returns an error if the activity fails after retries or if result deserialization fails.
		// Calling Get multiple times on the same Future returns the same result/error.
		Get(ctx context.Context, result any) error

		// IsReady returns true if the activity has completed (success or failure) and Get()
		// will not block. This allows workflows to poll or implement custom waiting strategies.
		IsReady() bool
	}

	// ActivityDefinition registers an activity handler with optional defaults.
	// Activities are stateless, short-lived tasks invoked from workflows.
	ActivityDefinition struct {
		// Name is the logical identifier for the activity (e.g., "ExecuteToolActivity").
		Name string
		// Handler executes the activity logic when invoked.
		Handler ActivityFunc
		// Options configures retry/timeout behavior for the activity.
		Options ActivityOptions
	}

	// ActivityFunc handles an activity invocation. It receives a standard Go context
	// and arbitrary input, returning a result or error. Unlike workflows, activities
	// can perform side effects (I/O, API calls, database access).
	ActivityFunc func(ctx context.Context, input any) (any, error)

	// ActivityOptions configures retry and timeouts for an activity.
	ActivityOptions struct {
		// Queue overrides the default activity queue. If empty, the activity inherits
		// the workflow's task queue.
		Queue string
		// RetryPolicy controls retry behavior for this activity. If zero-valued, the
		// engine uses its default retry policy.
		RetryPolicy RetryPolicy
		// Timeout bounds the total activity execution time, including retries. Zero
		// means no timeout (not recommended for production).
		Timeout time.Duration
	}

	// WorkflowStartRequest describes how to launch a workflow execution. Generated
	// code constructs these when agents are invoked.
	WorkflowStartRequest struct {
		// ID is the workflow identifier, which must be unique within the engine scope.
		// Typically derived from the agent ID and a UUID.
		ID string
		// Workflow names the registered workflow definition to execute. Engines that
		// support multiple workflows (one per agent) require this field.
		Workflow string
		// TaskQueue selects the queue to schedule the workflow on. Workers listening
		// on this queue will pick up the workflow.
		TaskQueue string
		// Input is the payload passed to the workflow handler (e.g., RunInput).
		Input any
		// Memo stores small diagnostic payloads alongside the workflow execution.
		// Engines like Temporal persist these for queries/visibility. Nil means no memo.
		Memo map[string]any
		// SearchAttributes captures indexed metadata used for visibility queries.
		// Nil means no attributes are set.
		SearchAttributes map[string]any
		// RetryPolicy controls automatic restarts of the workflow start attempt if
		// scheduling fails. Not to be confused with activity retries.
		RetryPolicy RetryPolicy
	}

	// ActivityRequest contains the info needed to schedule an activity from a workflow.
	// Workflows construct these when calling ExecuteActivity.
	ActivityRequest struct {
		// Name identifies the activity to execute (must match a registered ActivityDefinition).
		Name string
		// Input is the payload passed to the activity handler.
		Input any
		// Queue optionally overrides the queue for this invocation. If empty, inherits
		// from the activity definition or workflow queue.
		Queue string
		// RetryPolicy controls retry behavior for the scheduled activity. If zero-valued,
		// uses the policy from the activity definition.
		RetryPolicy RetryPolicy
		// Timeout bounds the activity execution time. Zero means no timeout.
		Timeout time.Duration
	}

	// WorkflowHandle allows callers to interact with a running workflow. Returned
	// by Engine.StartWorkflow, it provides methods to wait for completion, send
	// signals, or cancel execution.
	WorkflowHandle interface {
		// Wait blocks until the workflow completes, populating result with the workflow's
		// return value. Returns an error if the workflow fails, is cancelled, or if
		// deserialization of the result fails.
		Wait(ctx context.Context, result any) error

		// Signal sends an asynchronous message to the workflow. The workflow can listen
		// for signals using engine-specific APIs. Returns an error if the signal cannot
		// be delivered (e.g., workflow already completed).
		Signal(ctx context.Context, name string, payload any) error

		// Cancel requests cancellation of the workflow. The workflow's context will be
		// cancelled, and in-flight activities may be cancelled depending on the engine.
		// Returns an error if cancellation fails.
		Cancel(ctx context.Context) error
	}

	// RetryPolicy defines retry semantics shared by workflows and activities.
	// Zero-valued fields mean the engine uses its defaults.
	RetryPolicy struct {
		// MaxAttempts caps the total number of retry attempts. Zero means unlimited retries.
		MaxAttempts int
		// InitialInterval is the delay before the first retry. Zero means use engine default.
		InitialInterval time.Duration
		// BackoffCoefficient multiplies the delay after each retry. Values < 1 are treated
		// as 1 (constant backoff). A value of 2 provides exponential backoff.
		BackoffCoefficient float64
	}

	// SignalChannel exposes workflow signal delivery in an engine-agnostic way.
	// Implementations wrap engine-specific channels (Temporal signal channels,
	// in-process Go channels, etc.) and provide blocking and non-blocking receive
	// helpers so workflow code can react to external events deterministically.
	SignalChannel interface {
		// Receive blocks until a signal value is delivered and decodes it into dest.
		// Implementations should respect ctx when possible; for engines that do not
		// support context cancellation, Receive may ignore ctx and rely on workflow
		// cancellation semantics instead.
		Receive(ctx context.Context, dest any) error
		// ReceiveAsync attempts to receive a signal without blocking. It returns true
		// when a value was written into dest, or false if no signal was available.
		ReceiveAsync(dest any) bool
	}
)
