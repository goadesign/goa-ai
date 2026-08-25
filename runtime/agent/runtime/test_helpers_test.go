//nolint:lll // allow long lines in test literals for readability
package runtime

// This file provides planner, workflow, tool, and event-log fixtures shared by
// focused runtime contract tests.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/policy"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/agent/tools"
)

// mustRuntimeToolInput compiles a static test schema.
func mustRuntimeToolInput(schema []byte) model.ToolInput {
	input, err := model.AdvertisedToolInputFromSchema(schema)
	if err != nil {
		panic(err)
	}
	return input
}

const testPublicationBatchID = "7b62faf2-1667-4f54-a807-46d151764717"

func testToolFailure(kind planner.FailureKind, action planner.RecoveryAction, message string) *planner.ToolFailure {
	var priorInput rawjson.Message
	if action == planner.RecoveryCorrectCall {
		priorInput = rawjson.Message(`{"invalid":true}`)
	}
	return &planner.ToolFailure{
		Kind:  kind,
		Error: planner.NewToolError(message),
		Recovery: planner.RecoveryDirective{
			Action:     action,
			PriorInput: priorInput,
		},
	}
}

func wrapExecute(fn func(context.Context, *ToolCall) (*planner.ToolResult, error)) func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
	return func(ctx context.Context, call *ToolCall) (*ToolExecutionResult, error) {
		result, err := fn(ctx, call)
		if err != nil {
			return nil, err
		}
		if result == nil {
			return nil, nil
		}
		return Executed(result), nil
	}
}

// runLoop seeds workflow state directly for focused runtime tests.
func (r *Runtime) runLoop(
	wfCtx engine.WorkflowContext,
	reg AgentRegistration,
	input *RunInput,
	base *planner.PlanInput,
	initialResult *PlanResult,
	caps policy.CapsState,
	budgetDeadline time.Time,
	hardDeadline time.Time,
	turnID string,
	_ any,
) (*RunOutput, error) {
	st := newRunLoopState(initialResult, nil, model.TokenUsage{}, caps, 2)
	return r.runLoopWithState(
		wfCtx,
		reg,
		input,
		base,
		st,
		budgetDeadline,
		hardDeadline,
		turnID,
		nil,
	)
}

func seedTestToolSpecs(rt *Runtime, specs ...tools.ToolSpec) {
	if rt.toolSpecs == nil {
		rt.toolSpecs = make(map[tools.Ident]tools.ToolSpec)
	}
	if rt.policyToolMetadata == nil {
		rt.policyToolMetadata = make(map[tools.Ident]policy.ToolMetadata)
	}
	for _, spec := range specs {
		rt.toolSpecs[spec.Name] = spec
		rt.policyToolMetadata[spec.Name] = canonicalToolMetadata(spec, nil)
	}
}

func testModelRequest(toolNames ...string) *model.Request {
	definitions := make([]*model.ToolDefinition, len(toolNames))
	for index, name := range toolNames {
		definitions[index] = &model.ToolDefinition{
			Name:  name,
			Input: mustRuntimeToolInput([]byte(`{"type":"object"}`)),
		}
	}
	return &model.Request{Tools: definitions}
}

func testModelResponse(content []model.Message, calls ...model.ToolCall) *model.Response {
	response := &model.Response{
		Content:    append([]model.Message(nil), content...),
		StopReason: "end_turn",
	}
	if len(calls) == 0 {
		return response
	}
	response.StopReason = "tool_use"
	if len(response.Content) == 0 {
		response.Content = append(response.Content, model.Message{Role: model.ConversationRoleAssistant})
	}
	message := &response.Content[len(response.Content)-1]
	for _, call := range calls {
		message.Parts = append(message.Parts, model.ToolUsePart{
			ID:               call.ID,
			Name:             string(call.Name),
			Input:            call.Payload,
			ThoughtSignature: call.ThoughtSignature,
		})
	}
	return response
}

// testModelResponseWithUsage builds a canonical response whose terminal usage
// agrees with the deltas emitted by its test stream.
func testModelResponseWithUsage(
	content []model.Message,
	usage model.TokenUsage,
	calls ...model.ToolCall,
) *model.Response {
	response := testModelResponse(content, calls...)
	response.Usage = usage
	return response
}

