//nolint:lll // allow long lines in test literals for readability
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// testWorkflowContext is a lightweight engine.WorkflowContext implementation used by tests.
type testWorkflowContext struct {
	ctx context.Context

	lastHookCall    engine.HookActivityCall
	lastPlannerCall engine.PlannerActivityCall
	lastToolCall    engine.ToolActivityCall

	asyncResult ToolOutput

	sigMu         sync.Mutex
	pauseCh       chan api.PauseRequest
	resumeCh      chan api.ResumeRequest
	clarifyCh     chan api.ClarificationAnswer
	toolResultsCh chan api.ToolResultsSet
	confirmCh     chan api.ConfirmationDecision

	planResult    *planner.PlanResult
	hasPlanResult bool
	barrier       chan struct{}
	hookRuntime   *Runtime // optional runtime for hook activity execution
	runtime       *Runtime // optional runtime for activity execution (plan/resume/execute)
	childRuntime  *Runtime // optional runtime for child workflow execution

	childRequests      []engine.ChildWorkflowRequest
	firstChildGetCount int
	sawFirstChildGet   bool

	toolFutures map[string]*controlledToolFuture

	controlledChildHandles chan *controlledChildHandle

	// parent points to the original context when this is a derived context from WithCancel.
	// Test assertions can use the parent to inspect lastToolCall even when the call was
	// scheduled on a child context.
	parent *testWorkflowContext
}

func (t *testWorkflowContext) root() *testWorkflowContext {
	if t.parent != nil {
		return t.parent
	}
	return t
}

func (t *testWorkflowContext) Context() context.Context {
	if t.ctx == nil {
		panic("testWorkflowContext.ctx is nil")
	}
	return engine.WithWorkflowContext(t.ctx, t)
}

func (t *testWorkflowContext) WorkflowID() string {
	return "wf"
}

func (t *testWorkflowContext) RunID() string {
	return "run"
}

func (t *testWorkflowContext) Detached() engine.WorkflowContext {
	if t.ctx == nil {
		panic("testWorkflowContext.ctx is nil")
	}
	cctx := context.WithoutCancel(t.ctx)
	root := t.root()
	sub := &testWorkflowContext{
		ctx: cctx,

		lastHookCall:    t.lastHookCall,
		lastPlannerCall: t.lastPlannerCall,
		lastToolCall:    t.lastToolCall,

		asyncResult: t.asyncResult,

		planResult:    t.planResult,
		hasPlanResult: t.hasPlanResult,
		barrier:       t.barrier,
		hookRuntime:   t.hookRuntime,
		runtime:       t.runtime,
		childRuntime:  t.childRuntime,

		childRequests:      t.childRequests,
		firstChildGetCount: t.firstChildGetCount,
		sawFirstChildGet:   t.sawFirstChildGet,

		toolFutures: t.toolFutures,

		controlledChildHandles: t.controlledChildHandles,
		parent:                 root,
	}
	return sub
}

func (t *testWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	if t.ctx == nil {
		panic("testWorkflowContext.ctx is nil")
	}
	cctx, cancel := context.WithCancel(t.ctx)
	root := t.root()
	sub := &testWorkflowContext{
		ctx: cctx,

		lastHookCall:    t.lastHookCall,
		lastPlannerCall: t.lastPlannerCall,
		lastToolCall:    t.lastToolCall,

		asyncResult: t.asyncResult,

		planResult:    t.planResult,
		hasPlanResult: t.hasPlanResult,
		barrier:       t.barrier,
		hookRuntime:   t.hookRuntime,
		runtime:       t.runtime,
		childRuntime:  t.childRuntime,

		childRequests:      t.childRequests,
		firstChildGetCount: t.firstChildGetCount,
		sawFirstChildGet:   t.sawFirstChildGet,

		toolFutures:            t.toolFutures,
		controlledChildHandles: t.controlledChildHandles,
		parent:                 root,
	}
	return sub, cancel
}

func (t *testWorkflowContext) Now() time.Time {
	return time.Unix(0, 0)
}

