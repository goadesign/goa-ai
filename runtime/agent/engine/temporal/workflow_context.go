// Package temporal contains the goa-ai Temporal engine adapter.
//
// This file defines the Temporal-backed implementation of engine.WorkflowContext.
// The runtime uses it to:
// - execute typed planner/tool/hook activities with engine-owned defaults,
// - access deterministic time/timers and workflow cancellation,
// - receive external signals in a replay-safe way,
// - start child workflows by explicit name and queue.
//
// Contract:
//   - Activity option defaults are resolved by name and merged with per-call overrides.
//   - Temporal cancellation errors are normalized to context.Canceled for runtime-wide
//     classification that does not depend on Temporal types.
package temporal

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	temporalWorkflowContext struct {
		engine     *Engine
		ctx        workflow.Context
		workflowID string
		runID      string
		logger     telemetry.Logger
		metrics    telemetry.Metrics
		tracer     telemetry.Tracer
		baseCtx    context.Context
	}

	contextKey string

	temporalChildHandle struct {
		future workflow.ChildWorkflowFuture
		ctx    workflow.Context
		runID  string
		cancel workflow.CancelFunc
	}

	temporalFuture[T any] struct {
		future workflow.Future
		ctx    workflow.Context
	}

	temporalTimerFuture struct {
		future workflow.Future
		ctx    workflow.Context
		fireAt time.Time
	}

	immediateFuture[T any] struct {
		v T
	}

	temporalReceiver[T any] struct {
		ctx workflow.Context
		ch  workflow.ReceiveChannel
	}
)

const (
	workflowIDKey contextKey = "temporal.workflow_id"
	runIDKey      contextKey = "temporal.run_id"
)

// NewWorkflowContext adapts a Temporal workflow.Context into the engine.WorkflowContext
// used by the goa-ai runtime.
//
// This is intended for workflows that run in the same Temporal worker as the goa-ai
// engine but are not started through it, and still need to call runtime helpers
// (for example ExecuteAgentChildWithRoute).
//
// The returned context uses engine defaults (queue, timeouts, retry) when invoking
// typed planner/tool/hook activities.
func NewWorkflowContext(e *Engine, ctx workflow.Context) engine.WorkflowContext {
	return newTemporalWorkflowContext(e, ctx)
}

func newTemporalWorkflowContext(e *Engine, ctx workflow.Context) *temporalWorkflowContext {
	info := workflow.GetInfo(ctx)
	wfCtx := &temporalWorkflowContext{
		engine:     e,
		ctx:        ctx,
		workflowID: info.WorkflowExecution.ID,
		runID:      info.WorkflowExecution.RunID,
		logger:     e.logger,
		metrics:    e.metrics,
		tracer:     e.tracer,
		// NOTE: workflow execution is distributed and replayed; we cannot rely on
		// any process-local "base context registry" to initialize child workflows.
		// For deterministic behavior, build the base context from scratch and rely
		// on Temporal interceptors/propagators for trace context.
		baseCtx: context.Background(),
	}
	e.trackWorkflowContext(wfCtx.runID, wfCtx)
	return wfCtx
}

// normalizeTemporalError translates Temporal cancellation errors to context.Canceled.
//
// The runtime uses context cancellation to classify cancellations uniformly across
// engine backends without depending on Temporal SDK error types.
func normalizeTemporalError(err error) error {
	if err == nil {
		return nil
	}
	if temporal.IsCanceledError(err) {
		return context.Canceled
	}
	return err
}

func mergeRetryPolicies(base, override engine.RetryPolicy) engine.RetryPolicy {
	result := base
	if override.MaxAttempts != 0 {
		result.MaxAttempts = override.MaxAttempts
	}
	if override.InitialInterval != 0 {
		result.InitialInterval = override.InitialInterval
	}
	if override.BackoffCoefficient != 0 {
		result.BackoffCoefficient = override.BackoffCoefficient
	}
	return result
}

