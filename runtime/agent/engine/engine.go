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
//     Generated workflow code uses this to schedule activities and start child
//     workflows. Implementations must ensure replay-safe behavior.
//
//   - WorkflowHandle: Represents a running workflow. Callers use handles to wait
//     for completion or cancel execution.
//
//   - Future[T]: Represents a pending activity result. Enables parallel execution
//     by allowing workflows to launch multiple activities and collect results later,
//     without reflection-based assignment.
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
//   - Starting child workflows through the replay-safe workflow context
//
// Activities (planner calls, tool execution) are NOT deterministic and can
// perform arbitrary I/O. The engine records activity inputs/outputs and replays
// them during workflow recovery.
//
// # Usage Pattern
//
//	// Create engine (Temporal for production)
//	eng, _ := temporal.NewWorker(temporal.Options{...})
//	defer eng.Close()
//
//	// Create runtime with engine
//	rt := runtime.New(runtimeStore, runtime.WithEngine(eng))
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
	"fmt"
	"math"
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
	// RunStatusTimedOut indicates the workflow exceeded its run deadline.
	RunStatusTimedOut RunStatus = "timed_out"
	// RunStatusFailed indicates the workflow failed permanently.
	RunStatusFailed RunStatus = "failed"
	// RunStatusCanceled indicates the workflow was canceled externally.
	RunStatusCanceled RunStatus = "canceled"
	// RunStatusPaused indicates the workflow engine reports a paused execution.
	RunStatusPaused RunStatus = "paused"
)

var (
	// ErrPlannerActivityDeadlineExceeded indicates that a planner activity
	// exhausted its ScheduleToCloseTimeout. Other planner timeout causes remain
	// distinguishable backend errors.
	ErrPlannerActivityDeadlineExceeded = errors.New("planner activity deadline exceeded")
	// ErrWorkflowNotFound indicates that no workflow execution exists for the given identifier.
	ErrWorkflowNotFound = errors.New("workflow not found")
	// ErrWorkflowCompleted indicates that a requested workflow mutation arrived
	// after the workflow had already completed.
	ErrWorkflowCompleted = errors.New("workflow completed")
	// ErrWorkflowStartConflict indicates that an existing workflow ID was
	// started with different immutable execution semantics.
	ErrWorkflowStartConflict = errors.New("workflow start conflict")
	// ErrChildWorkflowIDReuse indicates that a parent explicitly issued the
	// same single-use child workflow ID more than once.
	ErrChildWorkflowIDReuse = errors.New("child workflow id already used")
)

type (
	// WorkflowStartConflictError reports an attempt to reuse a queryable workflow
	// ID for a different request.
	WorkflowStartConflictError struct {
		ID string
	}

	// CancellationConflictError reports a request whose reason differs from the
	// reason already accepted for the workflow.
	CancellationConflictError struct {
		// RunID identifies the workflow whose first reason won.
		RunID string
		// Reason is the later reason that was rejected.
		Reason string
	}

	// NonRetryableActivityError marks malformed input or a deterministic contract
	// conflict that repeating the same activity call cannot repair.
	NonRetryableActivityError struct {
		Err error
	}

	// ChildWorkflowIDReuseError reports a second explicit child workflow start.
	// Deterministic replay of the original command is not a second start.
	ChildWorkflowIDReuseError struct {
		ID string
	}
)

// Error implements error.
func (e *NonRetryableActivityError) Error() string {
	return e.Err.Error()
}

// Unwrap exposes the contract error.
func (e *NonRetryableActivityError) Unwrap() error {
	return e.Err
}

// MarkActivityErrorNonRetryable preserves err while classifying it for workflow
// engines that support retry policies.
func MarkActivityErrorNonRetryable(err error) error {
	return &NonRetryableActivityError{Err: err}
}

// IsActivityErrorNonRetryable reports whether retrying the same activity input
// cannot succeed.
func IsActivityErrorNonRetryable(err error) bool {
	var target *NonRetryableActivityError
	return errors.As(err, &target)
}

// Error implements error.
func (e *ChildWorkflowIDReuseError) Error() string {
	return fmt.Sprintf("child workflow %q: %v", e.ID, ErrChildWorkflowIDReuse)
}

// Unwrap exposes ErrChildWorkflowIDReuse for errors.Is.
func (e *ChildWorkflowIDReuseError) Unwrap() error {
	return ErrChildWorkflowIDReuse
}

