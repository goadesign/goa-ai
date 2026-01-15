// Package inmem provides an in-memory workflow engine implementation for
// tests and local development.
//
// The in-memory engine is intentionally minimal:
// - It runs workflow handlers in-process in goroutines (no durability).
// - It does not provide Temporal-like determinism or replay semantics.
// - Activity and workflow timeouts are best-effort and use the standard library.
//
// This engine is useful for unit tests that want to exercise runtime logic
// without standing up an external workflow backend.
package inmem

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

type (
	// eng implements engine.Engine with an in-process goroutine runner.
	eng struct {
		mu sync.RWMutex

		workflows map[string]engine.WorkflowDefinition

		hookActivities    map[string]hookActivityDef
		plannerActivities map[string]plannerActivityDef
		toolActivities    map[string]toolActivityDef

		// statuses tracks workflow status by run ID (inmem uses workflow ID as run ID).
		statuses map[string]engine.RunStatus
	}

	// wfCtx adapts context.Context plus in-memory signal channels into engine.WorkflowContext.
	wfCtx struct {
		ctx   context.Context
		id    string
		runID string
		eng   *eng

		pauseCh       chan api.PauseRequest
		resumeCh      chan api.ResumeRequest
		clarifyCh     chan api.ClarificationAnswer
		toolResultsCh chan api.ToolResultsSet
		confirmCh     chan api.ConfirmationDecision
	}

	// handle is the in-memory implementation of engine.WorkflowHandle.
	handle struct {
		mu     sync.Mutex
		done   chan struct{}
		err    error
		result *api.RunOutput
		wfCtx  *wfCtx
	}

	// childHandle adapts an in-memory WorkflowHandle to engine.ChildWorkflowHandle.
	childHandle struct {
		h engine.WorkflowHandle
	}

	hookActivityDef struct {
		handler func(context.Context, *api.HookActivityInput) error
		opts    engine.ActivityOptions
	}

	plannerActivityDef struct {
		handler func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)
		opts    engine.ActivityOptions
	}

	toolActivityDef struct {
		handler func(context.Context, *api.ToolInput) (*api.ToolOutput, error)
		opts    engine.ActivityOptions
	}

	// future is a simple typed Future implementation backed by a channel.
	future[T any] struct {
		ready  chan struct{}
		result T
		err    error
	}

	// receiver is a typed in-memory signal receiver.
	receiver[T any] struct {
		ch chan T
	}
)

var (
	_ engine.Engine              = (*eng)(nil)
	_ engine.WorkflowHandle      = (*handle)(nil)
	_ engine.WorkflowContext     = (*wfCtx)(nil)
	_ engine.ChildWorkflowHandle = (*childHandle)(nil)
)

// New returns a new in-memory workflow engine.
//
// This engine is intended for tests and local development only. It does not
// provide durability, determinism, or replay safety.
func New() engine.Engine {
	return &eng{
		statuses: make(map[string]engine.RunStatus),
	}
}

func (e *eng) RegisterWorkflow(_ context.Context, def engine.WorkflowDefinition) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.workflows == nil {
		e.workflows = make(map[string]engine.WorkflowDefinition)
	}
	if _, dup := e.workflows[def.Name]; dup {
		return fmt.Errorf("workflow %q already registered", def.Name)
	}
	if def.Handler == nil || def.Name == "" {
		return errors.New("invalid workflow definition")
	}
	e.workflows[def.Name] = def
	return nil
}

