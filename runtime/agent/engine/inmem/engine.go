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
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"time"

	commonpb "go.temporal.io/api/common/v1"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/engine/contract"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/internal/temporalerrors"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
)

type (
	// eng implements engine.Engine with an in-process goroutine runner.
	eng struct {
		mu sync.RWMutex

		workflows map[string]engine.WorkflowDefinition

		storageActivities      map[string]storageActivityDef
		plannerActivities      map[string]plannerActivityDef
		toolActivities         map[string]toolActivityDef
		agentChildActivities   map[string]agentChildActivityDef
		continuationActivities map[string]continuationActivityDef

		// statuses tracks workflow status by run ID (inmem uses workflow ID as run ID).
		statuses map[string]engine.RunStatus
		// handles retain terminal results so runtime repair can recover the exact
		// workflow output by ID after its original caller detaches.
		handles map[string]*handle
		// recipes bind each queryable workflow ID to the fixed-size digest of
		// the immutable start request accepted by this engine instance.
		recipes map[string][sha256.Size]byte
	}

	// wfCtx adapts context.Context into engine.WorkflowContext.
	wfCtx struct {
		ctx             context.Context
		id              string
		runID           string
		requestDigest   [32]byte
		eng             *eng
		seq             *sequenceCounter
		cancellations   *cancellationState
		control         *workflowControl
		startedChildren map[string]struct{}
	}

	// handle is the in-memory implementation of engine.WorkflowHandle.
	handle struct {
		mu          sync.Mutex
		done        chan struct{}
		err         error
		result      *api.RunOutput
		completedAt time.Time
		cancel      context.CancelFunc
		// cancellations belongs only to this workflow execution. Requests use it
		// to wait for workflow code without a process-wide command registry.
		cancellations *cancellationState
		// issuedControls records every workflow attempt created for this handle.
		// Earlier attempts stay recorded so delayed evidence can be accepted
		// without changing its original execution or assigning it to a retry.
		issuedControls map[*workflowControl]struct{}
		parent         *recoveryChild
	}

	// childHandle adapts an in-memory WorkflowHandle to engine.ChildWorkflowHandle.
	childHandle struct {
		h        engine.WorkflowHandle
		workflow *wfCtx
	}

	storageActivityDef struct {
		handler func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error)
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

	agentChildActivityDef struct {
		handler func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error)
		opts    engine.ActivityOptions
	}

	continuationActivityDef struct {
		handler func(context.Context, *api.ContinuationActivityInput) (bool, error)
		opts    engine.ActivityOptions
	}

	// activityTimeouts separates the limit for one attempt from the limit for
	// the complete activity, including retries and retry delays.
	activityTimeouts struct {
		startToClose    time.Duration
		scheduleToClose time.Duration
	}

	// future is a simple typed Future implementation backed by a channel.
	future[T any] struct {
		ready     chan struct{}
		result    T
		err       error
		once      sync.Once
		workflow  *wfCtx
		onFailure func(error)
	}
)