func convertRetryPolicy(r engine.RetryPolicy) *temporal.RetryPolicy {
	if r.MaxAttempts == 0 && r.InitialInterval == 0 && r.BackoffCoefficient == 0 {
		return nil
	}

	policy := &temporal.RetryPolicy{}
	if r.MaxAttempts > 0 {
		//nolint:gosec // MaxAttempts is validated at DSL eval time to be reasonable.
		policy.MaximumAttempts = int32(r.MaxAttempts)
	}
	if r.InitialInterval > 0 {
		policy.InitialInterval = r.InitialInterval
	}
	if r.BackoffCoefficient > 0 {
		policy.BackoffCoefficient = r.BackoffCoefficient
	}
	return policy
}

func (w *temporalWorkflowContext) Context() context.Context {
	ctx := context.WithValue(w.baseCtx, workflowIDKey, w.workflowID)
	ctx = context.WithValue(ctx, runIDKey, w.runID)
	return engine.WithWorkflowContext(ctx, w)
}

func (w *temporalWorkflowContext) SetQueryHandler(name string, handler any) error {
	return workflow.SetQueryHandler(w.ctx, name, handler)
}

func (w *temporalWorkflowContext) WorkflowID() string {
	return w.workflowID
}

func (w *temporalWorkflowContext) RunID() string {
	return w.runID
}

func (w *temporalWorkflowContext) PublishHook(ctx context.Context, call engine.HookActivityCall) error {
	if call.Name == "" {
		return errors.New("hook activity name is required")
	}
	if call.Input == nil {
		return errors.New("hook activity input is required")
	}

	actx := workflow.WithActivityOptions(w.ctx, w.activityOptionsFor(call.Name, call.Options))
	fut := workflow.ExecuteActivity(actx, call.Name, call.Input)
	var ignored struct{}
	return fut.Get(actx, &ignored)
}

