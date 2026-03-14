package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	sessioninmem "goa.design/goa-ai/runtime/agent/session/inmem"
	"goa.design/goa-ai/runtime/agent/telemetry"
)

type controlledWaitHandle struct {
	ready       chan struct{}
	waitStarted chan struct{}
	out         *api.RunOutput
	err         error
}

func (h *controlledWaitHandle) Wait(ctx context.Context) (*api.RunOutput, error) {
	if h.waitStarted != nil {
		select {
		case <-h.waitStarted:
		default:
			close(h.waitStarted)
		}
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-h.ready:
		return h.out, h.err
	}
}

func (h *controlledWaitHandle) Signal(context.Context, string, any) error {
	return nil
}

func (h *controlledWaitHandle) Cancel(context.Context) error {
	return nil
}

type controlledWaitEngine struct {
	stubEngine
	handle         *controlledWaitHandle
	reportedStatus engine.RunStatus
	queryErr       error
	completionOut  *api.RunOutput
	completionErr  error
}

type signalerWaitEngine struct {
	controlledWaitEngine
}

func (e *signalerWaitEngine) SignalByID(context.Context, string, string, string, any) error {
	return nil
}

type repairRaceRunlog struct {
	mu      sync.Mutex
	events  []*runlog.Event
	waiters int
	barrier chan struct{}
}

func newRepairRaceRunlog() *repairRaceRunlog {
	return &repairRaceRunlog{
		barrier: make(chan struct{}),
	}
}

func (r *repairRaceRunlog) Append(_ context.Context, e *runlog.Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *repairRaceRunlog) List(ctx context.Context, _ string, _ string, limit int) (runlog.Page, error) {
	r.mu.Lock()
	if !r.hasTerminalEventLocked() && r.waiters < 2 {
		r.waiters++
		barrier := r.barrier
		if r.waiters == 2 {
			close(r.barrier)
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return runlog.Page{}, ctx.Err()
		case <-barrier:
		}
		r.mu.Lock()
	}
	events := append([]*runlog.Event(nil), r.events...)
	r.mu.Unlock()
	if len(events) > limit {
		events = events[:limit]
	}
	return runlog.Page{Events: events}, nil
}

func (r *repairRaceRunlog) hasTerminalEventLocked() bool {
	for _, evt := range r.events {
		if evt.Type == hooks.RunCompleted {
			return true
		}
	}
	return false
}

type waitLazyRepairRaceRunlog struct {
	mu                 sync.Mutex
	events             []*runlog.Event
	initialWaiters     int
	initialBarrier     chan struct{}
	firstAppendRelease chan struct{}
}

func newWaitLazyRepairRaceRunlog() *waitLazyRepairRaceRunlog {
	return &waitLazyRepairRaceRunlog{
		initialBarrier:     make(chan struct{}),
		firstAppendRelease: make(chan struct{}),
	}
}