var (
	_ engine.Engine                = (*eng)(nil)
	_ engine.CancellationRequester = (*eng)(nil)
	_ engine.WorkflowHandle        = (*handle)(nil)
	_ engine.WorkflowContext       = (*wfCtx)(nil)
	_ engine.ChildWorkflowHandle   = (*childHandle)(nil)
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
		recipes:  make(map[string][sha256.Size]byte),
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

// RegisterStorageActivity registers the one activity that applies typed
// runtime storage commands outside deterministic workflow code.
func (e *eng) RegisterStorageActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error)) error {
	if name == "" {
		return errors.New("storage activity name is required")
	}
	if fn == nil {
		return errors.New("storage activity handler is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.storageActivities == nil {
		e.storageActivities = make(map[string]storageActivityDef)
	}
	if _, dup := e.storageActivities[name]; dup {
		return fmt.Errorf("storage activity %q already registered", name)
	}
	e.storageActivities[name] = storageActivityDef{
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

// RegisterAgentChildActivity registers the activity that prepares one child
// run outside workflow code.
func (e *eng) RegisterAgentChildActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error)) error {
	if name == "" {
		return errors.New("agent child activity name is required")
	}
	if fn == nil {
		return errors.New("agent child activity handler is required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.agentChildActivities == nil {
		e.agentChildActivities = make(map[string]agentChildActivityDef)
	}
	if _, dup := e.agentChildActivities[name]; dup {
		return fmt.Errorf("agent child activity %q already registered", name)
	}
	e.agentChildActivities[name] = agentChildActivityDef{
		handler: fn,
		opts:    opts,
	}
	return nil
}

// RegisterContinuationActivity registers a saved-page read outside workflow code.
func (e *eng) RegisterContinuationActivity(_ context.Context, name string, opts engine.ActivityOptions, fn func(context.Context, *api.ContinuationActivityInput) (bool, error)) error {
	if name == "" || fn == nil {
		return errors.New("continuation activity name and handler are required")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.continuationActivities == nil {
		e.continuationActivities = make(map[string]continuationActivityDef)
	}
	if _, exists := e.continuationActivities[name]; exists {
		return fmt.Errorf("continuation activity %q already registered", name)
	}
	e.continuationActivities[name] = continuationActivityDef{handler: fn, opts: opts}
	return nil
}

// StartWorkflow snapshots and starts one in-process workflow. The accepted
// workflow runs independently of the submission context and remains attached
// to its exact recipe digest for the lifetime of this engine.
func (e *eng) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	if err := engine.ValidateWorkflowStartRequest(req); err != nil {
		return nil, err
	}
	if _, reserved := req.Memo[startrecipe.MemoKey]; reserved {
		return nil, fmt.Errorf("in-memory engine: memo key %q is reserved", startrecipe.MemoKey)
	}
	snapshot, err := startrecipe.SnapshotRequest(req)
	if err != nil {
		return nil, err
	}
	req = snapshot.Request
	e.mu.RLock()
	def, ok := e.workflows[req.Workflow]
	e.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("workflow %q not registered", req.Workflow)
	}

	e.mu.Lock()
	if existingRecipe, exists := e.recipes[req.ID]; exists {
		if existingRecipe != snapshot.Digest {
			e.mu.Unlock()
			return nil, &engine.WorkflowStartConflictError{ID: req.ID}
		}
		handle := e.handles[req.ID]
		e.mu.Unlock()
		return handle, nil
	}
	if _, exists := e.handles[req.ID]; exists {
		e.mu.Unlock()
		return nil, &engine.WorkflowStartConflictError{ID: req.ID}
	}
	h, executionCtx := e.reserveWorkflowLocked(context.WithoutCancel(ctx), req.ID)
	e.recipes[req.ID] = snapshot.Digest
	e.mu.Unlock()

	e.executeWorkflow(executionCtx, req.ID, req.RunTimeout, req.RetryPolicy, def, snapshot.InputPayload, snapshot.Digest, h)
	return h, nil
}

// reserveWorkflowLocked records one accepted execution before its handler can
// run. The caller holds e.mu and has already established that the ID is free.
func (e *eng) reserveWorkflowLocked(executionCtx context.Context, id string) (*handle, context.Context) {
	executionCtx, cancel := context.WithCancel(executionCtx)
	cancellations := &cancellationState{cancel: cancel}
	h := &handle{
		done:           make(chan struct{}),
		cancel:         cancel,
		cancellations:  cancellations,
		issuedControls: make(map[*workflowControl]struct{}),
	}

	if e.statuses == nil {
		e.statuses = make(map[string]engine.RunStatus)
	}
	e.statuses[id] = engine.RunStatusRunning
	e.handles[id] = h
	return h, executionCtx
}

// executeWorkflow runs one accepted workflow from the copied input bytes. Root
// and child starts use the same retry and completion behavior after the engine
// accepts their requests.
func (e *eng) executeWorkflow(
	executionCtx context.Context,
	id string,
	runTimeout time.Duration,
	retryPolicy engine.RetryPolicy,
	def engine.WorkflowDefinition,
	inputPayload *commonpb.Payload,
	requestDigest [32]byte,
	h *handle,
) {
	go func() {
		defer h.cancel()
		defer close(h.done)
		res, err := e.runWorkflow(executionCtx, id, runTimeout, retryPolicy, def, inputPayload, requestDigest, h)
		h.cancellations.finish()
		h.mu.Lock()
		h.result = res
		h.err = err
		h.completedAt = time.Now().UTC()
		h.mu.Unlock()
		// Update status based on completion.
		e.mu.Lock()
		switch {
		case err == nil:
			e.statuses[id] = engine.RunStatusCompleted
		case errors.Is(err, context.Canceled):
			e.statuses[id] = engine.RunStatusCanceled
		case errors.Is(err, context.DeadlineExceeded):
			e.statuses[id] = engine.RunStatusTimedOut
		default:
			e.statuses[id] = engine.RunStatusFailed
		}
		e.mu.Unlock()
	}()
}

// runWorkflow executes fresh workflow attempts from the accepted input bytes.
// A run timeout applies to each attempt, matching Temporal's WorkflowRunTimeout.
func (e *eng) runWorkflow(
	executionCtx context.Context,
	id string,
	runTimeout time.Duration,
	retryPolicy engine.RetryPolicy,
	def engine.WorkflowDefinition,
	inputPayload *commonpb.Payload,
	requestDigest [32]byte,
	h *handle,
) (*api.RunOutput, error) {
	cancellations := h.cancellations
	dataConverter := workflowcodec.NewDataConverter()
	delay := retryPolicy.InitialInterval
	if delay == 0 {
		delay = time.Second
	}
	coefficient := retryPolicy.BackoffCoefficient
	if coefficient == 0 {
		coefficient = 2
	}
	for attempt := 1; ; attempt++ {
		var input *api.RunInput
		if err := dataConverter.FromPayload(inputPayload, &input); err != nil {
			return nil, fmt.Errorf("decode workflow retry input: %w", err)
		}
		attemptCtx, cancel := context.WithCancel(executionCtx)
		if runTimeout > 0 {
			attemptCtx, cancel = context.WithTimeout(executionCtx, runTimeout)
		}
		wctx := &wfCtx{
			ctx: attemptCtx,
			id:  id,
			// In-memory assigns workflow ID as run ID.
			runID:           id,
			requestDigest:   requestDigest,
			eng:             e,
			seq:             &sequenceCounter{},
			cancellations:   cancellations,
			startedChildren: make(map[string]struct{}),
		}
		control := wctx.executionControl()
		control.parent = h.parent
		if h.parent != nil {
			control.inherited = h.parent.inherited
		}
		h.mu.Lock()
		h.issuedControls[control] = struct{}{}
		h.mu.Unlock()
		cancellations.startAttempt(wctx)
		result, err := def.Handler(wctx, input)
		err = wctx.finish(err)
		if err == nil && attemptCtx.Err() != nil {
			err = attemptCtx.Err()
		}
		cancel()
		if err == nil {
			copied, copyErr := contract.CopyRunOutput(result)
			if copyErr != nil {
				return nil, copyErr
			}
			return copied, nil
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, engine.ErrWorkflowCompleted) || errors.Is(err, engine.ErrWorkflowStartConflict) ||
			temporalerrors.IsRequestValidation(err) || !workflowRetryAllowed(retryPolicy, attempt) {
			return result, err
		}
		cancellations.endAttempt()
		select {
		case <-executionCtx.Done():
			return nil, executionCtx.Err()
		case <-time.After(delay):
		}
		delay = time.Duration(float64(delay) * coefficient)
	}
}

// workflowRetryAllowed reports whether another failed execution attempt is
// permitted by the submitted workflow retry policy.
func workflowRetryAllowed(policy engine.RetryPolicy, completedAttempts int) bool {
	return policy.UnlimitedAttempts || policy.MaxAttempts > completedAttempts
}

// QueryRunCompletion returns the current workflow status and the exact result
// once the in-process workflow handler has finished. Open workflows return
// immediately without an output or workflow error.
func (e *eng) QueryRunCompletion(ctx context.Context, workflowID string) (engine.RunCompletion, error) {
	if workflowID == "" {
		return engine.RunCompletion{}, errors.New("workflow id is required")
	}
	e.mu.RLock()
	h, ok := e.handles[workflowID]
	status := e.statuses[workflowID]
	e.mu.RUnlock()
	if !ok {
		return engine.RunCompletion{}, engine.ErrWorkflowNotFound
	}
	if !isTerminalRunStatus(status) {
		return engine.RunCompletion{Status: status}, nil
	}
	select {
	case <-ctx.Done():
		return engine.RunCompletion{}, ctx.Err()
	case <-h.done:
	}
	h.mu.Lock()
	result := h.result
	workflowErr := h.err
	completedAt := h.completedAt
	h.mu.Unlock()
	var output *api.RunOutput
	if workflowErr == nil {
		var err error
		output, err = copyRetainedWorkflowOutput(result)
		if err != nil {
			return engine.RunCompletion{}, err
		}
	}
	return engine.RunCompletion{
		Status: status, CompletedAt: completedAt, Output: output, WorkflowError: workflowErr,
	}, nil
}

// copyRetainedWorkflowOutput reconstructs the saved output so callers cannot
// change the value returned to another workflow handle or completion query.
func copyRetainedWorkflowOutput(result *api.RunOutput) (*api.RunOutput, error) {
	if result == nil {
		return nil, nil
	}
	output, err := contract.CopyRunOutput(result)
	if err != nil {
		return nil, err
	}
	return output, nil
}

// isTerminalRunStatus reports whether an in-memory workflow has a final result.
func isTerminalRunStatus(status engine.RunStatus) bool {
	switch status {
	case engine.RunStatusCompleted, engine.RunStatusTimedOut,
		engine.RunStatusFailed, engine.RunStatusCanceled:
		return true
	case engine.RunStatusPending, engine.RunStatusRunning, engine.RunStatusPaused:
		return false
	default:
		panic("in-memory engine: unsupported run status: " + string(status))
	}
}

// RequestCancellation waits for workflow code to record the reason, then
// cancels the engine-owned execution context.
func (e *eng) RequestCancellation(ctx context.Context, request engine.CancellationRequest) error {
	if request.RunID == "" {
		return errors.New("run id is required")
	}
	if request.Reason == "" {
		return errors.New("cancellation reason is required")
	}
	e.mu.RLock()
	handle, ok := e.handles[request.RunID]
	e.mu.RUnlock()
	if !ok {
		return engine.ErrWorkflowNotFound
	}
	return handle.cancellations.request(ctx, request)
}

func (h *handle) Wait(ctx context.Context) (*api.RunOutput, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.done:
		h.mu.Lock()
		result := h.result
		workflowErr := h.err
		h.mu.Unlock()
		if workflowErr != nil {
			return nil, workflowErr
		}
		output, err := copyRetainedWorkflowOutput(result)
		if err != nil {
			return nil, err
		}
		return output, nil
	}
}

