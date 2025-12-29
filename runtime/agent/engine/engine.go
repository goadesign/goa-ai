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
//   - Future[T]: Represents a pending activity result. Enables parallel execution
//     by allowing workflows to launch multiple activities and collect results later,
//     without reflection-based assignment.
//
//   - Receiver[T]: Delivers typed signals to workflows in a deterministic way.
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

		// RegisterHookActivity registers a typed activity that publishes workflow-emitted
		// hook events outside of the deterministic workflow thread. The activity accepts
		// *api.HookActivityInput and returns an error.
		RegisterHookActivity(ctx context.Context, name string, opts ActivityOptions, fn func(context.Context, *api.HookActivityInput) error) error

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
	// with the workflow engine (planner/tool activities and signal receivers)
	// must produce deterministic results when replayed. Direct I/O, random number
	// generation, or system time access within workflows violates determinism and
	// causes workflow failures.
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

		// PublishHook schedules the runtime hook activity and waits for completion.
		// Implementations must run hook publishing outside of the deterministic workflow
		// thread (e.g., via activities in Temporal) so subscribers can perform I/O.
		PublishHook(ctx context.Context, call HookActivityCall) error

		// ExecutePlannerActivity schedules a planner activity (PlanStart/PlanResume)
		// and blocks until it completes. Planner activities are executed outside the
		// deterministic workflow thread and may perform I/O.
		ExecutePlannerActivity(ctx context.Context, call PlannerActivityCall) (*api.PlanActivityOutput, error)

		// ExecuteToolActivity schedules a tool execution activity and blocks until it
		// completes. This is useful for sequential execution (finalizers, single tools).
		ExecuteToolActivity(ctx context.Context, call ToolActivityCall) (*api.ToolOutput, error)

		// ExecuteToolActivityAsync schedules a tool execution activity and returns a Future
		// so workflows can run multiple tools concurrently and collect results later.
		ExecuteToolActivityAsync(ctx context.Context, call ToolActivityCall) (Future[*api.ToolOutput], error)

		// PauseRequests returns a typed receiver for pause signals.
		PauseRequests() Receiver[api.PauseRequest]

		// ResumeRequests returns a typed receiver for resume signals.
		ResumeRequests() Receiver[api.ResumeRequest]

		// ClarificationAnswers returns a typed receiver for clarification answers.
		ClarificationAnswers() Receiver[api.ClarificationAnswer]

		// ExternalToolResults returns a typed receiver for external tool results.
		ExternalToolResults() Receiver[api.ToolResultsSet]

		// ConfirmationDecisions returns a typed receiver for tool confirmation decisions.
		ConfirmationDecisions() Receiver[api.ConfirmationDecision]

		// Now returns the current workflow time in a deterministic manner. Implementations
		// must return a time source that is replay-safe (e.g., Temporal's workflow.Now).
		Now() time.Time

		// NewTimer returns a Future that becomes ready after the given duration elapses
		// in workflow time. This is the engine-agnostic primitive for waking up on time
		// without polling.
		//
		// Implementations must schedule a deterministic timer (e.g., Temporal's
		// workflow.NewTimer). A non-positive duration should produce a Future that is
		// already ready.
		NewTimer(ctx context.Context, d time.Duration) (Future[time.Time], error)

		// Await blocks until condition returns true, or ctx is done.
		//
		// Condition must be deterministic and side-effect free. A typical use is to
		// wait on a set of Futures using IsReady() without draining them in a fixed
		// order (e.g., "wait until any tool future completes").
		Await(ctx context.Context, condition func() bool) error

		// StartChildWorkflow starts a child workflow execution and returns a handle
		// to await its completion or cancel it. Implementations should honor the
		// provided workflow name, task queue and timeouts without requiring local
		// registration lookups in the parent process.
		StartChildWorkflow(ctx context.Context, req ChildWorkflowRequest) (ChildWorkflowHandle, error)

		// WithCancel returns a derived WorkflowContext whose cancellation can be
		// triggered independently of the parent workflow scope. This is used to
		// cooperatively cancel in-flight activities/child workflows when the runtime
		// needs to finalize (e.g., time budget reached).
		//
		// In deterministic engines, this must map to a workflow-level cancel scope
		// (e.g., Temporal's workflow.WithCancel).
		WithCancel() (WorkflowContext, func())
	}

	// Future represents a pending activity result that will become available after
	// the activity completes. Futures enable parallel activity execution: workflows
	// can launch multiple tool activities and collect results later using Get(),
	// which blocks until the activity finishes.
	//
	// Thread-safety: Futures are bound to a single workflow execution and must not
	// be shared across workflow executions. Calling Get() multiple times is safe
	// and returns the same result/error on each call.
	//
	// Lifecycle: Valid from creation until the workflow completes. Get() must be
	// called before the workflow exits; abandoned futures leak workflow resources
	// in some engines. IsReady() enables polling without blocking.
	Future[T any] interface {
		// Get blocks until the activity completes and returns the typed result.
		// Calling Get multiple times on the same Future returns the same value/error.
		Get(ctx context.Context) (T, error)

		// IsReady returns true if the activity has completed (success or failure) and Get()
		// will not block. This allows workflows to poll or implement custom waiting strategies.
		IsReady() bool
	}

	// Receiver exposes typed workflow signal delivery in an engine-agnostic way.
	// Implementations wrap engine-specific channels (Temporal signal channels,
	// in-process Go channels, etc.) and provide blocking and non-blocking receive
	// helpers so workflow code can react to external events deterministically.
	Receiver[T any] interface {
		// Receive blocks until a signal value is delivered and returns it.
		// Implementations should respect ctx when possible; for engines that do not
		// support context cancellation, Receive may ignore ctx and rely on workflow
		// cancellation semantics instead.
		Receive(ctx context.Context) (T, error)

		// ReceiveAsync attempts to receive a signal without blocking.
		ReceiveAsync() (T, bool)
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

	// HookActivityCall describes a single invocation of the runtime hook publishing
	// activity from inside workflow code.
	HookActivityCall struct {
		// Name identifies the registered hook activity.
		Name string

		// Input is the typed payload passed to the activity handler.
		Input *api.HookActivityInput

		// Options overrides the registered activity defaults for this invocation.
		Options ActivityOptions
	}

	// PlannerActivityCall describes a single invocation of a PlanStart/PlanResume
	// activity from inside workflow code.
	PlannerActivityCall struct {
		// Name identifies the registered planner activity.
		Name string

		// Input is the typed payload passed to the activity handler.
		Input *api.PlanActivityInput

		// Options overrides the registered activity defaults for this invocation.
		Options ActivityOptions
	}

	// ToolActivityCall describes a single invocation of a tool execution activity
	// from inside workflow code.
	ToolActivityCall struct {
		// Name identifies the registered execute_tool activity.
		Name string

		// Input is the typed payload passed to the activity handler.
		Input *api.ToolInput

		// Options overrides the registered activity defaults for this invocation.
		Options ActivityOptions
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
		// IsReady returns true if the child workflow has completed (success or failure)
		// and Get() will not block.
		IsReady() bool
		// Cancel requests cancellation of the child workflow execution.
		Cancel(ctx context.Context) error
		// RunID returns the engine-assigned run identifier of the child.
		RunID() string
	}
)
