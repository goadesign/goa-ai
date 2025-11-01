//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"goa.design/goa-ai/runtime/agents/engine"
	"goa.design/goa-ai/runtime/agents/hooks"
	"goa.design/goa-ai/runtime/agents/interrupt"
	"goa.design/goa-ai/runtime/agents/model"
	"goa.design/goa-ai/runtime/agents/planner"
	"goa.design/goa-ai/runtime/agents/policy"
	runinmem "goa.design/goa-ai/runtime/agents/run/inmem"
	"goa.design/goa-ai/runtime/agents/telemetry"
	"goa.design/goa-ai/runtime/agents/tools"
)

// testWorkflowContext is a lightweight engine.WorkflowContext implementation used by tests.
type testWorkflowContext struct {
	ctx           context.Context
	lastRequest   engine.ActivityRequest
	asyncResult   any
	signals       map[string]*testSignalChannel
	sigMu         sync.Mutex
	planResult    planner.PlanResult
	hasPlanResult bool
	barrier       chan struct{}
}

func (t *testWorkflowContext) Context() context.Context   { return t.ctx }
func (t *testWorkflowContext) WorkflowID() string         { return "wf" }
func (t *testWorkflowContext) RunID() string              { return "run" }
func (t *testWorkflowContext) Logger() telemetry.Logger   { return telemetry.NoopLogger{} }
func (t *testWorkflowContext) Metrics() telemetry.Metrics { return telemetry.NoopMetrics{} }
func (t *testWorkflowContext) Tracer() telemetry.Tracer   { return telemetry.NoopTracer{} }
func (t *testWorkflowContext) Now() time.Time             { return time.Unix(0, 0) }

func (t *testWorkflowContext) SignalChannel(name string) engine.SignalChannel {
	t.sigMu.Lock()
	defer t.sigMu.Unlock()
	if t.signals == nil {
		t.signals = make(map[string]*testSignalChannel)
	}
	ch, ok := t.signals[name]
	if !ok {
		ch = &testSignalChannel{ch: make(chan any, 1)}
		t.signals[name] = ch
	}
	return ch
}

func (t *testWorkflowContext) ExecuteActivity(ctx context.Context, req engine.ActivityRequest, result any) error {
	if t.lastRequest.Name == "" {
		t.lastRequest = req
	}
	if out, ok := result.(*PlanActivityOutput); ok {
		res := planner.PlanResult{}
		if t.hasPlanResult {
			res = t.planResult
		}
		*out = PlanActivityOutput{Result: res}
	}
	return nil
}

func (t *testWorkflowContext) ExecuteActivityAsync(ctx context.Context, req engine.ActivityRequest) (engine.Future, error) {
	t.lastRequest = req
	result := t.asyncResult
	if result == nil {
		result = PlanActivityOutput{Result: planner.PlanResult{}}
	}
	return &testFuture{result: result, barrier: t.barrier}, nil
}

type testFuture struct {
	result  any
	err     error
	barrier chan struct{}
}

func (f *testFuture) Get(ctx context.Context, result any) error {
	if f.err != nil {
		return f.err
	}
	if f.barrier != nil {
		<-f.barrier
	}
	switch out := result.(type) {
	case *PlanActivityOutput:
		if res, ok := f.result.(PlanActivityOutput); ok {
			*out = res
		}
	case *ToolOutput:
		if res, ok := f.result.(ToolOutput); ok {
			*out = res
		}
	}
	return nil
}

func (f *testFuture) IsReady() bool { return true }

type testSignalChannel struct{ ch chan any }

func (s *testSignalChannel) Receive(ctx context.Context, dest any) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case val := <-s.ch:
		copySignalValue(dest, val)
		return nil
	}
}

func (s *testSignalChannel) ReceiveAsync(dest any) bool {
	select {
	case val := <-s.ch:
		copySignalValue(dest, val)
		return true
	default:
		return false
	}
}

func copySignalValue(dest any, value any) {
	switch out := dest.(type) {
	case *interrupt.PauseRequest:
		if req, ok := value.(interrupt.PauseRequest); ok {
			*out = req
		}
	case *interrupt.ResumeRequest:
		if req, ok := value.(interrupt.ResumeRequest); ok {
			*out = req
		}
	}
}

// routeWorkflowContext routes activity execution through registered handlers so tests can call
// runtime helpers without standing up a workflow engine.
type routeWorkflowContext struct {
	ctx     context.Context
	runID   string
	routes  map[string]engine.ActivityDefinition
	lastReq engine.ActivityRequest
	signals map[string]*testSignalChannel
	sigMu   sync.Mutex
}

func (r *routeWorkflowContext) Context() context.Context   { return r.ctx }
func (r *routeWorkflowContext) WorkflowID() string         { return "wf" }
func (r *routeWorkflowContext) RunID() string              { return r.runID }
func (r *routeWorkflowContext) Logger() telemetry.Logger   { return telemetry.NoopLogger{} }
func (r *routeWorkflowContext) Metrics() telemetry.Metrics { return telemetry.NoopMetrics{} }
func (r *routeWorkflowContext) Tracer() telemetry.Tracer   { return telemetry.NoopTracer{} }
func (r *routeWorkflowContext) Now() time.Time             { return time.Unix(0, 0) }