// Cancel stops the workflow through its engine-owned execution context.
func (h *handle) Cancel(_ context.Context) error {
	h.cancel()
	return nil
}

func (c *childHandle) Get(ctx context.Context) (*api.RunOutput, error) {
	if c.workflow != nil {
		if err := c.workflow.wait(ctx, c.IsReady, nil); err != nil {
			return nil, err
		}
	}
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

func (w *wfCtx) Context() context.Context {
	return context.WithValue(engine.WithWorkflowContext(w.ctx, w), workflowScopeKey{}, w)
}

// SetQueryHandler is a no-op for the in-memory engine.
func (w *wfCtx) SetQueryHandler(name string, handler any) error {
	return nil
}

// SetCancellationHandler registers the function that records cancellation for
// this workflow. Commands that arrived first are processed before it returns.
func (w *wfCtx) SetCancellationHandler(handler engine.CancellationHandler) error {
	return w.cancellations.setHandler(handler)
}

func (w *wfCtx) WorkflowID() string {
	return w.id
}

func (w *wfCtx) RunID() string {
	return w.runID
}

// StartRequestDigest returns the snapshot retained when the engine accepted this execution.
func (w *wfCtx) StartRequestDigest() ([32]byte, error) {
	if w.requestDigest == ([32]byte{}) {
		return [32]byte{}, errors.New("workflow accepted request digest is missing")
	}
	return w.requestDigest, nil
}

func (w *wfCtx) StartChildWorkflow(_ context.Context, req engine.ChildWorkflowRequest) (engine.ChildWorkflowHandle, error) {
	if err := engine.ValidateChildWorkflowRequest(req); err != nil {
		return nil, err
	}
	snapshot, err := startrecipe.SnapshotChildRequest(req)
	if err != nil {
		return nil, err
	}
	req = snapshot.Request
	if _, exists := w.startedChildren[req.ID]; exists {
		return nil, &engine.ChildWorkflowIDReuseError{ID: req.ID}
	}
	w.startedChildren[req.ID] = struct{}{}

	w.eng.mu.RLock()
	def, ok := w.eng.workflows[req.Workflow]
	w.eng.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("workflow %q not registered", req.Workflow)
	}

	w.eng.mu.Lock()
	if _, exists := w.eng.handles[req.ID]; exists {
		w.eng.mu.Unlock()
		return nil, &engine.ChildWorkflowIDReuseError{ID: req.ID}
	}
	h, executionCtx := w.eng.reserveWorkflowLocked(w.ctx, req.ID)
	w.eng.mu.Unlock()

	control := w.executionControl()
	binding := &recoveryChild{parent: control, handle: h, id: req.ID, inherited: control.inherited || control.accept != nil, accepted: make(chan struct{})}
	h.parent = binding
	w.eng.executeWorkflow(executionCtx, req.ID, req.RunTimeout, req.RetryPolicy, def, snapshot.InputPayload, snapshot.Digest, h)
	control.children[req.ID] = binding
	close(binding.accepted)
	return &childHandle{h: h, workflow: w}, nil
}