// Error implements error.
func (e *WorkflowStartConflictError) Error() string {
	return fmt.Sprintf("workflow %q: %v", e.ID, ErrWorkflowStartConflict)
}

// Unwrap exposes ErrWorkflowStartConflict for errors.Is.
func (e *WorkflowStartConflictError) Unwrap() error {
	return ErrWorkflowStartConflict
}

// Error implements error.
func (e *CancellationConflictError) Error() string {
	return fmt.Sprintf("run %q already accepted a different cancellation reason; rejected %q", e.RunID, e.Reason)
}

type (
	// Engine abstracts workflow registration and execution so adapters (Temporal,
	// in-memory, or custom) can be swapped without touching generated code.
	// Implementations translate these generic types into backend-specific primitives.
	Engine interface {
		// RegisterWorkflow registers a workflow definition with the engine.
		RegisterWorkflow(ctx context.Context, def WorkflowDefinition) error

		// RegisterStorageActivity registers the one typed activity that applies
		// runtime storage commands outside the deterministic workflow thread.
		RegisterStorageActivity(ctx context.Context, name string, opts ActivityOptions, fn func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error)) error

		// RegisterPlannerActivity registers a typed planner activity (PlanStart or
		// PlanResume) that accepts *api.PlanActivityInput and returns *api.PlanActivityOutput.
		RegisterPlannerActivity(ctx context.Context, name string, opts ActivityOptions, fn func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error

		// RegisterExecuteToolActivity registers a typed execute_tool activity that
		// accepts *api.ToolInput and returns *api.ToolOutput.
		RegisterExecuteToolActivity(ctx context.Context, name string, opts ActivityOptions, fn func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error

		// RegisterAgentChildActivity registers the typed activity that prepares one
		// child-agent run outside deterministic workflow code.
		RegisterAgentChildActivity(ctx context.Context, name string, opts ActivityOptions, fn func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error)) error

		// StartWorkflow binds req.ID to its immutable request while the backend can
		// still query that execution. An exact retry returns the original open or
		// closed execution handle. Reusing a queryable ID with changed semantics
		// returns WorkflowStartConflictError. Callers own durable command identity
		// after backend history expires and must not reopen settled work.
		StartWorkflow(ctx context.Context, req WorkflowStartRequest) (WorkflowHandle, error)

		// QueryRunCompletion returns the workflow status and, once closed, its
		// final output or workflow error. The method error is reserved for failure
		// to retrieve this information.
		QueryRunCompletion(ctx context.Context, workflowID string) (RunCompletion, error)
	}

	// RegistrationSealer allows an engine to stage workflow/activity registrations
	// until the runtime has finished building its full local registry. Worker-capable
	// engines use this to avoid polling with partially registered handlers.
	RegistrationSealer interface {
		// SealRegistration closes the registration phase and activates any staged
		// workers before the runtime starts serving traffic. Successful calls must
		// be idempotent and implementations must honor ctx as the activation
		// deadline. If activation fails because ctx ended, callers may retry.
		SealRegistration(ctx context.Context) error
	}

	// CancellationRequester sends a durable cancellation command to a workflow.
	CancellationRequester interface {
		// RequestCancellation waits until workflow code handles the request, then
		// cancels the workflow. An exact retry succeeds. A different reason returns
		// CancellationConflictError. A request that arrives after terminal work
		// starts returns ErrWorkflowCompleted.
		RequestCancellation(context.Context, CancellationRequest) error
	}

	// CancellationRequest identifies one workflow and its write-once reason.
	CancellationRequest struct {
		// RunID identifies the workflow to cancel.
		RunID string
		// Reason records why cancellation was requested.
		Reason string
	}

	// CancellationHandler records one cancellation request from workflow code.
	// It returns ErrWorkflowCompleted when terminal work has already started.
	CancellationHandler func(WorkflowContext, CancellationRequest) error

	// RunCompletion separates the workflow result from errors encountered while
	// retrieving it from the engine.
	RunCompletion struct {
		// Status is the workflow state observed by the same query.
		Status RunStatus
		// CompletedAt is the time the engine closed the workflow. It is zero while
		// the workflow is open.
		CompletedAt time.Time
		// Output is the value returned by a successfully completed workflow.
		Output *api.RunOutput
		// WorkflowError is the final error returned by a failed, canceled, or timed
		// out workflow.
		WorkflowError error
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
	// a uniform API for activity execution and observability.
	//
	// Implementations must ensure deterministic replay: operations that interact
	// with the workflow engine (planner/tool activities and child workflows)
	// must produce deterministic results when replayed. Direct I/O, random number
	// generation, or system time access within workflows violates determinism and
	// causes workflow failures.
	//
	// Thread-safety: WorkflowContext is bound to a single workflow execution and
	// must not be shared across goroutines. Workflow operations are
	// serialized by the workflow engine.
	//
	// Lifecycle: Created by the engine when a workflow starts and remains valid
	// until the workflow completes or fails. Do not cache WorkflowContext outside
	// the workflow function scope.
	WorkflowContext interface {
		// Context returns the Go carrier for workflow identity and runtime values.
		// Activity scheduling uses this WorkflowContext receiver as the authoritative
		// replay-aware cancellation scope.
		Context() context.Context
		// SetQueryHandler registers a read-only query handler that can be invoked by
		// external clients to retrieve workflow state. Handlers must be deterministic
		// and side-effect free. Engines that do not support queries may implement
		// this as a no-op.
		SetQueryHandler(name string, handler any) error

		// SetCancellationHandler registers the workflow function that durably
		// records cancellation before execution stops. Requests received before
		// registration wait for this handler.
		SetCancellationHandler(CancellationHandler) error

		// WorkflowID returns the unique identifier for this workflow execution.
		WorkflowID() string

		// RunID returns the engine-assigned run identifier, used for observability
		// and run-level correlation.
		RunID() string

		// ExecuteStorageActivity schedules one durable state change and waits for
		// its matching result. Implementations run storage outside the deterministic
		// workflow thread so stores and record consumers can do I/O.
		ExecuteStorageActivity(call StorageActivityCall) (*api.StorageActivityResult, error)

		// ExecutePlannerActivity schedules a planner activity (PlanStart/PlanResume)
		// and blocks until it completes. Planner activities are executed outside the
		// deterministic workflow thread and may perform I/O. Implementations return
		// ErrPlannerActivityDeadlineExceeded only when ScheduleToCloseTimeout expires;
		// queue, attempt, heartbeat, and activity errors retain their original cause.
		ExecutePlannerActivity(call PlannerActivityCall) (*api.PlanActivityOutput, error)

		// ExecuteToolActivity schedules a tool execution activity and blocks until it
		// completes. This is useful for sequential execution (finalizers, single tools).
		ExecuteToolActivity(call ToolActivityCall) (*api.ToolOutput, error)

		// ExecuteToolActivityAsync schedules a tool execution activity and returns a Future
		// so workflows can run multiple tools concurrently and collect results later.
		ExecuteToolActivityAsync(call ToolActivityCall) (Future[*api.ToolOutput], error)

		// ExecuteAgentChildActivity prepares one child-agent run outside workflow
		// code and returns the exact values recorded in workflow history.
		ExecuteAgentChildActivity(call AgentChildActivityCall) (*api.AgentChildActivityOutput, error)

		// Now returns the current workflow time in a deterministic manner. Implementations
		// must return a time source that is replay-safe (e.g., Temporal's workflow.Now).
		Now() time.Time

		// NextSequence returns the next replay-stable monotonic sequence number for
		// this workflow context. Runtime record dispatch uses it to stamp unique
		// durable event keys without relying on nondeterministic sources.
		NextSequence() uint64

		// NewTimer returns a Future that becomes ready after the given duration elapses
		// in workflow time. This is the engine-agnostic primitive for waking up on time
		// without polling.
		//
		// Implementations must schedule a deterministic timer (e.g., Temporal's
		// workflow.NewTimer). A non-positive duration should produce a Future that is
		// already ready.
		NewTimer(ctx context.Context, d time.Duration) (Future[time.Time], error)

		// Await blocks until condition returns true or the receiver-owned
		// workflow scope is canceled.
		//
		// Condition must be deterministic and side-effect free. A typical use is to
		// wait on a set of Futures using IsReady() without draining them in a fixed
		// order (e.g., "wait until any tool future completes").
		Await(condition func() bool) error

		// StartChildWorkflow issues one single-use child workflow ID and returns a
		// handle to await or cancel it. A second explicit call with the same ID
		// returns ChildWorkflowIDReuseError even when the request is identical or
		// the first child is closed. Deterministic engine replay of the original
		// call is not a second issuance. Implementations honor the provided
		// workflow name, task queue, and timeouts without parent-side registration
		// lookups.
		StartChildWorkflow(ctx context.Context, req ChildWorkflowRequest) (ChildWorkflowHandle, error)

		// Detached returns a derived WorkflowContext whose cancellation is disconnected
		// from the parent workflow scope.
		//
		// This is intended for cleanup/terminal work (e.g., emitting RunCompleted or
		// RunSuspended hooks) that should still be attempted even when the main workflow context is
		// canceled.
		Detached() WorkflowContext

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

	// ActivityOptions configures retry and timeouts for an activity.
	ActivityOptions struct {
		// Queue overrides the default activity queue. If empty, the activity inherits
		// the workflow's task queue.
		Queue string
		// RetryPolicy controls retry behavior for this activity. If zero-valued, the
		// engine uses its default retry policy.
		RetryPolicy RetryPolicy
		// ScheduleToStartTimeout bounds how long the activity may wait in the task
		// queue before a worker starts the attempt. Zero means leave queue-wait
		// unspecified here and let the engine adapter apply its own defaults.
		ScheduleToStartTimeout time.Duration
		// ScheduleToCloseTimeout bounds the activity's total elapsed lifetime from
		// scheduling through queueing, all attempts, and retry backoff. Zero means
		// leave the total lifetime unspecified.
		ScheduleToCloseTimeout time.Duration
		// StartToCloseTimeout bounds one activity attempt once a worker has started
		// executing it. This is the primary "healthy attempt" budget for planner and
		// tool work. Zero means use the engine default.
		StartToCloseTimeout time.Duration
		// HeartbeatTimeout bounds the maximum gap between heartbeats emitted by the
		// running activity. Zero disables heartbeat-based liveness detection.
		HeartbeatTimeout time.Duration
	}

	// StorageActivityCall describes one durable state change from workflow code.
	StorageActivityCall struct {
		// Name identifies the registered storage activity.
		Name string

		// Command selects one operation and carries its immutable records.
		Command *api.StorageActivityCommand

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

	// AgentChildActivityCall describes one child-agent preparation activity.
	AgentChildActivityCall struct {
		// Name identifies the registered child-agent activity.
		Name string

		// Input contains the validated parent call and transcript state.
		Input *api.AgentChildActivityInput

		// Options overrides the registered activity defaults for this invocation.
		Options ActivityOptions
	}

	// EncodedValue contains one memo value in the engine's portable wire form.
	// Metadata describes how to decode Data. Both fields contain mutable byte
	// slices: code that creates or retains an EncodedValue must deep-copy Data,
	// the Metadata map, and every metadata value so callers cannot change an
	// accepted workflow request after submission.
	EncodedValue struct {
		// Metadata describes the encoding stored in Data.
		Metadata map[string][]byte
		// Data contains the encoded value bytes.
		Data []byte
	}

	// WorkflowStartRequest describes how to launch a workflow execution. Generated
	// code constructs these when agents are invoked.
	WorkflowStartRequest struct {
		// ID is the required workflow identifier, which must be unique within the
		// engine scope. It is typically derived from the agent ID and a UUID.
		ID string
		// Workflow is the required name that a worker uses to select the workflow
		// handler.
		Workflow string
		// TaskQueue is the required queue that receives the workflow task. A worker
		// listening on this queue will execute the workflow.
		TaskQueue string
		// Input is the required typed payload passed to the workflow handler.
		Input *api.RunInput
		// RunTimeout bounds one workflow execution attempt. A retry starts a fresh
		// attempt with the same timeout. Zero means use the engine default.
		RunTimeout time.Duration
		// Memo stores exact encoded diagnostic payloads alongside the workflow
		// execution. Engines persist these bytes without decoding and re-encoding
		// them. Nil means no memo.
		Memo map[string]EncodedValue
		// SearchAttributes captures indexed metadata used for visibility queries.
		// Nil means no attributes are set.
		SearchAttributes map[string]any
		// RetryPolicy controls automatic retries after a workflow execution fails.
		// It is separate from activity retries.
		RetryPolicy RetryPolicy
	}

	// WorkflowHandle allows callers to interact with a running workflow. Returned
	// by Engine.StartWorkflow, it provides methods to wait for completion or cancel
	// execution.
	WorkflowHandle interface {
		// Wait blocks until the workflow completes and returns the typed result.
		// Returns an error if the workflow fails or is cancelled.
		Wait(ctx context.Context) (*api.RunOutput, error)

		// Cancel requests cancellation of the workflow. The workflow's context will be
		// cancelled, and in-flight activities may be cancelled depending on the engine.
		// Returns an error if cancellation fails.
		Cancel(ctx context.Context) error
	}

	// RetryPolicy defines retry semantics shared by workflows and activities.
	// An all-zero policy leaves the engine or registered activity policy
	// unchanged. Retry timing may be set only with a finite or unlimited attempt
	// policy so callers cannot submit a partial retry policy.
	RetryPolicy struct {
		// MaxAttempts caps total attempts, including the first. Zero leaves the
		// engine or registered activity default unchanged.
		MaxAttempts int
		// UnlimitedAttempts explicitly overrides a registered finite default.
		// It must not be combined with MaxAttempts.
		UnlimitedAttempts bool
		// InitialInterval is the delay before the first retry. Zero leaves the
		// existing default unchanged.
		InitialInterval time.Duration
		// BackoffCoefficient multiplies the delay after each retry. Zero leaves the
		// existing default unchanged. Values below 1 are invalid.
		BackoffCoefficient float64
	}

	// ChildWorkflowRequest describes a child workflow to start from within an
	// existing workflow execution.
	ChildWorkflowRequest struct {
		// ID is the required child workflow identifier, unique within the engine
		// scope.
		ID string
		// Workflow is the required provider workflow name to execute.
		Workflow string
		// TaskQueue is the required queue that receives the child workflow task.
		TaskQueue string
		// Input is the required payload passed to the child workflow handler.
		Input *api.RunInput
		// RunTimeout bounds one child workflow execution attempt.
		RunTimeout time.Duration
		// RetryPolicy controls retries after a child workflow execution fails.
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
	}
)

// ValidateWorkflowLaunchSettings checks the timeout and retry values shared by
// root and child workflow starts. Callers use it before decoding or retaining
// other launch values so malformed timing settings fail first.
func ValidateWorkflowLaunchSettings(runTimeout time.Duration, retry RetryPolicy) error {
	if runTimeout < 0 {
		return errors.New("workflow run timeout must not be negative")
	}
	if retry.MaxAttempts < 0 {
		return errors.New("workflow retry max attempts must not be negative")
	}
	if retry.InitialInterval < 0 {
		return errors.New("workflow retry initial interval must not be negative")
	}
	if coefficient := retry.BackoffCoefficient; math.IsNaN(coefficient) ||
		math.IsInf(coefficient, 0) || coefficient < 0 || coefficient > 0 && coefficient < 1 {
		return errors.New("workflow retry backoff coefficient must be zero or at least one")
	}
	if retry.UnlimitedAttempts && retry.MaxAttempts != 0 {
		return errors.New("workflow retry cannot set both unlimited attempts and max attempts")
	}
	if (retry.InitialInterval != 0 || retry.BackoffCoefficient != 0) &&
		retry.MaxAttempts == 0 && !retry.UnlimitedAttempts {
		return errors.New("workflow retry timing requires max attempts or unlimited attempts")
	}
	return nil
}

// ValidateWorkflowStartRequest rejects requests that official workflow engines
// cannot submit with the same meaning. Callers must provide the workflow ID,
// workflow name, task queue, and input because an engine must not guess them
// from local worker registration or configuration.
func ValidateWorkflowStartRequest(req WorkflowStartRequest) error {
	if req.ID == "" {
		return errors.New("workflow id is required")
	}
	if req.Workflow == "" {
		return errors.New("workflow name is required")
	}
	if req.TaskQueue == "" {
		return errors.New("workflow task queue is required")
	}
	if req.Input == nil {
		return errors.New("workflow input is required")
	}
	if req.ID != req.Input.RunID {
		return errors.New("workflow id must match input run id")
	}
	return ValidateWorkflowLaunchSettings(req.RunTimeout, req.RetryPolicy)
}

// ValidateChildWorkflowRequest rejects child requests that official workflow
// engines cannot start with the same meaning. Callers must provide every value
// needed to route and execute the child because an engine must not inherit a
// queue or input from its parent workflow.
func ValidateChildWorkflowRequest(req ChildWorkflowRequest) error {
	if req.ID == "" {
		return errors.New("child workflow id is required")
	}
	if req.Workflow == "" {
		return errors.New("child workflow name is required")
	}
	if req.TaskQueue == "" {
		return errors.New("child workflow task queue is required")
	}
	if req.Input == nil {
		return errors.New("child workflow input is required")
	}
	if req.ID != req.Input.RunID {
		return errors.New("child workflow id must match input run id")
	}
	return ValidateWorkflowLaunchSettings(req.RunTimeout, req.RetryPolicy)
}