// testRecordBatch wraps records in the activity contract used by every durable
// publication, including singular lifecycle events.
func testRecordBatch(records ...*RecordActivityInput) *api.RecordActivityBatchInput {
	return &api.RecordActivityBatchInput{Records: records}
}

// testWorkflowContext is a lightweight engine.WorkflowContext implementation used by tests.
type testWorkflowContext struct {
	ctx context.Context
	now func() time.Time

	lastHookCall    engine.RecordActivityCall
	lastPlannerCall engine.PlannerActivityCall
	lastToolCall    engine.ToolActivityCall

	asyncResult  ToolOutput
	sequenceMu   sync.Mutex
	nextSequence uint64
	workflowID   string

	planResult      *PlanResult
	hasPlanResult   bool
	recoveryCatalog *RecoveryCatalog
	barrier         chan struct{}
	hookRuntime     *Runtime // optional runtime for record activity execution
	runtime         *Runtime // optional runtime for activity execution (plan/resume/execute)
	childRuntime    *Runtime // optional runtime for child workflow execution

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
	if t.workflowID != "" {
		return t.workflowID
	}
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
		workflowID:  t.workflowID,
		now:         t.now,

		planResult:      t.planResult,
		hasPlanResult:   t.hasPlanResult,
		recoveryCatalog: t.recoveryCatalog,
		barrier:         t.barrier,
		hookRuntime:     t.hookRuntime,
		runtime:         t.runtime,
		childRuntime:    t.childRuntime,

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
		workflowID:  t.workflowID,
		now:         t.now,

		planResult:      t.planResult,
		hasPlanResult:   t.hasPlanResult,
		recoveryCatalog: t.recoveryCatalog,
		barrier:         t.barrier,
		hookRuntime:     t.hookRuntime,
		runtime:         t.runtime,
		childRuntime:    t.childRuntime,

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
	if t.now != nil {
		return t.now()
	}
	return time.Unix(0, 0)
}

func (t *testWorkflowContext) NextSequence() uint64 {
	root := t.root()
	root.sequenceMu.Lock()
	defer root.sequenceMu.Unlock()
	root.nextSequence++
	return root.nextSequence
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

func (t *testWorkflowContext) Await(condition func() bool) error {
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
		case <-t.ctx.Done():
			return t.ctx.Err()
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
			out: &api.RunOutput{
				AgentID: req.Input.AgentID,
				RunID:   req.Input.RunID,
				Final: &model.Message{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "completed"}},
				},
			},
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

func (t *testWorkflowContext) PublishRecords(call engine.RecordActivityCall) error {
	t.lastHookCall = call
	hookRT := t.hookRuntime
	if hookRT == nil {
		hookRT = t.runtime
	}
	if hookRT == nil {
		return nil
	}
	if call.Name != recordActivityName {
		return fmt.Errorf("unexpected record activity name %q", call.Name)
	}
	return hookRT.recordActivity(t.Context(), call.Input)
}

func (t *testWorkflowContext) ExecutePlannerActivity(call engine.PlannerActivityCall) (*api.PlanActivityOutput, error) {
	t.lastPlannerCall = call
	switch call.Name {
	case "plan", "nested.plan":
		if t.runtime != nil {
			return t.runtime.PlanStartActivity(t.Context(), call.Input)
		}
	case "resume", "nested.resume":
		if t.runtime != nil {
			return t.runtime.PlanResumeActivity(t.Context(), call.Input)
		}
	}

	var result *PlanResult
	if t.hasPlanResult {
		result = t.planResult
	}
	return &PlanActivityOutput{
		PublicationBatchID: uuid.NewString(),
		Result:             result,
		Transcript:         nil,
		RecoveryCatalog:    t.recoveryCatalog,
	}, nil
}

func (t *testWorkflowContext) ExecuteToolActivity(call engine.ToolActivityCall) (*api.ToolOutput, error) {
	fut, err := t.ExecuteToolActivityAsync(call)
	if err != nil {
		return nil, err
	}
	return fut.Get(t.Context())
}