func (w *wfCtx) Detached() engine.WorkflowContext {
	w.executionControl()
	cctx := context.WithoutCancel(w.ctx)
	sub := *w
	sub.ctx = cctx
	return &sub
}

func (w *wfCtx) WithCancel() (engine.WorkflowContext, func()) {
	w.executionControl()
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
		fut := &future[time.Time]{ready: make(chan struct{}), workflow: w, result: now}
		close(fut.ready)
		return fut, nil
	}
	fireAt := now.Add(d)
	fut := &future[time.Time]{ready: make(chan struct{}), workflow: w}
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
	return w.wait(w.ctx, condition, nil)
}

func (w *wfCtx) ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	return executeRegisteredRecordedActivity(w, call.Name, call.Command, call.Options, func() (engine.ActivityOptions, func(context.Context, *api.StorageActivityCommand) (*api.StorageActivityResult, error), bool) {
		def, ok := w.eng.storageActivities[call.Name]
		return def.opts, def.handler, ok
	})
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
	timeouts := resolveActivityTimeouts(call.Options, def.opts)
	scheduleCtx, cancel, scheduleDeadline := withOptionalTimeout(w.ctx, timeouts.scheduleToClose)
	defer cancel()
	retry := mergedRetryPolicy(def.opts.RetryPolicy, call.Options.RetryPolicy)
	out, err := runBlockingActivity(w, func() (*api.PlanActivityOutput, error) {
		return executeRecordedActivity(scheduleCtx, timeouts.startToClose, retry, call.Input, def.handler)
	})
	if scheduleDeadline && errors.Is(scheduleCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("%w: %w", engine.ErrPlannerActivityDeadlineExceeded, scheduleCtx.Err())
	}
	if errors.Is(scheduleCtx.Err(), context.DeadlineExceeded) {
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

	fut := &future[*api.ToolOutput]{ready: make(chan struct{}), workflow: w}
	go func() {
		defer close(fut.ready)
		timeouts := resolveActivityTimeouts(call.Options, def.opts)
		scheduleCtx, cancel, _ := withOptionalTimeout(w.ctx, timeouts.scheduleToClose)
		defer cancel()
		retry := mergedRetryPolicy(def.opts.RetryPolicy, call.Options.RetryPolicy)
		fut.result, fut.err = executeActivityAttempts(
			scheduleCtx,
			timeouts.startToClose,
			retry,
			call.Input,
			def.handler,
		)
		if errors.Is(scheduleCtx.Err(), context.DeadlineExceeded) {
			fut.result = nil
			fut.err = context.DeadlineExceeded
		}
	}()
	return fut, nil
}