func (t *testWorkflowContext) NewTimer(ctx context.Context, d time.Duration) (engine.Future[time.Time], error) {
	now := time.Now()
	if d <= 0 {
		fut := &controlledTimeFuture{ready: make(chan struct{}), v: now}
		close(fut.ready)
		return fut, nil
	}
	fireAt := now.Add(d)
	fut := &controlledTimeFuture{ready: make(chan struct{}), v: fireAt}
	go func() {
		defer close(fut.ready)
		select {
		case <-ctx.Done():
			fut.err = ctx.Err()
		case <-time.After(d):
		}
	}()
	return fut, nil
}

func (t *testWorkflowContext) Await(ctx context.Context, condition func() bool) error {
	if condition == nil {
		return fmt.Errorf("await condition is required")
	}
	ticker := time.NewTicker(1 * time.Millisecond)
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

func (t *testWorkflowContext) SetQueryHandler(name string, handler any) error {
	return nil
}

func (t *testWorkflowContext) StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	t.childRequests = append(t.childRequests, req)
	// Also update parent if this is a derived context so tests can track from root.
	if t.parent != nil {
		t.parent.childRequests = append(t.parent.childRequests, req)
	}
	if t.controlledChildHandles != nil {
		h := &controlledChildHandle{
			ready: make(chan struct{}),
			out:   &api.RunOutput{},
		}
		t.controlledChildHandles <- h
		return h, nil
	}
	childRT := t.childRuntime
	if childRT == nil {
		childRT = t.runtime
	}
	return &testChildHandle{
		runtime: childRT,
		request: req,
		wfCtx:   t,
	}, nil
}

func (t *testWorkflowContext) PublishHook(ctx context.Context, call engine.HookActivityCall) error {
	t.lastHookCall = call
	hookRT := t.hookRuntime
	if hookRT == nil {
		hookRT = t.runtime
	}
	if hookRT == nil {
		return nil
	}
	if call.Name != hookActivityName {
		return fmt.Errorf("unexpected hook activity name %q", call.Name)
	}
	return hookRT.hookActivity(ctx, call.Input)
}

func (t *testWorkflowContext) ExecutePlannerActivity(ctx context.Context, call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	t.lastPlannerCall = call
	switch call.Name {
	case "plan", "nested.plan":
		if t.runtime != nil {
			return t.runtime.PlanStartActivity(ctx, call.Input)
		}
	case "resume", "nested.resume":
		if t.runtime != nil {
			return t.runtime.PlanResumeActivity(ctx, call.Input)
		}
	}

	var result *planner.PlanResult
	if t.hasPlanResult {
		result = t.planResult
	}
	return &PlanActivityOutput{
		Result:     result,
		Transcript: nil,
	}, nil
}

func (t *testWorkflowContext) ExecuteToolActivity(ctx context.Context, call engine.ToolActivityCall) (*api.ToolOutput, error) {
	fut, err := t.ExecuteToolActivityAsync(ctx, call)
	if err != nil {
		return nil, err
	}
	return fut.Get(ctx)
}

func (t *testWorkflowContext) ExecuteToolActivityAsync(ctx context.Context, call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
	t.lastToolCall = call
	// Also update parent if this is a derived context, so tests can inspect from the root.
	if t.parent != nil {
		t.parent.lastToolCall = call
	}

	if call.Input != nil && call.Input.ToolCallID != "" && len(t.toolFutures) > 0 {
		if fut, ok := t.toolFutures[call.Input.ToolCallID]; ok && fut != nil {
			return fut, nil
		}
	}

	fut := &testToolFuture{
		barrier: t.barrier,
	}

	switch call.Name {
	case "execute", "nested.execute":
		if t.runtime != nil {
			fut.result, fut.err = t.runtime.ExecuteToolActivity(ctx, call.Input)
			return fut, nil
		}
	}

	result := t.asyncResult
	fut.result = &result
	return fut, nil
}

func (t *testWorkflowContext) PauseRequests() engine.Receiver[api.PauseRequest] {
	root := t.root()
	root.ensureSignals()
	return testReceiver[api.PauseRequest]{ch: root.pauseCh}
}

