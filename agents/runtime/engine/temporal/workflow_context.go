package temporal

import (
	"context"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/agents/runtime/engine"
	"goa.design/goa-ai/agents/runtime/telemetry"
)

type temporalWorkflowContext struct {
	engine     *Engine
	ctx        workflow.Context
	workflowID string
	runID      string
	logger     telemetry.Logger
	metrics    telemetry.Metrics
	tracer     telemetry.Tracer
	baseCtx    context.Context
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
		baseCtx:    e.workflowBaseContext(info.WorkflowExecution.RunID),
	}
	e.trackWorkflowContext(wfCtx.runID, wfCtx)
	return wfCtx
}

type contextKey string

const (
	workflowIDKey contextKey = "temporal.workflow_id"
	runIDKey      contextKey = "temporal.run_id"
)

func (w *temporalWorkflowContext) Context() context.Context {
	ctx := w.baseCtx
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, workflowIDKey, w.workflowID)
	ctx = context.WithValue(ctx, runIDKey, w.runID)
	return ctx
}

func (w *temporalWorkflowContext) WorkflowID() string {
	return w.workflowID
}

func (w *temporalWorkflowContext) RunID() string {
	return w.runID
}

func (w *temporalWorkflowContext) ExecuteActivity(ctx context.Context, req engine.ActivityRequest, result any) error {
	actx := workflow.WithActivityOptions(w.ctx, w.activityOptionsFor(req))
	fut := workflow.ExecuteActivity(actx, req.Name, req.Input)
	return fut.Get(w.ctx, result)
}

func (w *temporalWorkflowContext) ExecuteActivityAsync(
	ctx context.Context,
	req engine.ActivityRequest,
) (engine.Future, error) {
	actx := workflow.WithActivityOptions(w.ctx, w.activityOptionsFor(req))
	fut := workflow.ExecuteActivity(actx, req.Name, req.Input)
	return &temporalFuture{future: fut, ctx: actx}, nil
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

func (w *temporalWorkflowContext) SignalChannel(name string) engine.SignalChannel {
	ch := workflow.GetSignalChannel(w.ctx, name)
	return &temporalSignalChannel{
		ctx: w.ctx,
		ch:  ch,
	}
}

func (w *temporalWorkflowContext) activityOptionsFor(req engine.ActivityRequest) workflow.ActivityOptions {
	defaults := w.engine.activityDefaultsFor(req.Name)

	queue := req.Queue
	if queue == "" {
		queue = defaults.Queue
	}
	if queue == "" {
		queue = w.engine.defaultQueue
	}

	timeout := req.Timeout
	if timeout == 0 {
		timeout = defaults.Timeout
	}
	if timeout == 0 {
		timeout = time.Minute
	}

	retry := mergeRetryPolicies(defaults.RetryPolicy, req.RetryPolicy)

	return workflow.ActivityOptions{
		StartToCloseTimeout: timeout,
		TaskQueue:           queue,
		RetryPolicy:         convertRetryPolicy(retry),
	}
}

type temporalFuture struct {
	future workflow.Future
	ctx    workflow.Context
}

func (f *temporalFuture) Get(_ context.Context, result any) error {
	return f.future.Get(f.ctx, result)
}

func (f *temporalFuture) IsReady() bool {
	return f.future.IsReady()
}

type temporalSignalChannel struct {
	ctx workflow.Context
	ch  workflow.ReceiveChannel
}

func (c *temporalSignalChannel) Receive(_ context.Context, dest any) error {
	c.ch.Receive(c.ctx, dest)
	return nil
}

func (c *temporalSignalChannel) ReceiveAsync(dest any) bool {
	return c.ch.ReceiveAsync(dest)
}

func (e *Engine) activityDefaultsFor(name string) engine.ActivityOptions {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.activityOptions[name]
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
		//nolint:gosec // MaxAttempts is validated at DSL eval time to be reasonable
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