func (r *routeWorkflowContext) SignalChannel(name string) engine.SignalChannel {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	if r.signals == nil {
		r.signals = make(map[string]*testSignalChannel)
	}
	ch, ok := r.signals[name]
	if !ok {
		ch = &testSignalChannel{ch: make(chan any, 1)}
		r.signals[name] = ch
	}
	return ch
}

func (r *routeWorkflowContext) ExecuteActivity(ctx context.Context, req engine.ActivityRequest, result any) error {
	r.lastReq = req
	def, ok := r.routes[req.Name]
	if !ok {
		return nil
	}
	out, err := def.Handler(ctx, req.Input)
	if err != nil {
		return err
	}
	return copyActivityResult(result, out)
}

func (r *routeWorkflowContext) ExecuteActivityAsync(ctx context.Context, req engine.ActivityRequest) (engine.Future, error) {
	r.lastReq = req
	def, ok := r.routes[req.Name]
	if !ok {
		return &testFuture{}, nil
	}
	out, err := def.Handler(ctx, req.Input)
	return &testFuture{result: out, err: err}, nil
}

func copyActivityResult(dst any, src any) error {
	switch out := dst.(type) {
	case *PlanActivityOutput:
		v, ok := src.(PlanActivityOutput)
		if !ok {
			return fmt.Errorf("runtime: unexpected plan output type %T", src)
		}
		*out = v
	case *ToolOutput:
		v, ok := src.(ToolOutput)
		if !ok {
			return fmt.Errorf("runtime: unexpected tool output type %T", src)
		}
		*out = v
	}
	return nil
}

type stubPlanner struct {
	start  func(context.Context, planner.PlanInput) (planner.PlanResult, error)
	resume func(context.Context, planner.PlanResumeInput) (planner.PlanResult, error)
}

func (s *stubPlanner) PlanStart(ctx context.Context, input planner.PlanInput) (planner.PlanResult, error) {
	if s.start != nil {
		return s.start(ctx, input)
	}
	return planner.PlanResult{}, nil
}

func (s *stubPlanner) PlanResume(ctx context.Context, input planner.PlanResumeInput) (planner.PlanResult, error) {
	if s.resume != nil {
		return s.resume(ctx, input)
	}
	return planner.PlanResult{}, nil
}

type stubWorkflowHandle struct {
	lastSignal string
	payload    any
}

func (h *stubWorkflowHandle) Wait(context.Context, any) error { return nil }
func (h *stubWorkflowHandle) Signal(ctx context.Context, name string, payload any) error {
	h.lastSignal = name
	h.payload = payload
	return nil
}
func (h *stubWorkflowHandle) Cancel(context.Context) error { return nil }

type stubEngine struct{ last engine.WorkflowStartRequest }

func (s *stubEngine) RegisterWorkflow(context.Context, engine.WorkflowDefinition) error { return nil }
func (s *stubEngine) RegisterActivity(context.Context, engine.ActivityDefinition) error { return nil }
func (s *stubEngine) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	s.last = req
	return noopWorkflowHandle{}, nil
}

type noopWorkflowHandle struct{}

func (noopWorkflowHandle) Wait(context.Context, any) error           { return nil }
func (noopWorkflowHandle) Signal(context.Context, string, any) error { return nil }
func (noopWorkflowHandle) Cancel(context.Context) error              { return nil }

func newTestRuntimeWithPlanner(agentID string, pl planner.Planner) *Runtime {
	return &Runtime{
		agents:    map[string]AgentRegistration{agentID: {Planner: pl}},
		toolsets:  make(map[string]ToolsetRegistration),
		toolSpecs: make(map[string]tools.ToolSpec),
		logger:    telemetry.NoopLogger{},
		metrics:   telemetry.NoopMetrics{},
		tracer:    telemetry.NoopTracer{},
		Bus:       noopHooks{},
		models:    make(map[string]model.Client),
		RunStore:  runinmem.New(),
	}
}

type recordingHooks struct{ events []hooks.Event }

func (r *recordingHooks) Publish(ctx context.Context, event hooks.Event) error {
	r.events = append(r.events, event)
	return nil
}

func (r *recordingHooks) Register(h hooks.Subscriber) (hooks.Subscription, error) {
	return noopSubscription{}, nil
}

type noopHooks struct{}

func (noopHooks) Publish(context.Context, hooks.Event) error { return nil }
func (noopHooks) Register(hooks.Subscriber) (hooks.Subscription, error) {
	return noopSubscription{}, nil
}

type noopSubscription struct{}

func (noopSubscription) Close() error { return nil }

type stubPolicyEngine struct{ decision policy.Decision }

func (s *stubPolicyEngine) Decide(context.Context, policy.Input) (policy.Decision, error) {
	return s.decision, nil
}

func newAnyJSONSpec(name string) tools.ToolSpec {
	codec := tools.JSONCodec[any]{
		ToJSON: json.Marshal,
		FromJSON: func(data []byte) (any, error) {
			if len(bytes.TrimSpace(data)) == 0 || string(bytes.TrimSpace(data)) == "null" {
				return nil, nil
			}
			var out any
			if err := json.Unmarshal(data, &out); err != nil {
				return nil, err
			}
			return out, nil
		},
	}
	return tools.ToolSpec{
		Name:    name,
		Toolset: "ts",
		Payload: tools.TypeSpec{Name: name + "_payload", Codec: codec},
		Result:  tools.TypeSpec{Name: name + "_result", Codec: codec},
	}
}