func (t *testWorkflowContext) ResumeRequests() engine.Receiver[api.ResumeRequest] {
	root := t.root()
	root.ensureSignals()
	return testReceiver[api.ResumeRequest]{ch: root.resumeCh}
}

func (t *testWorkflowContext) ClarificationAnswers() engine.Receiver[api.ClarificationAnswer] {
	root := t.root()
	root.ensureSignals()
	return testReceiver[api.ClarificationAnswer]{ch: root.clarifyCh}
}

func (t *testWorkflowContext) ExternalToolResults() engine.Receiver[api.ToolResultsSet] {
	root := t.root()
	root.ensureSignals()
	return testReceiver[api.ToolResultsSet]{ch: root.toolResultsCh}
}

func (t *testWorkflowContext) ConfirmationDecisions() engine.Receiver[api.ConfirmationDecision] {
	root := t.root()
	root.ensureSignals()
	return testReceiver[api.ConfirmationDecision]{ch: root.confirmCh}
}

func (t *testWorkflowContext) ensureSignals() {
	t.sigMu.Lock()
	defer t.sigMu.Unlock()
	if t.pauseCh == nil {
		t.pauseCh = make(chan api.PauseRequest, 1)
	}
	if t.resumeCh == nil {
		t.resumeCh = make(chan api.ResumeRequest, 1)
	}
	if t.clarifyCh == nil {
		t.clarifyCh = make(chan api.ClarificationAnswer, 1)
	}
	if t.toolResultsCh == nil {
		t.toolResultsCh = make(chan api.ToolResultsSet, 1)
	}
	if t.confirmCh == nil {
		t.confirmCh = make(chan api.ConfirmationDecision, 1)
	}
}

type controlledTimeFuture struct {
	ready chan struct{}
	v     time.Time
	err   error
}

func (f *controlledTimeFuture) Get(ctx context.Context) (time.Time, error) {
	select {
	case <-ctx.Done():
		return time.Time{}, ctx.Err()
	case <-f.ready:
		return f.v, f.err
	}
}

func (f *controlledTimeFuture) IsReady() bool {
	select {
	case <-f.ready:
		return true
	default:
		return false
	}
}

type testToolFuture struct {
	result  *api.ToolOutput
	err     error
	barrier chan struct{}
}

func (f *testToolFuture) Get(ctx context.Context) (*api.ToolOutput, error) {
	if f.barrier != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.barrier:
		}
	}
	return f.result, f.err
}

func (f *testToolFuture) IsReady() bool {
	return true
}

type controlledToolFuture struct {
	ready chan struct{}
	out   *api.ToolOutput
	err   error
}

func (f *controlledToolFuture) Get(ctx context.Context) (*api.ToolOutput, error) {
	if f == nil {
		return nil, fmt.Errorf("nil future")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.ready:
		return f.out, f.err
	}
}

func (f *controlledToolFuture) IsReady() bool {
	if f == nil {
		return true
	}
	select {
	case <-f.ready:
		return true
	default:
		return false
	}
}

type controlledChildHandle struct {
	ready chan struct{}
	out   *api.RunOutput
	err   error
}

func (h *controlledChildHandle) Get(ctx context.Context) (*api.RunOutput, error) {
	if h == nil {
		return nil, fmt.Errorf("nil child handle")
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.ready:
		return h.out, h.err
	}
}

func (h *controlledChildHandle) IsReady() bool {
	if h == nil {
		return true
	}
	select {
	case <-h.ready:
		return true
	default:
		return false
	}
}

func (h *controlledChildHandle) Cancel(ctx context.Context) error { return nil }

func (h *controlledChildHandle) RunID() string { return "" }

type testReceiver[T any] struct{ ch chan T }

func (r testReceiver[T]) Receive(ctx context.Context) (T, error) {
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case val := <-r.ch:
		return val, nil
	}
}

// ReceiveWithTimeout blocks until a value is delivered or the timeout elapses.
func (r testReceiver[T]) ReceiveWithTimeout(ctx context.Context, timeout time.Duration) (T, error) {
	if err := ctx.Err(); err != nil {
		var zero T
		return zero, err
	}
	if timeout <= 0 {
		var zero T
		return zero, context.DeadlineExceeded
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-timer.C:
		var zero T
		return zero, context.DeadlineExceeded
	case val := <-r.ch:
		return val, nil
	}
}

