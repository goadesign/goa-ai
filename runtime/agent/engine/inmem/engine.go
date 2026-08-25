// Package inmem provides an in-memory workflow engine implementation for
// tests and local development.
//
// The in-memory engine is intentionally minimal:
//   - It runs workflow handlers in-process in goroutines (no durability).
//   - It does not provide Temporal-like determinism or replay semantics.
//   - Activity timeouts cancel the handler context; handlers must return when
//     canceled because Go cannot preempt an in-process function safely.
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

		recordActivities  map[string]recordActivityDef
		plannerActivities map[string]plannerActivityDef
		toolActivities    map[string]toolActivityDef

		// statuses tracks workflow status by run ID (inmem uses workflow ID as run ID).
		statuses map[string]engine.RunStatus
		// handles retain terminal results so runtime repair can recover the exact
		// workflow output by ID after its original caller detaches.
		handles map[string]*handle
	}

	// wfCtx adapts context.Context into engine.WorkflowContext.
	wfCtx struct {
		ctx   context.Context
		id    string
		runID string
		eng   *eng
		seq   *sequenceCounter
	}

	// handle is the in-memory implementation of engine.WorkflowHandle.
	handle struct {
		mu     sync.Mutex
		done   chan struct{}
		err    error
		result *api.RunOutput
	}

	// childHandle adapts an in-memory WorkflowHandle to engine.ChildWorkflowHandle.
	childHandle struct {
		h engine.WorkflowHandle
	}

	recordActivityDef struct {
		handler func(context.Context, *api.RecordActivityBatchInput) error
		opts    engine.ActivityOptions
	}

	sequenceCounter struct {
		mu   sync.Mutex
		next uint64
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
)

var (
	_ engine.Engine              = (*eng)(nil)
	_ engine.WorkflowHandle      = (*handle)(nil)
	_ engine.WorkflowContext     = (*wfCtx)(nil)
	_ engine.ChildWorkflowHandle = (*childHandle)(nil)
)

func (s *sequenceCounter) Next() uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	return s.next
}