func (r *waitLazyRepairRaceRunlog) Append(ctx context.Context, e *runlog.Event) error {
	if e.Type == hooks.RunCompleted {
		r.mu.Lock()
		release := r.firstAppendRelease
		shouldBlock := release != nil && !r.hasTerminalEventLocked()
		r.mu.Unlock()
		if shouldBlock {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
			}
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
	return nil
}

func (r *waitLazyRepairRaceRunlog) List(ctx context.Context, _ string, _ string, limit int) (runlog.Page, error) {
	r.mu.Lock()
	if !r.hasTerminalEventLocked() && r.initialWaiters < 2 {
		r.initialWaiters++
		barrier := r.initialBarrier
		if r.initialWaiters == 2 {
			close(r.initialBarrier)
		}
		r.mu.Unlock()
		select {
		case <-ctx.Done():
			return runlog.Page{}, ctx.Err()
		case <-barrier:
		}
		r.mu.Lock()
	} else if !r.hasTerminalEventLocked() && r.initialWaiters >= 2 && r.firstAppendRelease != nil {
		close(r.firstAppendRelease)
		r.firstAppendRelease = nil
	}
	events := append([]*runlog.Event(nil), r.events...)
	r.mu.Unlock()
	if len(events) > limit {
		events = events[:limit]
	}
	return runlog.Page{Events: events}, nil
}

func (r *waitLazyRepairRaceRunlog) hasTerminalEventLocked() bool {
	for _, evt := range r.events {
		if evt.Type == hooks.RunCompleted {
			return true
		}
	}
	return false
}

func (e *controlledWaitEngine) StartWorkflow(ctx context.Context, req engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	e.last = req
	return e.handle, nil
}

func (e *controlledWaitEngine) QueryRunStatus(context.Context, string) (engine.RunStatus, error) {
	if e.queryErr != nil {
		return "", e.queryErr
	}
	if e.reportedStatus != "" {
		return e.reportedStatus, nil
	}
	select {
	case <-e.handle.ready:
		return engine.RunStatusCompleted, nil
	default:
		return engine.RunStatusRunning, nil
	}
}

func (e *controlledWaitEngine) QueryRunCompletion(context.Context, string) (*api.RunOutput, error) {
	if e.completionOut != nil || e.completionErr != nil {
		return e.completionOut, e.completionErr
	}
	switch e.reportedStatus {
	case engine.RunStatusCompleted:
		return e.handle.out, nil
	case engine.RunStatusTimedOut:
		return nil, context.DeadlineExceeded
	case engine.RunStatusFailed:
		return nil, errors.New("workflow failed before runtime emitted RunCompleted")
	case engine.RunStatusCanceled:
		return nil, context.Canceled
	default:
		return e.handle.out, e.handle.err
	}
}

func newObservedHandleTestRuntime(handle *controlledWaitHandle) *Runtime {
	return &Runtime{
		Engine:        &controlledWaitEngine{handle: handle},
		RunEventStore: runloginmem.New(),
		SessionStore:  sessioninmem.New(),
		Bus:           noopHooks{},
		logger:        telemetry.NoopLogger{},
		metrics:       telemetry.NoopMetrics{},
		tracer:        telemetry.NoopTracer{},
		runHandles:    make(map[string]engine.WorkflowHandle),
		agents: map[agent.Ident]AgentRegistration{
			"service.agent": {
				ID: "service.agent",
				Workflow: engine.WorkflowDefinition{
					Name:      "service.workflow",
					TaskQueue: "svc.queue",
				},
			},
		},
	}
}

func TestStartRunSynthesizesTerminalCompletionWhenWorkflowClosesWithoutHook(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready: make(chan struct{}),
		err:   context.DeadlineExceeded,
	}
	rt := newObservedHandleTestRuntime(handle)
	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	wfHandle, err := rt.MustClient(agent.Ident("service.agent")).Start(
		ctx,
		"sess-1",
		nil,
		WithRunID("run-1"),
		WithTurnID("turn-1"),
	)
	require.NoError(t, err)

	close(handle.ready)

	_, err = wfHandle.Wait(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	snapshot, err := rt.GetRunSnapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusFailed, snapshot.Status)
	require.Equal(t, run.PhaseFailed, snapshot.Phase)

	meta, err := rt.SessionStore.LoadRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusFailed, meta.Status)
}

func TestStartRunDoesNotWaitForCompletionUntilObserved(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready:       make(chan struct{}),
		waitStarted: make(chan struct{}),
	}
	rt := newObservedHandleTestRuntime(handle)
	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	_, err = rt.MustClient(agent.Ident("service.agent")).Start(
		ctx,
		"sess-1",
		nil,
		WithRunID("run-1"),
		WithTurnID("turn-1"),
	)
	require.NoError(t, err)

	select {
	case <-handle.waitStarted:
		t.Fatal("start should not begin waiting for workflow completion eagerly")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGetRunSnapshotRepairsTerminalCompletionOnDemand(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready:       make(chan struct{}),
		waitStarted: make(chan struct{}),
		err:         context.DeadlineExceeded,
	}
	rt := newObservedHandleTestRuntime(handle)
	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	_, err = rt.MustClient(agent.Ident("service.agent")).Start(
		ctx,
		"sess-1",
		nil,
		WithRunID("run-1"),
		WithTurnID("turn-1"),
	)
	require.NoError(t, err)

	select {
	case <-handle.waitStarted:
		t.Fatal("start should not begin waiting for workflow completion eagerly")
	case <-time.After(100 * time.Millisecond):
	}

	close(handle.ready)

	snapshot, err := rt.GetRunSnapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusFailed, snapshot.Status)
	require.Equal(t, run.PhaseFailed, snapshot.Phase)

	select {
	case <-handle.waitStarted:
	default:
		t.Fatal("snapshot repair should wait for terminal workflow completion")
	}

	meta, err := rt.SessionStore.LoadRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusFailed, meta.Status)
}