func (w *temporalWorkflowContext) ExecutePlannerActivity(ctx context.Context, call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	if call.Name == "" {
		return nil, errors.New("planner activity name is required")
	}
	if call.Input == nil {
		return nil, errors.New("planner activity input is required")
	}

	actx := workflow.WithActivityOptions(w.ctx, w.activityOptionsFor(call.Name, call.Options))
	fut := workflow.ExecuteActivity(actx, call.Name, call.Input)
	var out *api.PlanActivityOutput
	if err := fut.Get(actx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (w *temporalWorkflowContext) ExecuteToolActivity(ctx context.Context, call engine.ToolActivityCall) (*api.ToolOutput, error) {
	fut, err := w.ExecuteToolActivityAsync(ctx, call)
	if err != nil {
		return nil, err
	}
	return fut.Get(ctx)
}

func (w *temporalWorkflowContext) ExecuteToolActivityAsync(ctx context.Context, call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
	if call.Name == "" {
		return nil, errors.New("tool activity name is required")
	}
	if call.Input == nil {
		return nil, errors.New("tool activity input is required")
	}

	actx := workflow.WithActivityOptions(w.ctx, w.activityOptionsFor(call.Name, call.Options))
	fut := workflow.ExecuteActivity(actx, call.Name, call.Input)
	return &temporalFuture[*api.ToolOutput]{future: fut, ctx: actx}, nil
}

func (w *temporalWorkflowContext) PauseRequests() engine.Receiver[api.PauseRequest] {
	ch := workflow.GetSignalChannel(w.ctx, api.SignalPause)
	return &temporalReceiver[api.PauseRequest]{
		ctx: w.ctx,
		ch:  ch,
	}
}

func (w *temporalWorkflowContext) ResumeRequests() engine.Receiver[api.ResumeRequest] {
	ch := workflow.GetSignalChannel(w.ctx, api.SignalResume)
	return &temporalReceiver[api.ResumeRequest]{
		ctx: w.ctx,
		ch:  ch,
	}
}

func (w *temporalWorkflowContext) ClarificationAnswers() engine.Receiver[api.ClarificationAnswer] {
	ch := workflow.GetSignalChannel(w.ctx, api.SignalProvideClarification)
	return &temporalReceiver[api.ClarificationAnswer]{
		ctx: w.ctx,
		ch:  ch,
	}
}

func (w *temporalWorkflowContext) ExternalToolResults() engine.Receiver[api.ToolResultsSet] {
	ch := workflow.GetSignalChannel(w.ctx, api.SignalProvideToolResults)
	return &temporalReceiver[api.ToolResultsSet]{
		ctx: w.ctx,
		ch:  ch,
	}
}

func (w *temporalWorkflowContext) ConfirmationDecisions() engine.Receiver[api.ConfirmationDecision] {
	ch := workflow.GetSignalChannel(w.ctx, api.SignalProvideConfirmation)
	return &temporalReceiver[api.ConfirmationDecision]{
		ctx: w.ctx,
		ch:  ch,
	}
}

func (w *temporalWorkflowContext) Logger() telemetry.Logger {
	return w.logger
}

func (w *temporalWorkflowContext) Metrics() telemetry.Metrics {
	return w.metrics
}

func (w *temporalWorkflowContext) Tracer() telemetry.Tracer {
	return w.tracer
}

func (w *temporalWorkflowContext) Now() time.Time {
	return workflow.Now(w.ctx)
}

func (w *temporalWorkflowContext) NewTimer(ctx context.Context, d time.Duration) (engine.Future[time.Time], error) {
	now := workflow.Now(w.ctx)
	if d <= 0 {
		return immediateFuture[time.Time]{v: now}, nil
	}

	fireAt := now.Add(d)
	fut := workflow.NewTimer(w.ctx, d)
	return &temporalTimerFuture{future: fut, ctx: w.ctx, fireAt: fireAt}, nil
}

func (w *temporalWorkflowContext) Await(ctx context.Context, condition func() bool) error {
	if condition == nil {
		return errors.New("await condition is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return workflow.Await(w.ctx, condition)
}

func (w *temporalWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	cctx, cancel := workflow.WithCancel(w.ctx)
	return &temporalWorkflowContext{
			engine:     w.engine,
			ctx:        cctx,
			workflowID: w.workflowID,
			runID:      w.runID,
			logger:     w.logger,
			metrics:    w.metrics,
			tracer:     w.tracer,
			baseCtx:    w.baseCtx,
		}, func() {
			cancel()
		}
}

func (w *temporalWorkflowContext) activityOptionsFor(name string, override engine.ActivityOptions) workflow.ActivityOptions {
	defaults := w.engine.activityDefaultsFor(name)

	queue := override.Queue
	if queue == "" {
		queue = defaults.Queue
	}
	if queue == "" {
		queue = w.engine.defaultQueue
	}

	timeout := override.Timeout
	if timeout == 0 {
		timeout = defaults.Timeout
	}
	if timeout == 0 {
		timeout = time.Minute
	}

	retry := mergeRetryPolicies(defaults.RetryPolicy, override.RetryPolicy)

	return workflow.ActivityOptions{
		// Bound both queue wait time and execution time to the effective timeout.
		// Without ScheduleToStartTimeout, a workflow can block until its run timeout
		// when workers are unavailable, preventing deterministic deadline handling
		// in the runtime.
		ScheduleToStartTimeout: timeout,
		StartToCloseTimeout:    timeout,
		TaskQueue:              queue,
		RetryPolicy:            convertRetryPolicy(retry),
	}
}

// StartChildWorkflow starts a Temporal child workflow with explicit workflow name and task queue.
//
// This avoids parent-side registration lookups: the caller supplies the workflow name and the
// engine starts it directly in Temporal.
func (w *temporalWorkflowContext) StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	opts := workflow.ChildWorkflowOptions{
		WorkflowID:         req.ID,
		TaskQueue:          req.TaskQueue,
		WorkflowRunTimeout: req.RunTimeout,
		RetryPolicy:        convertRetryPolicy(req.RetryPolicy),
	}

	cctx := workflow.WithChildOptions(w.ctx, opts)
	cctx, cancel := workflow.WithCancel(cctx)
	fut := workflow.ExecuteChildWorkflow(cctx, req.Workflow, req.Input)
	return &temporalChildHandle{future: fut, ctx: cctx, cancel: cancel}, nil
}

func (w *temporalWorkflowContext) Detached() engine.WorkflowContext {
	dctx, _ := workflow.NewDisconnectedContext(w.ctx)
	return &temporalWorkflowContext{
		engine:     w.engine,
		ctx:        dctx,
		workflowID: w.workflowID,
		runID:      w.runID,
		logger:     w.logger,
		metrics:    w.metrics,
		tracer:     w.tracer,
		baseCtx:    w.baseCtx,
	}
}

func (h *temporalChildHandle) Get(_ context.Context) (*api.RunOutput, error) {
	var out *api.RunOutput
	if err := h.future.Get(h.ctx, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (h *temporalChildHandle) IsReady() bool {
	return h.future.IsReady()
}

func (h *temporalChildHandle) Cancel(_ context.Context) error {
	h.cancel()
	return nil
}

func (h *temporalChildHandle) RunID() string {
	// Child run IDs are not always exposed synchronously; return the cached value.
	return h.runID
}

func (f *temporalFuture[T]) Get(_ context.Context) (T, error) {
	var out T
	if err := f.future.Get(f.ctx, &out); err != nil {
		return out, normalizeTemporalError(err)
	}
	return out, nil
}

func (f *temporalFuture[T]) IsReady() bool {
	return f.future.IsReady()
}

func (f *temporalTimerFuture) Get(_ context.Context) (time.Time, error) {
	var ignored struct{}
	if err := f.future.Get(f.ctx, &ignored); err != nil {
		return time.Time{}, normalizeTemporalError(err)
	}
	return f.fireAt, nil
}

func (f *temporalTimerFuture) IsReady() bool {
	return f.future.IsReady()
}

func (f immediateFuture[T]) Get(ctx context.Context) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	return f.v, nil
}

func (f immediateFuture[T]) IsReady() bool {
	return true
}

// Receive blocks until a signal value is delivered and returns it.
//
// Temporal receives signals on the workflow context (not the provided ctx). We still
// honor ctx cancellation before blocking so callers can enforce deadlines in a
// deterministic way.
func (r *temporalReceiver[T]) Receive(ctx context.Context) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}

	var out T
	r.ch.Receive(r.ctx, &out)
	return out, nil
}

// ReceiveWithTimeout blocks until a signal value is delivered or the timeout elapses.
//
// This is implemented using a workflow timer so it is replay-safe and allows runtime
// code to enforce global run budgets while awaiting external signals.
func (r *temporalReceiver[T]) ReceiveWithTimeout(ctx context.Context, timeout time.Duration) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	if timeout <= 0 {
		var zero T
		return zero, context.DeadlineExceeded
	}

	var (
		out      T
		got      bool
		timedOut bool
	)

	timerCtx, cancel := workflow.WithCancel(r.ctx)
	timer := workflow.NewTimer(timerCtx, timeout)
	sel := workflow.NewSelector(r.ctx)
	sel.AddReceive(r.ch, func(c workflow.ReceiveChannel, _ bool) {
		cancel()
		c.Receive(r.ctx, &out)
		got = true
	})
	sel.AddFuture(timer, func(workflow.Future) {
		timedOut = true
	})
	sel.Select(r.ctx)
	cancel()

	if got {
		return out, nil
	}
	if timedOut {
		var zero T
		return zero, context.DeadlineExceeded
	}

	var zero T
	return zero, errors.New("temporal receiver: select returned without signal or timeout")
}

// ReceiveAsync attempts to receive a signal value without blocking.
func (r *temporalReceiver[T]) ReceiveAsync() (T, bool) {
	var out T
	if ok := r.ch.ReceiveAsync(&out); ok {
		return out, true
	}
	return out, false
}

func (e *Engine) activityDefaultsFor(name string) engine.ActivityOptions {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activityOptions[name]
}
