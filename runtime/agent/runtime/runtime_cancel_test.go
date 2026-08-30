package runtime

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	recordingCancellationEngine struct {
		engine.Engine
		requests []engine.CancellationRequest
		err      error
	}

	// suspensionOrderingControls lets a test stop suspension storage after
	// workflow finalization has claimed the terminal outcome.
	suspensionOrderingControls struct {
		suspensionStarted   chan struct{}
		suspensionRelease   chan struct{}
		cancellationWaiting chan struct{}
		waitOnce            sync.Once
	}

	// suspensionOrderingWorkflowContext exposes the exact ordering between
	// suspension storage and a later cancellation request.
	suspensionOrderingWorkflowContext struct {
		*testWorkflowContext
		controls *suspensionOrderingControls
	}
)

const cancellationPlanActivityName = "plan"

func (e *recordingCancellationEngine) RequestCancellation(_ context.Context, request engine.CancellationRequest) error {
	e.requests = append(e.requests, request)
	return e.err
}

// Context keeps this ordering-aware workflow context attached to storage
// calls made through a standard context.Context value.
func (w *suspensionOrderingWorkflowContext) Context() context.Context {
	return engine.WithWorkflowContext(w.ctx, w)
}

// Detached preserves the ordering controls on cancellation-independent
// workflow work.
func (w *suspensionOrderingWorkflowContext) Detached() engine.WorkflowContext {
	detached := w.testWorkflowContext.Detached().(*testWorkflowContext)
	return &suspensionOrderingWorkflowContext{
		testWorkflowContext: detached,
		controls:            w.controls,
	}
}

// Await reports when a later cancellation is waiting for finalization to
// finish, then delegates the actual wait to the normal test context.
func (w *suspensionOrderingWorkflowContext) Await(condition func() bool) error {
	if !condition() {
		w.controls.waitOnce.Do(func() {
			close(w.controls.cancellationWaiting)
		})
	}
	return w.testWorkflowContext.Await(condition)
}

// ExecuteStorageActivity pauses the suspension write while leaving every
// other command on the real integrated test store.
func (w *suspensionOrderingWorkflowContext) ExecuteStorageActivity(call engine.StorageActivityCall) (*api.StorageActivityResult, error) {
	if call.Command.Suspension != nil {
		close(w.controls.suspensionStarted)
		<-w.controls.suspensionRelease
	}
	return w.testWorkflowContext.ExecuteStorageActivity(call)
}

func TestCancelRunDoesNotRequireRunRecordBeforeWorkflowCommand(t *testing.T) {
	store := newTestStore()
	workflowEngine := &recordingCancellationEngine{Engine: engineinmem.New()}
	runtime := New(store, WithEngine(workflowEngine))
	err := runtime.CancelRun(context.Background(), CancelRequest{
		RunID: "run", Reason: run.CancellationReasonUserRequested,
	})
	require.NoError(t, err)
	require.Equal(t, []engine.CancellationRequest{{
		RunID: "run", Reason: run.CancellationReasonUserRequested,
	}}, workflowEngine.requests)
}

func TestCancelRunReturnsReasonConflict(t *testing.T) {
	workflowEngine := &recordingCancellationEngine{
		Engine: engineinmem.New(),
		err: &engine.CancellationConflictError{
			RunID: "run", Reason: run.CancellationReasonSessionEnded,
		},
	}
	runtime := New(newTestStore(), WithEngine(workflowEngine))
	err := runtime.CancelRun(context.Background(), CancelRequest{
		RunID: "run", Reason: run.CancellationReasonSessionEnded,
	})
	var conflict *CancellationReasonConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "run", conflict.RunID)
	require.Equal(t, run.CancellationReasonSessionEnded, conflict.Reason)
}

func TestCancelRunTreatsCompletedEngineRaceAsSuccess(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "agent", RunID: "run", SessionID: "session", Status: session.RunStatusCompleted,
	})
	workflowEngine := &recordingCancellationEngine{
		Engine: engineinmem.New(),
		err:    engine.ErrWorkflowCompleted,
	}
	runtime := New(store, WithEngine(workflowEngine))
	err := runtime.CancelRun(context.Background(), CancelRequest{
		RunID: "run", Reason: run.CancellationReasonSessionEnded,
	})
	require.NoError(t, err)
}