func (r testReceiver[T]) ReceiveAsync() (T, bool) {
	select {
	case val := <-r.ch:
		return val, true
	default:
		var zero T
		return zero, false
	}
}

// routeWorkflowContext routes activity execution through registered handlers so tests can call
// runtime helpers without standing up a workflow engine.
type routeWorkflowContext struct {
	ctx   context.Context
	runID string

	plannerRoutes map[string]func(context.Context, *PlanActivityInput) (*PlanActivityOutput, error)
	toolRoutes    map[string]func(context.Context, *ToolInput) (*ToolOutput, error)

	lastHookCall    engine.HookActivityCall
	lastPlannerCall engine.PlannerActivityCall
	lastToolCall    engine.ToolActivityCall

	sigMu         sync.Mutex
	pauseCh       chan api.PauseRequest
	resumeCh      chan api.ResumeRequest
	clarifyCh     chan api.ClarificationAnswer
	toolResultsCh chan api.ToolResultsSet
	confirmCh     chan api.ConfirmationDecision

	hookRuntime  *Runtime // optional runtime for hook activity execution
	childRuntime *Runtime // optional runtime for child workflow execution

	parent *routeWorkflowContext
}

func (r *routeWorkflowContext) root() *routeWorkflowContext {
	if r.parent != nil {
		return r.parent
	}
	return r
}

func (r *routeWorkflowContext) Context() context.Context {
	if r.ctx == nil {
		panic("routeWorkflowContext.ctx is nil")
	}
	return engine.WithWorkflowContext(r.ctx, r)
}

func (r *routeWorkflowContext) WorkflowID() string {
	return "wf"
}

func (r *routeWorkflowContext) RunID() string {
	return r.runID
}

func (r *routeWorkflowContext) Detached() engine.WorkflowContext {
	if r.ctx == nil {
		panic("routeWorkflowContext.ctx is nil")
	}
	cctx := context.WithoutCancel(r.ctx)
	root := r.root()
	sub := &routeWorkflowContext{
		ctx:   cctx,
		runID: r.runID,

		plannerRoutes: r.plannerRoutes,
		toolRoutes:    r.toolRoutes,

		lastHookCall:    r.lastHookCall,
		lastPlannerCall: r.lastPlannerCall,
		lastToolCall:    r.lastToolCall,

		hookRuntime:  r.hookRuntime,
		childRuntime: r.childRuntime,

		parent: root,
	}
	return sub
}

func (r *routeWorkflowContext) WithCancel() (engine.WorkflowContext, func()) {
	if r.ctx == nil {
		panic("routeWorkflowContext.ctx is nil")
	}
	cctx, cancel := context.WithCancel(r.ctx)
	root := r.root()
	sub := &routeWorkflowContext{
		ctx:   cctx,
		runID: r.runID,

		plannerRoutes: r.plannerRoutes,
		toolRoutes:    r.toolRoutes,

		lastHookCall:    r.lastHookCall,
		lastPlannerCall: r.lastPlannerCall,
		lastToolCall:    r.lastToolCall,

		hookRuntime:  r.hookRuntime,
		childRuntime: r.childRuntime,

		parent: root,
	}
	return sub, cancel
}

func (r *routeWorkflowContext) Now() time.Time {
	return time.Unix(0, 0)
}

func (r *routeWorkflowContext) NewTimer(ctx context.Context, d time.Duration) (engine.Future[time.Time], error) {
	now := time.Now()
	if d <= 0 {
		fut := &controlledTimeFuture{ready: make(chan struct{}), v: now}
		close(fut.ready)
		return fut, nil
	}
	fireAt := now.Add(d)
	fut := &controlledTimeFuture{ready: make(chan struct{}), v: fireAt}
	go func() {
		defer close(fut.ready)
		select {
		case <-ctx.Done():
			fut.err = ctx.Err()
		case <-time.After(d):
		}
	}()
	return fut, nil
}