// RegisterHookActivity registers a typed hook activity that publishes workflow-emitted
// hook events outside of deterministic workflow code.
func (e *eng) RegisterHookActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.HookActivityInput) error) error {
	if name == "" {
		return errors.New("hook activity name is required")
	}
	if fn == nil {
		return errors.New("hook activity handler is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.hookActivities == nil {
		e.hookActivities = make(map[string]hookActivityDef)
	}
	if _, dup := e.hookActivities[name]; dup {
		return fmt.Errorf("hook activity %q already registered", name)
	}
	e.hookActivities[name] = hookActivityDef{
		handler: fn,
		opts:    opts,
	}
	return nil
}

// RegisterPlannerActivity registers a typed planner activity (PlanStart/PlanResume).
func (e *eng) RegisterPlannerActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error {
	if name == "" {
		return errors.New("planner activity name is required")
	}
	if fn == nil {
		return errors.New("planner activity handler is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.plannerActivities == nil {
		e.plannerActivities = make(map[string]plannerActivityDef)
	}
	if _, dup := e.plannerActivities[name]; dup {
		return fmt.Errorf("planner activity %q already registered", name)
	}
	e.plannerActivities[name] = plannerActivityDef{
		handler: fn,
		opts:    opts,
	}
	return nil
}

// RegisterExecuteToolActivity registers a typed execute_tool activity.
func (e *eng) RegisterExecuteToolActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error {
	if name == "" {
		return errors.New("tool activity name is required")
	}
	if fn == nil {
		return errors.New("tool activity handler is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.toolActivities == nil {
		e.toolActivities = make(map[string]toolActivityDef)
	}
	if _, dup := e.toolActivities[name]; dup {
		return fmt.Errorf("tool activity %q already registered", name)
	}
	e.toolActivities[name] = toolActivityDef{
		handler: fn,
		opts:    opts,
	}
	return nil
}

func (e *eng) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	e.mu.RLock()
	def, ok := e.workflows[req.Workflow]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("workflow %q not registered", req.Workflow)
	}
	if req.ID == "" {
		return nil, errors.New("workflow id is required")
	}

	wctx := &wfCtx{
		ctx: ctx,
		id:  req.ID,
		// In-memory assigns workflow ID as run ID.
		runID: req.ID,
		eng:   e,

		pauseCh:       make(chan api.PauseRequest, 1),
		resumeCh:      make(chan api.ResumeRequest, 1),
		clarifyCh:     make(chan api.ClarificationAnswer, 1),
		toolResultsCh: make(chan api.ToolResultsSet, 1),
		confirmCh:     make(chan api.ConfirmationDecision, 1),
	}

	h := &handle{done: make(chan struct{}), wfCtx: wctx}

	// Track workflow as running.
	e.mu.Lock()
	if e.statuses == nil {
		e.statuses = make(map[string]engine.RunStatus)
	}
	e.statuses[req.ID] = engine.RunStatusRunning
	e.mu.Unlock()

	go func() {
		defer close(h.done)
		res, err := def.Handler(wctx, req.Input)
		h.mu.Lock()
		h.result = res
		h.err = err
		h.mu.Unlock()
		// Update status based on completion.
		e.mu.Lock()
		if err != nil {
			if errors.Is(err, context.Canceled) {
				e.statuses[req.ID] = engine.RunStatusCanceled
			} else {
				e.statuses[req.ID] = engine.RunStatusFailed
			}
		} else {
			e.statuses[req.ID] = engine.RunStatusCompleted
		}
		e.mu.Unlock()
	}()

	return h, nil
}

// QueryRunStatus returns the current lifecycle status for a workflow execution.
func (e *eng) QueryRunStatus(_ context.Context, runID string) (engine.RunStatus, error) {
	if runID == "" {
		return "", fmt.Errorf("run id is required")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	status, ok := e.statuses[runID]
	if !ok {
		return "", engine.ErrWorkflowNotFound
	}
	return status, nil
}

func (h *handle) Wait(ctx context.Context) (*api.RunOutput, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		return h.result, h.err
	}
}

func (h *handle) Signal(ctx context.Context, name string, payload any) error {
	switch name {
	case api.SignalPause:
		req, ok := payload.(api.PauseRequest)
		if !ok {
			return fmt.Errorf("signal %q expects api.PauseRequest, got %T", name, payload)
		}
		return sendSignal(ctx, h.done, h.wfCtx.pauseCh, req)

	case api.SignalResume:
		req, ok := payload.(api.ResumeRequest)
		if !ok {
			return fmt.Errorf("signal %q expects api.ResumeRequest, got %T", name, payload)
		}
		return sendSignal(ctx, h.done, h.wfCtx.resumeCh, req)

	case api.SignalProvideClarification:
		req, ok := payload.(api.ClarificationAnswer)
		if !ok {
			return fmt.Errorf("signal %q expects api.ClarificationAnswer, got %T", name, payload)
		}
		return sendSignal(ctx, h.done, h.wfCtx.clarifyCh, req)

	case api.SignalProvideToolResults:
		req, ok := payload.(api.ToolResultsSet)
		if !ok {
			return fmt.Errorf("signal %q expects api.ToolResultsSet, got %T", name, payload)
		}
		return sendSignal(ctx, h.done, h.wfCtx.toolResultsCh, req)

	case api.SignalProvideConfirmation:
		req, ok := payload.(api.ConfirmationDecision)
		if !ok {
			return fmt.Errorf("signal %q expects api.ConfirmationDecision, got %T", name, payload)
		}
		return sendSignal(ctx, h.done, h.wfCtx.confirmCh, req)

	default:
		return fmt.Errorf("unknown signal %q", name)
	}
}

func (h *handle) Cancel(_ context.Context) error {
	// In-memory: best-effort cancellation via context cancellation is not wired.
	// Return nil to match no-op behavior.
	return nil
}

func (c *childHandle) Get(ctx context.Context) (*api.RunOutput, error) {
	return c.h.Wait(ctx)
}

func (c *childHandle) IsReady() bool {
	if h, ok := c.h.(*handle); ok {
		select {
		case <-h.done:
			return true
		default:
			return false
		}
	}
	return false
}

func (c *childHandle) Cancel(ctx context.Context) error {
	return c.h.Cancel(ctx)
}

func (c *childHandle) RunID() string {
	return ""
}

func (w *wfCtx) Context() context.Context {
	return engine.WithWorkflowContext(w.ctx, w)
}

// SetQueryHandler is a no-op for the in-memory engine.
func (w *wfCtx) SetQueryHandler(name string, handler any) error {
	return nil
}

func (w *wfCtx) WorkflowID() string {
	return w.id
}

func (w *wfCtx) RunID() string {
	return w.runID
}

func (w *wfCtx) StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	h, err := w.eng.StartWorkflow(ctx, engine.WorkflowStartRequest{
		ID:          req.ID,
		Workflow:    req.Workflow,
		TaskQueue:   req.TaskQueue,
		Input:       req.Input,
		RunTimeout:  req.RunTimeout,
		RetryPolicy: req.RetryPolicy,
	})
	if err != nil {
		return nil, err
	}
	return &childHandle{h: h}, nil
}

