package inmem

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"reflect"
)

// New returns a new in-memory Engine implementation suitable for local
// development, tests, and simple single-process runs. It is not deterministic
// or replay-safe and should not be used for production workloads.
func New() engine.Engine { return &eng{} }

type eng struct {
	mu         sync.RWMutex
	workflows  map[string]engine.WorkflowDefinition
	activities map[string]engine.ActivityDefinition
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

func (e *eng) RegisterActivity(ctx context.Context, def engine.ActivityDefinition) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.activities == nil {
		e.activities = make(map[string]engine.ActivityDefinition)
	}
	if _, dup := e.activities[def.Name]; dup {
		return fmt.Errorf("activity %q already registered", def.Name)
	}
	if def.Handler == nil || def.Name == "" {
		return errors.New("invalid activity definition")
	}
	e.activities[def.Name] = def
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

	go func() {
		defer close(h.done)
		res, err := def.Handler(wctx, req.Input)
		h.mu.Lock()
		h.result = res
		h.err = err
		h.mu.Unlock()
	}()

	return h, nil
}

type handle struct {
	mu     sync.Mutex
	done   chan struct{}
	err    error
	result any
	wfCtx  *wfCtx
}

func (h *handle) Wait(ctx context.Context, result any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-h.done:
		h.mu.Lock()
		defer h.mu.Unlock()
		assignResult(result, h.result)
		return h.err
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

type wfCtx struct {
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

func (w *wfCtx) Context() context.Context   { return w.ctx }
func (w *wfCtx) WorkflowID() string         { return w.id }
func (w *wfCtx) RunID() string              { return w.runID }
func (w *wfCtx) Logger() telemetry.Logger   { return w.logger }
func (w *wfCtx) Metrics() telemetry.Metrics { return w.metrics }
func (w *wfCtx) Tracer() telemetry.Tracer   { return w.tracer }
func (w *wfCtx) Now() time.Time             { return time.Now() }

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
		res, err := def.Handler(ctx, req.Input)
		f.mu.Lock()
		f.result = res
		f.err = err
		f.mu.Unlock()
	}()
	return f, nil
}

type future struct {
	mu     sync.Mutex
	ready  chan struct{}
	result any
	err    error
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

type signalChan struct{ ch chan any }

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