func (r *routeWorkflowContext) Await(ctx context.Context, condition func() bool) error {
	if condition == nil {
		return fmt.Errorf("await condition is required")
	}
	ticker := time.NewTicker(1 * time.Millisecond)
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

func (r *routeWorkflowContext) SetQueryHandler(name string, handler any) error {
	return nil
}

func (r *routeWorkflowContext) StartChildWorkflow(ctx context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	return &testChildHandle{
		runtime: r.childRuntime,
		request: req,
		wfCtx:   r,
	}, nil
}

func (r *routeWorkflowContext) PublishHook(ctx context.Context, call engine.HookActivityCall) error {
	r.lastHookCall = call
	if call.Name != hookActivityName {
		return fmt.Errorf("unexpected hook activity name %q", call.Name)
	}
	if r.hookRuntime == nil {
		return nil
	}
	return r.hookRuntime.hookActivity(ctx, call.Input)
}

func (r *routeWorkflowContext) ExecutePlannerActivity(ctx context.Context, call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	r.lastPlannerCall = call
	handler, ok := r.plannerRoutes[call.Name]
	if !ok {
		return nil, fmt.Errorf("no planner route for activity %q", call.Name)
	}
	return handler(ctx, call.Input)
}

func (r *routeWorkflowContext) ExecuteToolActivity(ctx context.Context, call engine.ToolActivityCall) (*api.ToolOutput, error) {
	fut, err := r.ExecuteToolActivityAsync(ctx, call)
	if err != nil {
		return nil, err
	}
	return fut.Get(ctx)
}

func (r *routeWorkflowContext) ExecuteToolActivityAsync(ctx context.Context, call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
	r.lastToolCall = call
	handler, ok := r.toolRoutes[call.Name]
	if !ok {
		return nil, fmt.Errorf("no tool route for activity %q", call.Name)
	}

	fut := &testToolFuture{}
	fut.result, fut.err = handler(ctx, call.Input)
	return fut, nil
}

func (r *routeWorkflowContext) PauseRequests() engine.Receiver[api.PauseRequest] {
	root := r.root()
	root.ensureSignals()
	return testReceiver[api.PauseRequest]{ch: root.pauseCh}
}

func (r *routeWorkflowContext) ResumeRequests() engine.Receiver[api.ResumeRequest] {
	root := r.root()
	root.ensureSignals()
	return testReceiver[api.ResumeRequest]{ch: root.resumeCh}
}

func (r *routeWorkflowContext) ClarificationAnswers() engine.Receiver[api.ClarificationAnswer] {
	root := r.root()
	root.ensureSignals()
	return testReceiver[api.ClarificationAnswer]{ch: root.clarifyCh}
}

func (r *routeWorkflowContext) ExternalToolResults() engine.Receiver[api.ToolResultsSet] {
	root := r.root()
	root.ensureSignals()
	return testReceiver[api.ToolResultsSet]{ch: root.toolResultsCh}
}

func (r *routeWorkflowContext) ConfirmationDecisions() engine.Receiver[api.ConfirmationDecision] {
	root := r.root()
	root.ensureSignals()
	return testReceiver[api.ConfirmationDecision]{ch: root.confirmCh}
}

func (r *routeWorkflowContext) ensureSignals() {
	r.sigMu.Lock()
	defer r.sigMu.Unlock()
	if r.pauseCh == nil {
		r.pauseCh = make(chan api.PauseRequest, 1)
	}
	if r.resumeCh == nil {
		r.resumeCh = make(chan api.ResumeRequest, 1)
	}
	if r.clarifyCh == nil {
		r.clarifyCh = make(chan api.ClarificationAnswer, 1)
	}
	if r.toolResultsCh == nil {
		r.toolResultsCh = make(chan api.ToolResultsSet, 1)
	}
	if r.confirmCh == nil {
		r.confirmCh = make(chan api.ConfirmationDecision, 1)
	}
}

type stubPlanner struct {
	start  func(context.Context, *planner.PlanInput) (*planner.PlanResult, error)
	resume func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error)
}

func (s *stubPlanner) PlanStart(ctx context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
	if s.start != nil {
		return s.start(ctx, input)
	}
	return &planner.PlanResult{}, nil
}

