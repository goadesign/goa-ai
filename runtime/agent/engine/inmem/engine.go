// Package inmem provides an in-memory implementation of the workflow engine
// for testing and development.
package inmem

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type (
	eng struct {
		mu         sync.RWMutex
		workflows  map[string]engine.WorkflowDefinition
		activities map[string]inmemActivity
		statuses   map[string]engine.RunStatus // tracks workflow status by runID
	}

	childHandle struct {
		h engine.WorkflowHandle
	}

	handle struct {
		mu     sync.Mutex
		done   chan struct{}
		err    error
		result *api.RunOutput
		wfCtx  *wfCtx
	}

	wfCtx struct {
		ctx     context.Context
		id      string
		runID   string
		logger  telemetry.Logger
		metrics telemetry.Metrics
		tracer  telemetry.Tracer
		eng     *eng

		sigMu *sync.Mutex
		sigs  map[string]*signalChan
	}

	future struct {
		mu     sync.Mutex
		ready  chan struct{}
		result any
		err    error
	}

	signalChan struct{ ch chan any }

	inmemActivity struct {
		handler func(context.Context, any) (any, error)
		opts    engine.ActivityOptions
	}
)

// New returns a new in-memory Engine implementation suitable for local
// development, tests, and simple single-process runs. It is not deterministic
// or replay-safe and should not be used for production workloads.
func New() engine.Engine {
	return &eng{
		statuses: make(map[string]engine.RunStatus),
	}
}