func TestStartRunLeavesTerminalRepairLazyWithoutObserver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready: make(chan struct{}),
		err:   context.DeadlineExceeded,
	}
	rt := newObservedHandleTestRuntime(handle)
	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	_, err = rt.MustClient(agent.Ident("service.agent")).Start(
		ctx,
		"sess-1",
		nil,
		WithRunID("run-1"),
		WithTurnID("turn-1"),
	)
	require.NoError(t, err)

	close(handle.ready)

	time.Sleep(50 * time.Millisecond)
	meta, err := rt.SessionStore.LoadRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusPending, meta.Status)
}

func TestOneShotRunLeavesTerminalRepairLazyWithoutObserver(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready: make(chan struct{}),
		err:   context.DeadlineExceeded,
	}
	rt := newObservedHandleTestRuntime(handle)

	_, err := rt.startOneShotRun(ctx, &RunInput{
		AgentID: "service.agent",
		RunID:   "run-1",
		TurnID:  "turn-1",
	})
	require.NoError(t, err)

	close(handle.ready)

	time.Sleep(50 * time.Millisecond)
	page, err := rt.RunEventStore.List(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.Empty(t, page.Events)
}

func TestStartRunSkipsSynthesizedCompletionWhenRunAlreadyTerminal(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready: make(chan struct{}),
		err:   errors.New("workflow execution already completed"),
	}
	rt := newObservedHandleTestRuntime(handle)
	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	wfHandle, err := rt.MustClient(agent.Ident("service.agent")).Start(
		ctx,
		"sess-1",
		nil,
		WithRunID("run-1"),
		WithTurnID("turn-1"),
	)
	require.NoError(t, err)
	require.NoError(t, rt.publishHookErr(
		ctx,
		hooks.NewRunCompletedEvent("run-1", "service.agent", "sess-1", runStatusSuccess, run.PhaseCompleted, nil),
		"turn-1",
	))

	close(handle.ready)

	_, err = wfHandle.Wait(ctx)
	require.Error(t, err)

	page, err := rt.ListRunEvents(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, hooks.RunCompleted, page.Events[0].Type)
}

func TestGetRunSnapshotRepairsTerminalCompletionWithoutObservedHandle(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.reportedStatus = engine.RunStatusFailed

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	snapshot, err := rt.GetRunSnapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusFailed, snapshot.Status)
	require.Equal(t, run.PhaseFailed, snapshot.Phase)

	meta, err := rt.SessionStore.LoadRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusFailed, meta.Status)

	page, err := rt.ListRunEvents(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Events)
	require.Equal(t, hooks.RunCompleted, page.Events[len(page.Events)-1].Type)
}

func TestGetRunSnapshotRepairsTimedOutRunWithTimeoutPublicError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.reportedStatus = engine.RunStatusTimedOut

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	snapshot, err := rt.GetRunSnapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusFailed, snapshot.Status)

	page, err := rt.ListRunEvents(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Events)

	last := page.Events[len(page.Events)-1]
	decoded, err := hooks.DecodeFromHookInput(&hooks.ActivityInput{
		Type:      last.Type,
		RunID:     last.RunID,
		AgentID:   last.AgentID,
		SessionID: last.SessionID,
		TurnID:    last.TurnID,
		Payload:   last.Payload,
	})
	require.NoError(t, err)

	completed, ok := decoded.(*hooks.RunCompletedEvent)
	require.True(t, ok)
	require.Equal(t, hooks.PublicErrorTimeout, completed.PublicError)
	require.Equal(t, hooks.ErrorKindTimeout, completed.ErrorKind)
}

func TestGetRunSnapshotRepairsFailedRunWithQueriedProviderError(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.reportedStatus = engine.RunStatusFailed
	eng.completionErr = model.NewProviderError(
		"bedrock",
		"converse_stream",
		429,
		model.ProviderErrorKindRateLimited,
		"ThrottlingException",
		"too many requests",
		"req-1",
		true,
		errors.New("throttled"),
	)

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	snapshot, err := rt.GetRunSnapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusFailed, snapshot.Status)

	page, err := rt.ListRunEvents(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Events)

	last := page.Events[len(page.Events)-1]
	decoded, err := hooks.DecodeFromHookInput(&hooks.ActivityInput{
		Type:      last.Type,
		RunID:     last.RunID,
		AgentID:   last.AgentID,
		SessionID: last.SessionID,
		TurnID:    last.TurnID,
		Payload:   last.Payload,
	})
	require.NoError(t, err)

	completed, ok := decoded.(*hooks.RunCompletedEvent)
	require.True(t, ok)
	require.Equal(t, hooks.PublicErrorProviderRateLimited, completed.PublicError)
	require.Equal(t, string(model.ProviderErrorKindRateLimited), completed.ErrorKind)
	require.Equal(t, "bedrock", completed.ErrorProvider)
	require.Equal(t, 429, completed.HTTPStatus)
	require.True(t, completed.Retryable)
}