func (w *wfCtx) Detached() engine.WorkflowContext {
	cctx := context.WithoutCancel(w.ctx)
	sub := *w
	sub.ctx = cctx
	return &sub
}

func (w *wfCtx) WithCancel() (engine.WorkflowContext, func()) {
	cctx, cancel := context.WithCancel(w.ctx)
	sub := *w
	sub.ctx = cctx
	return &sub, cancel
}

func (w *wfCtx) Now() time.Time {
	return time.Now()
}

func (w *wfCtx) NewTimer(ctx context.Context, d time.Duration) (engine.Future[time.Time], error) {
	now := time.Now()
	if d <= 0 {
		fut := &future[time.Time]{ready: make(chan struct{}), result: now}
		close(fut.ready)
		return fut, nil
	}
	fireAt := now.Add(d)
	fut := &future[time.Time]{ready: make(chan struct{})}
	go func() {
		defer close(fut.ready)
		select {
		case <-ctx.Done():
			fut.err = ctx.Err()
		case <-time.After(d):
			fut.result = fireAt
		}
	}()
	return fut, nil
}

func (w *wfCtx) Await(ctx context.Context, condition func() bool) error {
	if condition == nil {
		return errors.New("await condition is required")
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *wfCtx) PublishHook(ctx context.Context, call engine.HookActivityCall) error {
	if call.Name == "" {
		return errors.New("hook activity name is required")
	}
	if call.Input == nil {
		return errors.New("hook activity input is required")
	}
	w.eng.mu.RLock()
	def, ok := w.eng.hookActivities[call.Name]
	w.eng.mu.RUnlock()
	if !ok {
		return fmt.Errorf("hook activity %q not registered", call.Name)
	}
	timeout := call.Options.Timeout
	if timeout == 0 {
		timeout = def.opts.Timeout
	}
	actCtx, cancel := withOptionalTimeout(ctx, timeout)
	defer cancel()
	return def.handler(actCtx, call.Input)
}

func (w *wfCtx) ExecutePlannerActivity(ctx context.Context, call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	if call.Name == "" {
		return nil, errors.New("planner activity name is required")
	}
	if call.Input == nil {
		return nil, errors.New("planner activity input is required")
	}
	w.eng.mu.RLock()
	def, ok := w.eng.plannerActivities[call.Name]
	w.eng.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("planner activity %q not registered", call.Name)
	}
	timeout := call.Options.Timeout
	if timeout == 0 {
		timeout = def.opts.Timeout
	}
	actCtx, cancel := withOptionalTimeout(ctx, timeout)
	defer cancel()
	return def.handler(actCtx, call.Input)
}