// ExecuteAgentChildActivity runs child preparation outside the workflow
// function and returns its recorded result.
func (w *wfCtx) ExecuteAgentChildActivity(call engine.AgentChildActivityCall) (*api.AgentChildActivityOutput, error) {
	return executeRegisteredRecordedActivity(w, call.Name, call.Input, call.Options, func() (engine.ActivityOptions, func(context.Context, *api.AgentChildActivityInput) (*api.AgentChildActivityOutput, error), bool) {
		def, ok := w.eng.agentChildActivities[call.Name]
		return def.opts, def.handler, ok
	})
}

// ExecuteContinuationActivity copies the request and result through the same
// activity transport as other recorded reads. A failed read returns its error.
func (w *wfCtx) ExecuteContinuationActivity(call engine.ContinuationActivityCall) (bool, error) {
	output, err := executeRegisteredRecordedActivity(w, call.Name, call.Input, call.Options, func() (engine.ActivityOptions, func(context.Context, *api.ContinuationActivityInput) (*bool, error), bool) {
		def, ok := w.eng.continuationActivities[call.Name]
		return def.opts, func(ctx context.Context, input *api.ContinuationActivityInput) (*bool, error) {
			available, err := def.handler(ctx, input)
			if err != nil {
				return nil, err
			}
			return &available, nil
		}, ok
	})
	if err != nil {
		return false, err
	}
	if output == nil {
		return false, errors.New("continuation activity returned no answer")
	}
	return *output, nil
}