func TestGetRunSnapshotRepairsTerminalCompletionOnceAcrossConcurrentReaders(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.reportedStatus = engine.RunStatusFailed
	rt.RunEventStore = newRepairRaceRunlog()

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	var wait sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, err := rt.GetRunSnapshot(ctx, "run-1")
			errs <- err
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	page, err := rt.ListRunEvents(ctx, "run-1", "", 10)
	require.NoError(t, err)

	completed := 0
	for _, evt := range page.Events {
		if evt.Type == hooks.RunCompleted {
			completed++
		}
	}
	require.Equal(t, 1, completed)
}

func TestWaitAndLazyRepairPublishSingleTerminalCompletionForSignalerEngines(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready: make(chan struct{}),
		err:   context.DeadlineExceeded,
	}
	rt := newObservedHandleTestRuntime(handle)
	rt.Engine = &signalerWaitEngine{
		controlledWaitEngine: controlledWaitEngine{
			handle:         handle,
			reportedStatus: engine.RunStatusFailed,
		},
	}
	rt.RunEventStore = newWaitLazyRepairRaceRunlog()

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	wfHandle, err := rt.MustClient(agent.Ident("service.agent")).Start(
		ctx,
		"sess-1",
		nil,
		WithRunID("run-1"),
		WithTurnID("turn-1"),
	)
	require.NoError(t, err)

	var (
		waitErr     error
		snapshot    *run.Snapshot
		snapshotErr error
	)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, waitErr = wfHandle.Wait(ctx)
	}()
	go func() {
		defer wg.Done()
		snapshot, snapshotErr = rt.GetRunSnapshot(ctx, "run-1")
	}()

	close(handle.ready)
	wg.Wait()

	require.ErrorIs(t, waitErr, context.DeadlineExceeded)
	require.NoError(t, snapshotErr)
	require.Equal(t, run.StatusFailed, snapshot.Status)

	page, err := rt.ListRunEvents(ctx, "run-1", "", 10)
	require.NoError(t, err)

	completed := 0
	var completedEvent *hooks.RunCompletedEvent
	for _, evt := range page.Events {
		if evt.Type == hooks.RunCompleted {
			completed++
			decoded, decodeErr := hooks.DecodeFromHookInput(&hooks.ActivityInput{
				Type:      evt.Type,
				RunID:     evt.RunID,
				AgentID:   evt.AgentID,
				SessionID: evt.SessionID,
				TurnID:    evt.TurnID,
				Payload:   evt.Payload,
			})
			require.NoError(t, decodeErr)
			var ok bool
			completedEvent, ok = decoded.(*hooks.RunCompletedEvent)
			require.True(t, ok)
		}
	}
	require.Equal(t, 1, completed)
	require.NotNil(t, completedEvent)
	require.Equal(t, hooks.PublicErrorTimeout, completedEvent.PublicError)
	require.Equal(t, hooks.ErrorKindTimeout, completedEvent.ErrorKind)
}

func TestListRunEventsRepairsTerminalCompletionForTailReads(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.reportedStatus = engine.RunStatusFailed

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	firstPage, err := rt.RunEventStore.List(ctx, "run-1", "", 1)
	require.NoError(t, err)
	require.Len(t, firstPage.Events, 1)

	tailPage, err := rt.ListRunEvents(ctx, "run-1", firstPage.Events[0].ID, 10)
	require.NoError(t, err)
	require.Len(t, tailPage.Events, 1)
	require.Equal(t, hooks.RunCompleted, tailPage.Events[0].Type)
}

func TestListRunEventsRepairsTerminalCompletionForNonEmptyTailPage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.reportedStatus = engine.RunStatusFailed

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	page, err := rt.ListRunEvents(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, hooks.RunCompleted, page.Events[len(page.Events)-1].Type)
}