func (w *wfCtx) ExecuteToolActivity(ctx context.Context, call engine.ToolActivityCall) (*api.ToolOutput, error) {
	fut, err := w.ExecuteToolActivityAsync(ctx, call)
	if err != nil {
		return nil, err
	}
	return fut.Get(ctx)
}

func (w *wfCtx) ExecuteToolActivityAsync(ctx context.Context, call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
	if call.Name == "" {
		return nil, errors.New("tool activity name is required")
	}
	if call.Input == nil {
		return nil, errors.New("tool activity input is required")
	}
	w.eng.mu.RLock()
	def, ok := w.eng.toolActivities[call.Name]
	w.eng.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("tool activity %q not registered", call.Name)
	}

	fut := &future[*api.ToolOutput]{ready: make(chan struct{})}
	go func() {
		defer close(fut.ready)
		timeout := call.Options.Timeout
		if timeout == 0 {
			timeout = def.opts.Timeout
		}
		actCtx, cancel := withOptionalTimeout(ctx, timeout)
		defer cancel()
		fut.result, fut.err = def.handler(actCtx, call.Input)
	}()
	return fut, nil
}

func (w *wfCtx) PauseRequests() engine.Receiver[api.PauseRequest] {
	return receiver[api.PauseRequest]{ch: w.pauseCh}
}

func (w *wfCtx) ResumeRequests() engine.Receiver[api.ResumeRequest] {
	return receiver[api.ResumeRequest]{ch: w.resumeCh}
}

func (w *wfCtx) ClarificationAnswers() engine.Receiver[api.ClarificationAnswer] {
	return receiver[api.ClarificationAnswer]{ch: w.clarifyCh}
}

func (w *wfCtx) ExternalToolResults() engine.Receiver[api.ToolResultsSet] {
	return receiver[api.ToolResultsSet]{ch: w.toolResultsCh}
}

func (w *wfCtx) ConfirmationDecisions() engine.Receiver[api.ConfirmationDecision] {
	return receiver[api.ConfirmationDecision]{ch: w.confirmCh}
}

func (r receiver[T]) Receive(ctx context.Context) (T, error) {
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case val := <-r.ch:
		return val, nil
	}
}

func (r receiver[T]) ReceiveAsync() (T, bool) {
	select {
	case val := <-r.ch:
		return val, true
	default:
		var zero T
		return zero, false
	}
}

func (f *future[T]) Get(ctx context.Context) (T, error) {
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-f.ready:
		return f.result, f.err
	}
}

func (f *future[T]) IsReady() bool {
	select {
	case <-f.ready:
		return true
	default:
		return false
	}
}

func sendSignal[T any](ctx context.Context, done <-chan struct{}, ch chan<- T, payload T) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return errors.New("workflow completed")
	case ch <- payload:
		return nil
	}
}

func withOptionalTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout <= 0 {
		return parent, func() {
		}
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	return ctx, cancel
}