func TestCancelRunComparesReasonAfterWorkflowClosure(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	meta, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	payload, err := json.Marshal(cancellationIntentPayload{Reason: run.CancellationReasonSessionEnded})
	require.NoError(t, err)
	_, err = store.RecordRunCancellation(t.Context(), storage.RunCancellation{
		RunID: "run", Reason: run.CancellationReasonSessionEnded,
		Record: &runlog.Event{
			EventKey: cancellationIntentEventKey, RunID: "run", AgentID: agent.Ident(meta.AgentID),
			SessionID: meta.SessionID, Type: storage.CancellationRecordType, Payload: payload,
			Timestamp: time.Now().UTC(),
		},
	})
	require.NoError(t, err)
	terminal := testHookRecord(t, hooks.NewRunCompletedEvent(
		"run",
		agent.Ident(meta.AgentID),
		meta.SessionID,
		"canceled",
		run.PhaseCanceled,
		meta.Labels,
		context.Canceled,
		&run.Cancellation{Reason: run.CancellationReasonSessionEnded},
	), terminalRunEventKey, time.Now().UTC())
	_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: "run", Status: session.RunStatusCanceled,
		Record: terminal,
	})
	require.NoError(t, err)
	workflowEngine := &recordingCancellationEngine{
		Engine: engineinmem.New(),
		err:    engine.ErrWorkflowCompleted,
	}
	runtime := New(store, WithEngine(workflowEngine))

	require.NoError(t, runtime.CancelRun(t.Context(), CancelRequest{
		RunID: "run", Reason: run.CancellationReasonSessionEnded,
	}))
	err = runtime.CancelRun(t.Context(), CancelRequest{
		RunID: "run", Reason: run.CancellationReasonUserRequested,
	})
	var conflict *CancellationReasonConflictError
	require.ErrorAs(t, err, &conflict)
	require.Equal(t, "run", conflict.RunID)
	require.Equal(t, run.CancellationReasonUserRequested, conflict.Reason)
}

func TestCancelRunRejectsMissingWorkflowForActiveRun(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	workflowEngine := &recordingCancellationEngine{
		Engine: engineinmem.New(),
		err:    engine.ErrWorkflowNotFound,
	}
	runtime := New(store, WithEngine(workflowEngine))
	err := runtime.CancelRun(context.Background(), CancelRequest{
		RunID: "run", Reason: run.CancellationReasonSessionEnded,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, engine.ErrWorkflowNotFound)
	require.NotErrorIs(t, err, session.ErrRunNotFound)
}

func TestAcceptedCancellationReplacesSuccessfulWorkflowReturn(t *testing.T) {
	store := newTestStore()
	_, err := store.CreateSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)

	var wfCtx *testWorkflowContext
	pl := &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		require.NotNil(t, wfCtx.cancellationHandler)
		err := wfCtx.cancellationHandler(wfCtx, engine.CancellationRequest{
			RunID:  "run",
			Reason: run.CancellationReasonUserRequested,
		})
		require.NoError(t, err)
		return finalPlannerResult("completed before cancellation was applied"), nil
	}}
	runtime := newTestRuntimeWithPlanner("agent", pl)
	runtime.Store = store
	registration := runtime.agents["agent"]
	registration.PlanActivityName = cancellationPlanActivityName
	runtime.agents["agent"] = registration
	wfCtx = &testWorkflowContext{
		ctx:         t.Context(),
		runtime:     runtime,
		hookRuntime: runtime,
	}

	output, err := runtime.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   "agent",
		RunID:     "run",
		SessionID: "session",
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, output)
	stored, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCanceled, stored.Status)
	require.Equal(t, run.CancellationReasonUserRequested, stored.CancellationReason)
}

func TestAcceptedCancellationReplacesWorkflowSuspension(t *testing.T) {
	store := newTestStore()
	_, err := store.CreateSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)

	var wfCtx *testWorkflowContext
	pl := &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		require.NotNil(t, wfCtx.cancellationHandler)
		err := wfCtx.cancellationHandler(wfCtx, engine.CancellationRequest{
			RunID:  "run",
			Reason: run.CancellationReasonUserRequested,
		})
		require.NoError(t, err)
		return &planner.PlanResult{Await: planner.NewAwait(
			planner.AwaitClarificationItem(&planner.AwaitClarification{
				ID:       "clarification",
				Question: "Which facility?",
			}),
		)}, nil
	}}
	runtime := newTestRuntimeWithPlanner("agent", pl)
	runtime.Store = store
	registration := runtime.agents["agent"]
	registration.PlanActivityName = cancellationPlanActivityName
	runtime.agents["agent"] = registration
	wfCtx = &testWorkflowContext{
		ctx:         t.Context(),
		runtime:     runtime,
		hookRuntime: runtime,
	}

	output, err := runtime.ExecuteWorkflow(wfCtx, &RunInput{
		AgentID:   "agent",
		RunID:     "run",
		SessionID: "session",
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, output)
	stored, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCanceled, stored.Status)
	require.Equal(t, run.CancellationReasonUserRequested, stored.CancellationReason)
	_, err = store.LoadRunSuspension(t.Context(), "run")
	require.ErrorIs(t, err, session.ErrRunSuspensionNotFound)
}