func (e *eng) RegisterWorkflow(ctx context.Context, def engine.WorkflowDefinition) error {
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

// RegisterPlannerActivity registers a typed planner activity (PlanStart/PlanResume).
func (e *eng) RegisterPlannerActivity(ctx context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error {
	if name == "" || fn == nil {
		return errors.New("invalid planner activity definition")
	}
	return e.registerActivity(ctx, name, func(c context.Context, input any) (any, error) {
		in, _ := input.(*api.PlanActivityInput)
		if in == nil {
			if v, ok := input.(api.PlanActivityInput); ok {
				in = &v
			}
		}
		if in == nil {
			return nil, errors.New("invalid planner activity input")
		}
		return fn(c, in)
	}, opts)
}

// RegisterExecuteToolActivity registers a typed execute_tool activity.
func (e *eng) RegisterExecuteToolActivity(ctx context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error {
	if name == "" || fn == nil {
		return errors.New("invalid execute tool activity definition")
	}
	return e.registerActivity(ctx, name, func(c context.Context, input any) (any, error) {
		in, _ := input.(*api.ToolInput)
		if in == nil {
			if v, ok := input.(api.ToolInput); ok {
				in = &v
			}
		}
		if in == nil {
			return nil, errors.New("invalid tool activity input")
		}
		return fn(c, in)
	}, opts)
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
		ctx:     ctx,
		id:      req.ID,
		runID:   req.ID, // in-memory assigns same ID as run
		logger:  telemetry.NoopLogger{},
		metrics: telemetry.NoopMetrics{},
		tracer:  telemetry.NoopTracer{},
		eng:     e,
		sigMu:   &sync.Mutex{},
		sigs:    make(map[string]*signalChan),
	}

	h := &handle{done: make(chan struct{}), wfCtx: wctx}

	// Track workflow as running
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
		// Update status based on completion
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

func (e *eng) registerActivity(_ context.Context, name string, handler func(context.Context, any) (any, error), opts engine.ActivityOptions) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activities == nil {
		e.activities = make(map[string]inmemActivity)
	}
	if _, dup := e.activities[name]; dup {
		return fmt.Errorf("activity %q already registered", name)
	}
	if handler == nil || name == "" {
		return errors.New("invalid activity definition")
	}
	e.activities[name] = inmemActivity{handler: handler, opts: opts}
	return nil
}

// StartChildWorkflow starts a new in-memory workflow using the engine and returns an adapter handle.
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

func (c *childHandle) Get(ctx context.Context) (*api.RunOutput, error) {
	return c.h.Wait(ctx)
}
func (c *childHandle) Cancel(ctx context.Context) error { return c.h.Cancel(ctx) }
func (c *childHandle) RunID() string                    { return "" }

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
	ch := h.wfCtx.SignalChannel(name).(*signalChan)
	select {
	case ch.ch <- payload:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-h.done:
		return errors.New("workflow completed")
	}
}

func (h *handle) Cancel(ctx context.Context) error {
	// In-memory: best-effort cancellation via context cancellation is not wired.
	// Return nil to match no-op behavior.
	return nil
}

// QueryRunStatus returns the current lifecycle status for a workflow execution
// by checking the in-memory status map.
func (e *eng) QueryRunStatus(ctx context.Context, runID string) (engine.RunStatus, error) {
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

func (w *wfCtx) Context() context.Context   { return w.ctx }
func (w *wfCtx) WorkflowID() string         { return w.id }
func (w *wfCtx) RunID() string              { return w.runID }
func (w *wfCtx) Logger() telemetry.Logger   { return w.logger }
func (w *wfCtx) Metrics() telemetry.Metrics { return w.metrics }
func (w *wfCtx) Tracer() telemetry.Tracer   { return w.tracer }
func (w *wfCtx) Now() time.Time             { return time.Now() }

// SetQueryHandler is a no-op for the in-memory engine.
func (w *wfCtx) SetQueryHandler(name string, handler any) error { return nil }

func (w *wfCtx) ExecuteActivity(ctx context.Context, req engine.ActivityRequest, result any) error {
	fut, err := w.ExecuteActivityAsync(ctx, req)
	if err != nil {
		return err
	}
	return fut.Get(ctx, result)
}

func (w *wfCtx) ExecuteActivityAsync(ctx context.Context, req engine.ActivityRequest) (engine.Future, error) {
	w.eng.mu.RLock()
	def, ok := w.eng.activities[req.Name]
	w.eng.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("activity %q not registered", req.Name)
	}
	f := &future{ready: make(chan struct{})}
	go func() {
		defer close(f.ready)
		res, err := def.handler(ctx, req.Input)
		f.mu.Lock()
		f.result = res
		f.err = err
		f.mu.Unlock()
	}()
	return f, nil
}

func (f *future) Get(ctx context.Context, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-f.ready:
		f.mu.Lock()
		defer f.mu.Unlock()
		assignResult(result, f.result)
		return f.err
	}
}

func (f *future) IsReady() bool {
	select {
	case <-f.ready:
		return true
	default:
		return false
	}
}

func (s *signalChan) Receive(ctx context.Context, dest any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case v := <-s.ch:
		assignResult(dest, v)
		return nil
	}
}

func (s *signalChan) ReceiveAsync(dest any) bool {
	select {
	case v := <-s.ch:
		assignResult(dest, v)
		return true
	default:
		return false
	}
}

func (w *wfCtx) SignalChannel(name string) engine.SignalChannel {
	w.sigMu.Lock()
	defer w.sigMu.Unlock()
	ch, ok := w.sigs[name]
	if !ok {
		ch = &signalChan{ch: make(chan any, 1)}
		w.sigs[name] = ch
	}
	return ch
}

func assignResult(dst any, src any) {
	if dst == nil || src == nil {
		return
	}
	dv := reflect.ValueOf(dst)
	if dv.Kind() != reflect.Ptr || dv.IsNil() {
		return
	}
	sv := reflect.ValueOf(src)
	// Direct assignable types
	if sv.IsValid() && sv.Type().AssignableTo(dv.Elem().Type()) {
		dv.Elem().Set(sv)
		return
	}
	// Allow setting interface pointers when value implements the interface
	if dv.Elem().Kind() == reflect.Interface && sv.Type().Implements(dv.Elem().Type()) {
		dv.Elem().Set(sv)
		return
	}
}