// executeRegisteredRecordedActivity resolves one registered activity and runs
// it with the serialization, timeout, and retry behavior shared by Temporal.
func executeRegisteredRecordedActivity[I, O any](w *wfCtx, name string, input *I, options engine.ActivityOptions, lookup func() (engine.ActivityOptions, func(context.Context, *I) (*O, error), bool)) (*O, error) {
	if name == "" {
		return nil, errors.New("recorded activity name is required")
	}
	if input == nil {
		return nil, errors.New("recorded activity input is required")
	}
	w.eng.mu.RLock()
	defaults, handler, ok := lookup()
	w.eng.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("recorded activity %q not registered", name)
	}
	timeouts := resolveActivityTimeouts(options, defaults)
	scheduleCtx, cancel, _ := withOptionalTimeout(w.ctx, timeouts.scheduleToClose)
	defer cancel()
	retry := mergedRetryPolicy(defaults.RetryPolicy, options.RetryPolicy)
	return runBlockingActivity(w, func() (*O, error) {
		return executeRecordedActivity(scheduleCtx, timeouts.startToClose, retry, input, handler)
	})
}

// executeRecordedActivity serializes activity values at the same boundary as
// Temporal. Every retry decodes fresh input, and callers receive a copy of the
// successful output rather than a handler-owned value.
func executeRecordedActivity[I, O any](ctx context.Context, startToClose time.Duration, retry engine.RetryPolicy, input *I, execute func(context.Context, *I) (*O, error)) (*O, error) {
	out, err := executeActivityAttempts(ctx, startToClose, retry, input, execute)
	if err != nil {
		return nil, err
	}
	dataConverter := workflowcodec.NewDataConverter()
	output, err := workflowcodec.Copy(dataConverter, out)
	if err != nil {
		return nil, fmt.Errorf("copy recorded activity output: %w", err)
	}
	return output, nil
}