func (t *testWorkflowContext) ExecuteToolActivityAsync(call engine.ToolActivityCall) (engine.Future[*api.ToolOutput], error) {
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
			fut.result, fut.err = t.runtime.ExecuteToolActivity(t.Context(), call.Input)
			return fut, nil
		}
	}

	result := t.asyncResult
	fut.result = &result
	return fut, nil
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

	mu       sync.Mutex
	canceled bool
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

func (h *controlledChildHandle) Cancel(context.Context) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.canceled = true
	return nil
}

func (h *controlledChildHandle) RunID() string { return "" }

func (h *controlledChildHandle) wasCanceled() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.canceled
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

type stubEngine struct {
	last                             engine.WorkflowStartRequest
	registeredRecordActivityOptions  map[string]engine.ActivityOptions
	registeredPlannerActivityOptions map[string]engine.ActivityOptions
	registeredExecuteActivityOptions map[string]engine.ActivityOptions
	sealCalls                        int
	sealErrors                       []error
}

func (s *stubEngine) RegisterWorkflow(context.Context, engine.WorkflowDefinition) error { return nil }
func (s *stubEngine) RegisterRecordActivity(_ context.Context, name string, opts engine.ActivityOptions, _ func(context.Context, *api.RecordActivityBatchInput) error) error {
	if s.registeredRecordActivityOptions == nil {
		s.registeredRecordActivityOptions = make(map[string]engine.ActivityOptions)
	}
	s.registeredRecordActivityOptions[name] = opts
	return nil
}
func (s *stubEngine) RegisterPlannerActivity(_ context.Context, name string, opts engine.ActivityOptions, _ func(context.Context, *api.PlanActivityInput) (*api.PlanActivityOutput, error)) error {
	if s.registeredPlannerActivityOptions == nil {
		s.registeredPlannerActivityOptions = make(map[string]engine.ActivityOptions)
	}
	s.registeredPlannerActivityOptions[name] = opts
	return nil
}
func (s *stubEngine) RegisterExecuteToolActivity(_ context.Context, name string, opts engine.ActivityOptions, _ func(context.Context, *api.ToolInput) (*api.ToolOutput, error)) error {
	if s.registeredExecuteActivityOptions == nil {
		s.registeredExecuteActivityOptions = make(map[string]engine.ActivityOptions)
	}
	s.registeredExecuteActivityOptions[name] = opts
	return nil
}
func (s *stubEngine) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	s.last = req
	return noopWorkflowHandle{}, nil
}

func (s *stubEngine) QueryRunStatus(context.Context, string) (engine.RunStatus, error) {
	return engine.RunStatusCompleted, nil
}

func (s *stubEngine) QueryRunCompletion(context.Context, string) (*api.RunOutput, error) {
	return &api.RunOutput{}, nil
}

func (s *stubEngine) SealRegistration(context.Context) error {
	s.sealCalls++
	if len(s.sealErrors) == 0 {
		return nil
	}
	idx := s.sealCalls - 1
	if idx >= len(s.sealErrors) {
		idx = len(s.sealErrors) - 1
	}
	if s.sealErrors[idx] == nil {
		return nil
	}
	return s.sealErrors[idx]
}

type noopWorkflowHandle struct{}

func (noopWorkflowHandle) Wait(context.Context) (*api.RunOutput, error) { return &api.RunOutput{}, nil }
func (noopWorkflowHandle) Cancel(context.Context) error                 { return nil }

func newTestRuntimeWithPlanner(agentID agent.Ident, pl planner.Planner) *Runtime {
	return &Runtime{
		agents:        map[agent.Ident]AgentRegistration{agentID: {Planner: pl}},
		toolsets:      make(map[string]ToolsetRegistration),
		toolSpecs:     make(map[tools.Ident]tools.ToolSpec),
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		SessionStore:  sessioninmem.New(),
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

// countRunEventsByType returns the number of canonical events of one type in a
// run-log page so suspension tests can prove prompt ownership by run ID.
func countRunEventsByType(page runlog.Page, eventType runlog.Type) int {
	count := 0
	for _, event := range page.Events {
		if event.Type == eventType {
			count++
		}
	}
	return count
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
	return &api.RunOutput{
		AgentID: h.request.Input.AgentID,
		RunID:   h.request.Input.RunID,
		Final: &model.Message{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "completed"}},
		},
	}, nil
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