func (s *stubPlanner) PlanResume(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
	if s.resume != nil {
		return s.resume(ctx, input)
	}
	return &planner.PlanResult{}, nil
}

type stubWorkflowHandle struct {
	lastSignal string
	payload    any
}

func (h *stubWorkflowHandle) Wait(context.Context) (*api.RunOutput, error) {
	return &api.RunOutput{}, nil
}
func (h *stubWorkflowHandle) Signal(ctx context.Context, name string, payload any) error {
	h.lastSignal = name
	h.payload = payload
	return nil
}
func (h *stubWorkflowHandle) Cancel(context.Context) error { return nil }

type stubEngine struct{ last engine.WorkflowStartRequest }

func (s *stubEngine) RegisterWorkflow(context.Context, engine.WorkflowDefinition) error { return nil }
func (s *stubEngine) RegisterHookActivity(context.Context, string, engine.ActivityOptions, func(context.Context, *api.HookActivityInput) error) error {
	return nil
}
func (s *stubEngine) RegisterPlannerActivity(context.Context, string, engine.ActivityOptions, func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error {
	return nil
}
func (s *stubEngine) RegisterExecuteToolActivity(context.Context, string, engine.ActivityOptions, func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error {
	return nil
}
func (s *stubEngine) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	s.last = req
	return noopWorkflowHandle{}, nil
}

func (s *stubEngine) QueryRunStatus(context.Context, string) (engine.RunStatus, error) {
	return engine.RunStatusCompleted, nil
}

type noopWorkflowHandle struct{}

func (noopWorkflowHandle) Wait(context.Context) (*api.RunOutput, error) { return &api.RunOutput{}, nil }
func (noopWorkflowHandle) Signal(context.Context, string, any) error    { return nil }
func (noopWorkflowHandle) Cancel(context.Context) error                 { return nil }

func newTestRuntimeWithPlanner(agentID agent.Ident, pl planner.Planner) *Runtime {
	return &Runtime{
		agents:        map[agent.Ident]AgentRegistration{agentID: {Planner: pl}},
		toolsets:      make(map[string]ToolsetRegistration),
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		RunEventStore: runloginmem.New(),
		Bus:           noopHooks{},
		models:        make(map[string]model.Client),
	}
}

type recordingHooks struct {
	mu     sync.Mutex
	events []hooks.Event
	ch     chan hooks.Event
}

func (r *recordingHooks) Publish(ctx context.Context, event hooks.Event) error {
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	if r.ch != nil {
		r.ch <- event
	}
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

type testChildHandle struct {
	runtime *Runtime
	request engine.ChildWorkflowRequest
	wfCtx   engine.WorkflowContext
}

func (h *testChildHandle) Get(ctx context.Context) (*api.RunOutput, error) {
	if tw, ok := h.wfCtx.(*testWorkflowContext); ok {
		if !tw.sawFirstChildGet {
			tw.sawFirstChildGet = true
			tw.firstChildGetCount = len(tw.childRequests)
		}
		// Also update parent if this is a derived context.
		if tw.parent != nil && !tw.parent.sawFirstChildGet {
			tw.parent.sawFirstChildGet = true
			tw.parent.firstChildGetCount = len(tw.childRequests)
		}
	}
	if h.runtime != nil && h.request.Input != nil {
		// Execute the nested agent workflow
		return h.runtime.ExecuteWorkflow(h.wfCtx, h.request.Input)
	}
	return &api.RunOutput{}, nil
}

func (h *testChildHandle) IsReady() bool {
	// Test child handles complete synchronously when Get is invoked.
	// We return true so callers can treat them as ready when draining.
	return true
}
func (h *testChildHandle) Cancel(ctx context.Context) error { return nil }
func (h *testChildHandle) RunID() string                    { return "" }

func newAnyJSONSpec(name tools.Ident, toolset string) tools.ToolSpec {
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
		Toolset: toolset,
		Payload: tools.TypeSpec{Name: string(name) + "_payload", Codec: codec},
		Result:  tools.TypeSpec{Name: string(name + "_result"), Codec: codec},
	}
}
