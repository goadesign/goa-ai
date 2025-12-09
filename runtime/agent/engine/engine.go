// Package engine defines workflow engine abstractions for durable agent
// execution. It provides pluggable interfaces so generated code and the runtime
// can target Temporal, in-memory, or custom backends without modification.
//
// # Core Abstractions
//
// The package defines several key interfaces:
//
//   - Engine: Registers workflows and activities, starts workflow executions.
//     The runtime calls Engine methods during agent registration and run submission.
//
//   - WorkflowContext: Provides deterministic operations inside workflow handlers.
//     Generated workflow code uses this to schedule activities, handle signals,
//     and start child workflows. Implementations must ensure replay-safe behavior.
//
//   - WorkflowHandle: Represents a running workflow. Callers use handles to wait
//     for completion, send signals, or cancel execution.
//
//   - Future: Represents a pending activity result. Enables parallel execution
//     by allowing workflows to launch multiple activities and collect results later.
//
//   - SignalChannel: Delivers external signals to workflows in a deterministic way.
//     Used for pause/resume, clarification answers, and external tool results.
//
// # Available Implementations
//
// Two engine implementations ship with goa-ai:
//
//   - temporal: Production-grade durable execution backed by Temporal. Supports
//     workflow replay, long-running execution, and distributed workers.
//
//   - inmem: In-memory synchronous execution for development and testing.
//     No durability, no workers, runs immediately in the caller's goroutine.
//
// # Determinism Requirements
//
// Workflow handlers run in a deterministic environment where the same inputs
// and history must produce the same outputs. WorkflowContext enforces this by:
//
//   - Providing Now() instead of time.Now() for workflow time
//   - Requiring activities for all I/O operations
//   - Using replay-safe signal channels
//
// Activities (planner calls, tool execution) are NOT deterministic and can
// perform arbitrary I/O. The engine records activity inputs/outputs and replays
// them during workflow recovery.
//
// # Usage Pattern
//
//	// Create engine (Temporal for production)
//	eng, _ := temporal.New(temporal.Options{...})
//	defer eng.Close()
//
//	// Create runtime with engine
//	rt := runtime.New(runtime.WithEngine(eng))
//
//	// Register agents (registers workflows/activities on engine)
//	chat.RegisterChatAgent(ctx, rt, chat.ChatAgentConfig{...})
//
//	// Start runs (submits workflows to engine)
//	client := chat.NewClient(rt)
//	out, _ := client.Run(ctx, "session-1", messages)
package engine

import (
	"context"
	"errors"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
)

// RunStatus represents the lifecycle state of a workflow execution.
type RunStatus string

const (
	// RunStatusPending indicates the workflow has been accepted but not started yet.
	RunStatusPending RunStatus = "pending"
	// RunStatusRunning indicates the workflow is actively executing.
	RunStatusRunning RunStatus = "running"
	// RunStatusCompleted indicates the workflow finished successfully.
	RunStatusCompleted RunStatus = "completed"
	// RunStatusFailed indicates the workflow failed permanently.
	RunStatusFailed RunStatus = "failed"
	// RunStatusCanceled indicates the workflow was canceled externally.
	RunStatusCanceled RunStatus = "canceled"
	// RunStatusPaused indicates execution is paused awaiting external intervention.
	RunStatusPaused RunStatus = "paused"
)

var (
	// ErrWorkflowNotFound indicates that no workflow execution exists for the given identifier.
	ErrWorkflowNotFound = errors.New("workflow not found")
)