func TestListRunEventsRepairsTerminalCompletionForFullTailPage(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.reportedStatus = engine.RunStatusFailed

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	page, err := rt.ListRunEvents(ctx, "run-1", "", 1)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, hooks.RunCompleted, page.Events[len(page.Events)-1].Type)
}

func TestRunCompletedHookClearsStoredHandleWhenEngineSignalsByID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready: make(chan struct{}),
	}
	rt := newObservedHandleTestRuntime(handle)
	rt.Engine = &signalerWaitEngine{
		controlledWaitEngine: controlledWaitEngine{
			handle: handle,
		},
	}

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	_, err = rt.MustClient(agent.Ident("service.agent")).Start(
		ctx,
		"sess-1",
		nil,
		WithRunID("run-1"),
		WithTurnID("turn-1"),
	)
	require.NoError(t, err)

	stored, ok := rt.workflowHandle("run-1")
	require.True(t, ok)
	require.NotNil(t, stored)

	err = rt.publishHookErr(
		ctx,
		hooks.NewRunCompletedEvent("run-1", "service.agent", "sess-1", runStatusSuccess, run.PhaseCompleted, nil),
		"turn-1",
	)
	require.NoError(t, err)

	_, ok = rt.workflowHandle("run-1")
	require.False(t, ok)
}

func TestGetRunSnapshotUsesObservedHandleBeforeSynthesizingForSignalerEngines(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	handle := &controlledWaitHandle{
		ready: make(chan struct{}),
		err:   context.DeadlineExceeded,
	}
	rt := newObservedHandleTestRuntime(handle)
	rt.Engine = &signalerWaitEngine{
		controlledWaitEngine: controlledWaitEngine{
			handle:         handle,
			reportedStatus: engine.RunStatusFailed,
		},
	}

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	wfHandle, err := rt.MustClient(agent.Ident("service.agent")).Start(
		ctx,
		"sess-1",
		nil,
		WithRunID("run-1"),
		WithTurnID("turn-1"),
	)
	require.NoError(t, err)

	close(handle.ready)

	snapshot, err := rt.GetRunSnapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusFailed, snapshot.Status)

	_, err = wfHandle.Wait(ctx)
	require.ErrorIs(t, err, context.DeadlineExceeded)

	page, err := rt.ListRunEvents(ctx, "run-1", "", 10)
	require.NoError(t, err)
	require.NotEmpty(t, page.Events)

	last := page.Events[len(page.Events)-1]
	decoded, err := hooks.DecodeFromHookInput(&hooks.ActivityInput{
		Type:      last.Type,
		RunID:     last.RunID,
		AgentID:   last.AgentID,
		SessionID: last.SessionID,
		TurnID:    last.TurnID,
		Payload:   last.Payload,
	})
	require.NoError(t, err)

	completed, ok := decoded.(*hooks.RunCompletedEvent)
	require.True(t, ok)
	require.Equal(t, hooks.PublicErrorTimeout, completed.PublicError)
	require.Equal(t, hooks.ErrorKindTimeout, completed.ErrorKind)
}

func TestGetRunSnapshotReturnsStoredStateWhenRepairStatusQueryFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.queryErr = errors.New("temporal unavailable")

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	snapshot, err := rt.GetRunSnapshot(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusRunning, snapshot.Status)
}

func TestListRunEventsReturnsStoredPageWhenRepairStatusQueryFails(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	rt := newObservedHandleTestRuntime(&controlledWaitHandle{
		ready: make(chan struct{}),
	})
	eng, ok := rt.Engine.(*controlledWaitEngine)
	require.True(t, ok)
	eng.queryErr = errors.New("temporal unavailable")

	_, err := rt.CreateSession(ctx, "sess-1")
	require.NoError(t, err)

	input := RunInput{
		AgentID:   "service.agent",
		RunID:     "run-1",
		SessionID: "sess-1",
		TurnID:    "turn-1",
	}
	runCtx := run.Context{
		RunID:     input.RunID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Attempt:   1,
	}
	err = rt.publishHookErr(
		ctx,
		hooks.NewRunStartedEvent(input.RunID, input.AgentID, runCtx, input),
		input.TurnID,
	)
	require.NoError(t, err)

	firstPage, err := rt.RunEventStore.List(ctx, "run-1", "", 1)
	require.NoError(t, err)
	require.Len(t, firstPage.Events, 1)

	page, err := rt.ListRunEvents(ctx, "run-1", firstPage.Events[0].ID, 10)
	require.NoError(t, err)
	require.Empty(t, page.Events)
}