// New returns a new in-memory workflow engine.
//
// This engine is intended for tests and local development only. It does not
// provide durability, determinism, or replay safety.
func New() engine.Engine {
	return &eng{
		statuses: make(map[string]engine.RunStatus),
		handles:  make(map[string]*handle),
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

// RegisterRecordActivity registers a typed runtime-record activity that
// persists one non-empty ordered batch outside deterministic workflow code.
func (e *eng) RegisterRecordActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.RecordActivityBatchInput) error) error {
	if name == "" {
		return errors.New("record activity name is required")
	}
	if fn == nil {
		return errors.New("record activity handler is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.recordActivities == nil {
		e.recordActivities = make(map[string]recordActivityDef)
	}
	if _, dup := e.recordActivities[name]; dup {
		return fmt.Errorf("record activity %q already registered", name)
	}
	e.recordActivities[name] = recordActivityDef{
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
		seq:   &sequenceCounter{},
	}

	h := &handle{done: make(chan struct{})}

	// Track workflow as running.
	e.mu.Lock()
	if e.statuses == nil {
		e.statuses = make(map[string]engine.RunStatus)
	}
	e.statuses[req.ID] = engine.RunStatusRunning
	e.handles[req.ID] = h
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
func (e *eng) QueryRunStatus(_ context.Context, workflowID string) (engine.RunStatus, error) {
	if workflowID == "" {
		return "", fmt.Errorf("workflow id is required")
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	status, ok := e.statuses[workflowID]
	if !ok {
		return "", engine.ErrWorkflowNotFound
	}
	return status, nil
}

// QueryRunCompletion returns the exact terminal result produced by the
// in-process workflow handler.
func (e *eng) QueryRunCompletion(ctx context.Context, workflowID string) (*api.RunOutput, error) {
	if workflowID == "" {
		return nil, errors.New("workflow id is required")
	}
	e.mu.RLock()
	h, ok := e.handles[workflowID]
	e.mu.RUnlock()
	if !ok {
		return nil, engine.ErrWorkflowNotFound
	}
	return h.Wait(ctx)
}

func (e *eng) CancelByID(_ context.Context, _ string) error {
	// In-memory: best-effort cancellation is not wired. The runtime may use this
	// in tests; returning nil preserves no-op semantics.
	return nil
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

func (w *wfCtx) NextSequence() uint64 {
	return w.seq.Next()
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

func (w *wfCtx) Await(condition func() bool) error {
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
		case <-w.ctx.Done():
			return w.ctx.Err()
		case <-ticker.C:
		}
	}
}

func (w *wfCtx) PublishRecords(call engine.RecordActivityCall) error {
	if call.Name == "" {
		return errors.New("record activity name is required")
	}
	if call.Input == nil {
		return errors.New("record activity input is required")
	}
	w.eng.mu.RLock()
	def, ok := w.eng.recordActivities[call.Name]
	w.eng.mu.RUnlock()
	if !ok {
		return fmt.Errorf("record activity %q not registered", call.Name)
	}
	timeout, _ := activityTimeout(call.Options, def.opts)
	actCtx, cancel, _ := withOptionalTimeout(w.ctx, timeout)
	defer cancel()
	err := def.handler(actCtx, call.Input)
	if errors.Is(actCtx.Err(), context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return err
}

func (w *wfCtx) ExecutePlannerActivity(call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
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
	timeout, totalDeadline := activityTimeout(call.Options, def.opts)
	actCtx, cancel, activityDeadline := withOptionalTimeout(w.ctx, timeout)
	defer cancel()
	out, err := def.handler(actCtx, call.Input)
	if totalDeadline && activityDeadline &&
		errors.Is(actCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: %w", engine.ErrPlannerActivityDeadlineExceeded, actCtx.Err())
	}
	if errors.Is(actCtx.Err(), context.DeadlineExceeded) {
		return nil, context.DeadlineExceeded
	}
	return out, err
}

func (w *wfCtx) ExecuteToolActivity(call engine.ToolActivityCall) (*api.ToolOutput, error) {
	fut, err := w.ExecuteToolActivityAsync(call)
	if err != nil {
		return nil, err
	}
	return fut.Get(w.ctx)
}

func (w *wfCtx) ExecuteToolActivityAsync(call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
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
		timeout, _ := activityTimeout(call.Options, def.opts)
		actCtx, cancel, _ := withOptionalTimeout(w.ctx, timeout)
		defer cancel()
		fut.result, fut.err = def.handler(actCtx, call.Input)
		if errors.Is(actCtx.Err(), context.DeadlineExceeded) {
			fut.result = nil
			fut.err = context.DeadlineExceeded
		}
	}()
	return fut, nil
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

// activityTimeout resolves the effective in-memory execution timeout. With no
// queue or retries, schedule-to-close and start-to-close are competing bounds;
// the shorter bound owns expiration.
func activityTimeout(override, defaults engine.ActivityOptions) (time.Duration, bool) {
	startToClose := override.StartToCloseTimeout
	if startToClose == 0 {
		startToClose = defaults.StartToCloseTimeout
	}
	scheduleToClose := override.ScheduleToCloseTimeout
	if scheduleToClose == 0 {
		scheduleToClose = defaults.ScheduleToCloseTimeout
	}
	if scheduleToClose > 0 && (startToClose == 0 || scheduleToClose <= startToClose) {
		return scheduleToClose, true
	}
	return startToClose, false
}

// withOptionalTimeout creates the activity deadline and reports whether it is
// earlier than the parent deadline. The result identifies which configured
// boundary owns cancellation without changing the cause visible to handlers.
func withOptionalTimeout(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc, bool) {
	if timeout <= 0 {
		return parent, func() {
		}, false
	}
	deadline := time.Now().Add(timeout)
	activityDeadline := true
	if parentDeadline, ok := parent.Deadline(); ok && !deadline.Before(parentDeadline) {
		activityDeadline = false
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	return ctx, cancel, activityDeadline
}