type (
	// Engine abstracts workflow registration and execution so adapters (Temporal,
	// in-memory, or custom) can be swapped without touching generated code.
	// Implementations translate these generic types into backend-specific primitives.
	Engine interface {
		// RegisterWorkflow registers a workflow definition with the engine.
		RegisterWorkflow(ctx context.Context, def WorkflowDefinition) error

		// RegisterPlannerActivity registers a typed planner activity (PlanStart or
		// PlanResume) that accepts *api.PlanActivityInput and returns *api.PlanActivityOutput.
		RegisterPlannerActivity(ctx context.Context, name string, opts ActivityOptions, fn func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error

		// RegisterExecuteToolActivity registers a typed execute_tool activity that
		// accepts *api.ToolInput and returns *api.ToolOutput.
		RegisterExecuteToolActivity(ctx context.Context, name string, opts ActivityOptions, fn func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error

		// StartWorkflow initiates a new workflow execution and returns a handle for
		// interacting with it. The workflow ID in req must be unique for the engine
		// instance. Returns an error if the workflow name is not registered, the ID
		// conflicts with a running workflow, or if scheduling fails.
		StartWorkflow(ctx context.Context, req WorkflowStartRequest) (WorkflowHandle, error)

		// QueryRunStatus returns the current lifecycle status for a workflow execution
		// identified by runID. The engine is the source of truth for workflow status.
		// Returns an error if the run does not exist or if querying fails.
		QueryRunStatus(ctx context.Context, runID string) (RunStatus, error)
	}

	// Signaler provides direct signaling by workflow ID/run ID without relying on
	// in-process workflow handles. Engines that support out-of-process signaling
	// (e.g., Temporal) should implement this interface so the runtime can deliver
	// Provide*/Pause/Resume signals across process restarts.
	Signaler interface {
		// SignalByID sends a signal to the given workflow identified by workflowID
		// and optional runID. The payload is engine-specific and must be
		// serializable by the engine client.
		SignalByID(ctx context.Context, workflowID, runID, name string, payload any) error
	}

	// WorkflowDefinition binds a workflow handler to a logical name and default queue.
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
	// and a typed RunInput, returning a typed RunOutput. Implementations must be
	// deterministic with respect to activity results.
	WorkflowFunc func(ctx WorkflowContext, input *api.RunInput) (*api.RunOutput, error)

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
		// SetQueryHandler registers a read-only query handler that can be invoked by
		// external clients to retrieve workflow state. Handlers must be deterministic
		// and side-effect free. Engines that do not support queries may implement
		// this as a no-op.
		SetQueryHandler(name string, handler any) error

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

		// Now returns the current workflow time in a deterministic manner. Implementations
		// must return a time source that is replay-safe (e.g., Temporal's workflow.Now).
		Now() time.Time

		// StartChildWorkflow starts a child workflow execution and returns a handle
		// to await its completion or cancel it. Implementations should honor the
		// provided workflow name, task queue and timeouts without requiring local
		// registration lookups in the parent process.
		StartChildWorkflow(ctx context.Context, req ChildWorkflowRequest) (ChildWorkflowHandle, error)
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
		// Input is the typed payload passed to the workflow handler.
		Input *api.RunInput
		// RunTimeout bounds the total workflow execution time at the engine level.
		// Zero means use the engine default (if any). Engines may map this to their
		// native execution timeout/TTL (Temporal: WorkflowRunTimeout/ExecutionTimeout).
		RunTimeout time.Duration
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
		// Name identifies the activity to execute (must match a registered name).
		Name string
		// Input is the payload passed to the activity handler.
		Input any
		// Queue optionally overrides the queue for this invocation. If empty, inherits
		// from the activity registration or workflow queue.
		Queue string
		// RetryPolicy controls retry behavior for the scheduled activity. If zero-valued,
		// uses the policy from the activity registration.
		RetryPolicy RetryPolicy
		// Timeout bounds the activity execution time. Zero means no timeout.
		Timeout time.Duration
	}

	// WorkflowHandle allows callers to interact with a running workflow. Returned
	// by Engine.StartWorkflow, it provides methods to wait for completion, send
	// signals, or cancel execution.
	WorkflowHandle interface {
		// Wait blocks until the workflow completes and returns the typed result.
		// Returns an error if the workflow fails or is cancelled.
		Wait(ctx context.Context) (*api.RunOutput, error)

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

	// ChildWorkflowRequest describes a child workflow to start from within an
	// existing workflow execution.
	ChildWorkflowRequest struct {
		// ID is the child workflow identifier, unique within the engine scope.
		ID string
		// Workflow is the provider workflow name to execute.
		Workflow string
		// TaskQueue is the queue to schedule the child on.
		TaskQueue string
		// Input is the payload passed to the child workflow handler.
		Input *api.RunInput
		// RunTimeout bounds the total child workflow execution time.
		RunTimeout time.Duration
		// RetryPolicy controls start retries for the child workflow start attempt.
		RetryPolicy RetryPolicy
	}

	// ChildWorkflowHandle allows a parent workflow to await/cancel a child workflow.
	ChildWorkflowHandle interface {
		// Get waits for child completion and returns the typed result.
		Get(ctx context.Context) (*api.RunOutput, error)
		// Cancel requests cancellation of the child workflow execution.
		Cancel(ctx context.Context) error
		// RunID returns the engine-assigned run identifier of the child.
		RunID() string
	}
)