func TestWorkflowSuspensionRejectsCancellationAfterPersistenceStarts(t *testing.T) {
	store := newTestStore()
	_, err := store.CreateSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)

	pl := &stubPlanner{start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
		return &planner.PlanResult{Await: planner.NewAwait(
			planner.AwaitClarificationItem(&planner.AwaitClarification{
				ID:       "clarification",
				Question: "Which facility?",
			}),
		)}, nil
	}}
	runtime := newTestRuntimeWithPlanner("agent", pl)
	runtime.Store = store
	registration := runtime.agents["agent"]
	registration.PlanActivityName = cancellationPlanActivityName
	runtime.agents["agent"] = registration
	controls := &suspensionOrderingControls{
		suspensionStarted:   make(chan struct{}),
		suspensionRelease:   make(chan struct{}),
		cancellationWaiting: make(chan struct{}),
	}
	wfCtx := &suspensionOrderingWorkflowContext{
		testWorkflowContext: &testWorkflowContext{
			ctx:         t.Context(),
			runtime:     runtime,
			hookRuntime: runtime,
		},
		controls: controls,
	}
	type workflowResult struct {
		output *RunOutput
		err    error
	}
	workflowDone := make(chan workflowResult, 1)
	go func() {
		output, executeErr := runtime.ExecuteWorkflow(wfCtx, &RunInput{
			AgentID:   "agent",
			RunID:     "run",
			SessionID: "session",
		})
		workflowDone <- workflowResult{output: output, err: executeErr}
	}()
	<-controls.suspensionStarted

	cancellationDone := make(chan error, 1)
	go func() {
		cancellationDone <- wfCtx.cancellationHandler(wfCtx, engine.CancellationRequest{
			RunID:  "run",
			Reason: run.CancellationReasonUserRequested,
		})
	}()
	<-controls.cancellationWaiting
	close(controls.suspensionRelease)

	result := <-workflowDone
	require.NoError(t, result.err)
	require.NotNil(t, result.output)
	require.NotNil(t, result.output.Suspension)
	require.ErrorIs(t, <-cancellationDone, engine.ErrWorkflowCompleted)
	stored, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusSuspended, stored.Status)
	require.Empty(t, stored.CancellationReason)
	suspension, err := store.LoadRunSuspension(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, result.output.Suspension.ID, suspension.ID)
	page, err := store.ListRunRecords(t.Context(), "run", "", 100)
	require.NoError(t, err)
	var suspended, completed, cancellation int
	for _, record := range page.Events {
		switch record.Type {
		case hooks.RunSuspended:
			suspended++
		case hooks.RunCompleted:
			completed++
		case storage.CancellationRecordType:
			cancellation++
		}
	}
	require.Equal(t, 1, suspended)
	require.Zero(t, completed)
	require.Zero(t, cancellation)
}

func TestWorkflowFinalizationRejectsCancellationAfterTerminalStorageStarts(t *testing.T) {
	state := &workflowFinalizationState{}
	wfCtx := &testWorkflowContext{ctx: t.Context()}
	accepted, err := state.beginFinalization(wfCtx)
	require.NoError(t, err)
	require.False(t, accepted)

	done := make(chan error, 1)
	go func() {
		done <- state.beginCancellation(wfCtx)
	}()
	select {
	case err := <-done:
		t.Fatalf("cancellation completed before terminal storage: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	state.finishFinalization()
	require.ErrorIs(t, <-done, engine.ErrWorkflowCompleted)
}

func TestWorkflowFinalizationWaitsForAcceptedCancellation(t *testing.T) {
	state := &workflowFinalizationState{}
	wfCtx := &testWorkflowContext{ctx: t.Context()}
	require.NoError(t, state.beginCancellation(wfCtx))

	type finalizationResult struct {
		accepted bool
		err      error
	}
	done := make(chan finalizationResult, 1)
	go func() {
		accepted, err := state.beginFinalization(wfCtx)
		done <- finalizationResult{accepted: accepted, err: err}
	}()
	select {
	case result := <-done:
		t.Fatalf("finalization completed before cancellation storage: %v", result.err)
	case <-time.After(20 * time.Millisecond):
	}

	state.finishCancellation(true)
	result := <-done
	require.NoError(t, result.err)
	require.True(t, result.accepted)
	state.finishFinalization()
	require.ErrorIs(t, state.beginCancellation(wfCtx), engine.ErrWorkflowCompleted)
}

func TestWorkflowFinalizationReopensAfterFailedCancellation(t *testing.T) {
	state := &workflowFinalizationState{}
	wfCtx := &testWorkflowContext{ctx: t.Context()}
	require.NoError(t, state.beginCancellation(wfCtx))
	state.finishCancellation(false)
	require.NoError(t, state.beginCancellation(wfCtx))
	state.finishCancellation(true)
	accepted, err := state.beginFinalization(wfCtx)
	require.NoError(t, err)
	require.True(t, accepted)
	state.finishFinalization()
	require.ErrorIs(t, state.beginCancellation(wfCtx), engine.ErrWorkflowCompleted)
}