// executeActivityAttempts snapshots one input and decodes a fresh value for
// each attempt. The caller decides whether its successful output also crosses
// the recorded-value boundary.
func executeActivityAttempts[I, O any](ctx context.Context, startToClose time.Duration, retry engine.RetryPolicy, input *I, execute func(context.Context, *I) (*O, error)) (*O, error) {
	dataConverter := workflowcodec.NewDataConverter()
	recordedInput, err := workflowcodec.Encode(dataConverter, input)
	if err != nil {
		return nil, fmt.Errorf("encode recorded activity input: %w", err)
	}
	out, err := executeActivityWithRetry(ctx, startToClose, retry, func(attemptCtx context.Context) (*O, error) {
		attemptInput, err := recordedInput.Decode()
		if err != nil {
			return nil, engine.MarkActivityErrorNonRetryable(
				fmt.Errorf("decode recorded activity input: %w", err),
			)
		}
		return execute(attemptCtx, attemptInput)
	})
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return nil, context.DeadlineExceeded
	}
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (f *future[T]) Get(ctx context.Context) (T, error) {
	if f.workflow != nil {
		if err := f.workflow.wait(ctx, f.IsReady, f.ready); err != nil {
			var zero T
			return zero, err
		}
		return f.result, f.err
	}
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

// resolveActivityTimeouts keeps the total activity lifetime separate from the
// timeout applied freshly to each attempt.
func resolveActivityTimeouts(override, defaults engine.ActivityOptions) activityTimeouts {
	startToClose := override.StartToCloseTimeout
	if startToClose == 0 {
		startToClose = defaults.StartToCloseTimeout
	}
	scheduleToClose := override.ScheduleToCloseTimeout
	if scheduleToClose == 0 {
		scheduleToClose = defaults.ScheduleToCloseTimeout
	}
	return activityTimeouts{
		startToClose:    startToClose,
		scheduleToClose: scheduleToClose,
	}
}

// mergedRetryPolicy applies an explicit unlimited override while inheriting
// unspecified delay and backoff values from the registered activity.
func mergedRetryPolicy(defaults, override engine.RetryPolicy) engine.RetryPolicy {
	result := defaults
	if override.UnlimitedAttempts {
		result.MaxAttempts = 0
		result.UnlimitedAttempts = true
	} else if override.MaxAttempts != 0 {
		result.MaxAttempts = override.MaxAttempts
		result.UnlimitedAttempts = false
	}
	if override.InitialInterval != 0 {
		result.InitialInterval = override.InitialInterval
	}
	if override.BackoffCoefficient != 0 {
		result.BackoffCoefficient = override.BackoffCoefficient
	}
	return result
}

// executeActivityWithRetry applies the same attempt count, delay, and permanent
// error rules to every retried in-memory activity. Each attempt receives a new
// start-to-close timeout while ctx bounds the complete retry sequence.
func executeActivityWithRetry[T any](ctx context.Context, startToClose time.Duration, retry engine.RetryPolicy, execute func(context.Context) (T, error)) (T, error) {
	delay := retry.InitialInterval
	if delay == 0 {
		delay = time.Millisecond
	}
	coefficient := max(1, retry.BackoffCoefficient)
	for attempt := 1; ; attempt++ {
		attemptCtx, cancel, _ := withOptionalTimeout(ctx, startToClose)
		output, err := execute(attemptCtx)
		if errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
			err = context.DeadlineExceeded
		}
		cancel()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			var zero T
			return zero, context.DeadlineExceeded
		}
		if err == nil || engine.IsActivityErrorNonRetryable(err) || temporalerrors.IsRequestValidation(err) {
			return output, err
		}
		if !retry.UnlimitedAttempts && (retry.MaxAttempts == 0 || attempt >= retry.MaxAttempts) {
			var zero T
			return zero, err
		}
		select {
		case <-ctx.Done():
			var zero T
			return zero, ctx.Err()
		case <-time.After(delay):
		}
		delay = time.Duration(float64(delay) * coefficient)
	}
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
