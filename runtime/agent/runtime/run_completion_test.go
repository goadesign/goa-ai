// Package runtime tests explicit repair of missing terminal records from closed
// workflow history. Snapshot reads are covered separately and remain read-only.
package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

var repairedCompletionTime = time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)

type (
	// completionQueryEngine returns one configured completion while
	// leaving unrelated engine operations unavailable to the test.
	completionQueryEngine struct {
		engine.Engine
		completion    engine.RunCompletion
		completionErr error
	}

	// unavailableTerminalStore reports each terminal repair attempt and then
	// returns a temporary failure so the repair retry loop remains active.
	unavailableTerminalStore struct {
		storage.Store
		called chan struct{}
	}

	// workflowTerminalWinsStore records the workflow's terminal event after the
	// repair read but before the repair write reaches the integrated store.
	workflowTerminalWinsStore struct {
		storage.Store
		workflowTimestamp time.Time
	}

	// workflowSuspensionWinsStore records the workflow's suspension after the
	// repair read but before the repair write reaches the integrated store.
	workflowSuspensionWinsStore struct {
		storage.Store
		workflowTimestamp time.Time
	}

	// countingTerminalRepairStore records how often the runtime asks storage to
	// apply one terminal repair.
	countingTerminalRepairStore struct {
		storage.Store
		calls int
	}

	// lostTerminalRepairResponseStore commits its first repair but reports a
	// temporary response failure so the runtime must repeat the exact command.
	lostTerminalRepairResponseStore struct {
		storage.Store
		calls int
	}
)

func (e completionQueryEngine) QueryRunCompletion(context.Context, string) (engine.RunCompletion, error) {
	return e.completion, e.completionErr
}

func (s unavailableTerminalStore) RepairRunTerminal(context.Context, storage.RunTerminal) (storage.RunRepairResult, error) {
	select {
	case s.called <- struct{}{}:
	default:
	}
	return storage.RunRepairResult{}, errors.New("Session unavailable")
}

func (s workflowTerminalWinsStore) RepairRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	workflow := command
	record := *command.Record
	record.Timestamp = s.workflowTimestamp
	workflow.Record = &record
	if _, err := s.RecordRunTerminal(ctx, workflow); err != nil {
		return storage.RunRepairResult{}, err
	}
	return s.Store.RepairRunTerminal(ctx, command)
}

func (s workflowSuspensionWinsStore) RepairRunSuspension(ctx context.Context, command storage.RunSuspension) (storage.RunRepairResult, error) {
	workflow := command
	record := *command.Record
	record.Timestamp = s.workflowTimestamp
	workflow.Record = &record
	if _, err := s.RecordRunSuspension(ctx, workflow); err != nil {
		return storage.RunRepairResult{}, err
	}
	return s.Store.RepairRunSuspension(ctx, command)
}

func (s *countingTerminalRepairStore) RepairRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	s.calls++
	return s.Store.RepairRunTerminal(ctx, command)
}

func (s *lostTerminalRepairResponseStore) RepairRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	s.calls++
	result, err := s.Store.RepairRunTerminal(ctx, command)
	if err != nil {
		return storage.RunRepairResult{}, err
	}
	if s.calls == 1 {
		return storage.RunRepairResult{}, errors.New("repair response lost")
	}
	return result, nil
}

func TestRepairRunCompletionStoresSuccessfulResultOnce(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session",
		Status: session.RunStatusRunning, Labels: map[string]string{"site": "one"},
	})
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: repairedCompletionTime, Output: &api.RunOutput{
				AgentID: "svc.agent", RunID: "run", Final: &model.Message{},
			}},
		},
	}

	require.NoError(t, runtime.RepairRunCompletion(t.Context(), "run"))
	require.NoError(t, runtime.RepairRunCompletion(t.Context(), "run"))
	meta, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCompleted, meta.Status)
	page, err := store.ListRunRecords(t.Context(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, hooks.RunCompleted, page.Events[1].Type)
	require.Equal(t, repairedCompletionTime, page.Events[1].Timestamp)
}

func TestValidateRepairCompletionRejectsInvalidWorkflowOutput(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		output *api.RunOutput
	}{
		{
			name:   "missing terminal result",
			output: &api.RunOutput{AgentID: "svc.agent", RunID: "run"},
		},
		{
			name: "multiple terminal results",
			output: &api.RunOutput{
				AgentID:    "svc.agent",
				RunID:      "run",
				Final:      &model.Message{},
				Suspension: &api.RunSuspension{},
			},
		},
		{
			name: "invalid suspension",
			output: &api.RunOutput{
				AgentID:    "svc.agent",
				RunID:      "run",
				Suspension: &api.RunSuspension{},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateRepairCompletion(engine.RunCompletion{
				Status:      engine.RunStatusCompleted,
				CompletedAt: repairedCompletionTime,
				Output:      test.output,
			}, &RunInput{AgentID: "svc.agent", RunID: "run"})

			require.ErrorIs(t, err, ErrRunCompletionCorrupt)
		})
	}
}

func TestRepairRunCompletionStoresWorkflowFailure(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completion: engine.RunCompletion{
				Status: engine.RunStatusFailed, CompletedAt: repairedCompletionTime, WorkflowError: errors.New("planner failed"),
			},
		},
	}

	require.NoError(t, runtime.RepairRunCompletion(t.Context(), "run"))
	meta, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusFailed, meta.Status)
}

func TestRepairRunCompletionRetriesStreamWithoutRepeatingStore(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	store := &countingTerminalRepairStore{Store: stored}
	bus := hooks.NewBus()
	busCalls := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	sink := &retryingStreamSink{err: errors.New("stream send failed")}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{
		Store: store,
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: repairedCompletionTime, Output: &api.RunOutput{
			AgentID: "svc.agent", RunID: "run", Final: &model.Message{},
		}}},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.RepairRunCompletion(t.Context(), "run"))
	require.Equal(t, 1, store.calls)
	require.Equal(t, 1, busCalls)
	require.GreaterOrEqual(t, sink.callCount(), 2)
}

func TestRepairRunCompletionExactRetryResumesStreamDelivery(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	store := &lostTerminalRepairResponseStore{Store: stored}
	bus := hooks.NewBus()
	busCalls := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	sink := &retryingStreamSink{}
	subscriber, err := stream.NewSubscriber(sink)
	require.NoError(t, err)
	runtime := &Runtime{
		Store: store,
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status:      engine.RunStatusCompleted,
			CompletedAt: repairedCompletionTime,
			Output: &api.RunOutput{
				AgentID: "svc.agent",
				RunID:   "run",
				Final:   &model.Message{},
			},
		}},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.RepairRunCompletion(t.Context(), "run"))
	require.Equal(t, 2, store.calls)
	require.Zero(t, busCalls)
	require.Equal(t, 2, sink.callCount())
}

func TestRepairRunCompletionStoresSuspension(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning,
	})
	suspension := suspensionContractFixtureWithContext(
		t,
		tools.Ident("svc.tools.lookup"),
		"svc.agent",
		"run-1",
		nil,
		nil,
	)
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: repairedCompletionTime, Output: &api.RunOutput{
				AgentID: "svc.agent", RunID: "run-1", Suspension: suspension,
			}},
		},
	}

	require.NoError(t, runtime.RepairRunCompletion(t.Context(), "run-1"))
	meta, err := store.LoadRun(t.Context(), "run-1")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusSuspended, meta.Status)
	stored, err := runtime.LoadRunSuspension(t.Context(), "run-1")
	require.NoError(t, err)
	require.Equal(t, suspension.ID, stored.ID)
}

func TestRepairRunCompletionDoesNotPublishWhenWorkflowTerminalWins(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	workflowTimestamp := repairedCompletionTime
	bus := hooks.NewBus()
	published := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		published++
		return nil
	}))
	require.NoError(t, err)
	runtime := &Runtime{
		Store: workflowTerminalWinsStore{Store: store, workflowTimestamp: workflowTimestamp},
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusFailed, CompletedAt: repairedCompletionTime,
			WorkflowError: errors.New("planner failed"),
		}},
	}

	require.NoError(t, runtime.RepairRunCompletion(t.Context(), "run"))
	require.Zero(t, published)
	page, err := store.ListRunRecords(t.Context(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, workflowTimestamp, page.Events[1].Timestamp)
}

func TestRepairRunCompletionDoesNotPublishWhenWorkflowSuspensionWins(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning,
	})
	suspension := suspensionContractFixtureWithContext(
		t,
		tools.Ident("svc.tools.lookup"),
		"svc.agent",
		"run-1",
		nil,
		nil,
	)
	workflowTimestamp := repairedCompletionTime.Add(-time.Second)
	bus := hooks.NewBus()
	published := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		published++
		return nil
	}))
	require.NoError(t, err)
	runtime := &Runtime{
		Store: workflowSuspensionWinsStore{Store: store, workflowTimestamp: workflowTimestamp},
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: repairedCompletionTime, Output: &api.RunOutput{
			AgentID: "svc.agent", RunID: "run-1", Suspension: suspension,
		}}},
	}

	require.NoError(t, runtime.RepairRunCompletion(t.Context(), "run-1"))
	require.Zero(t, published)
	page, err := store.ListRunRecords(t.Context(), "run-1", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, workflowTimestamp, page.Events[1].Timestamp)
}

func TestRepairRunCompletionDoesNotStoreRetrievalFailure(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	retrievalErr := errors.New("temporal unavailable")
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completionErr: retrievalErr,
		},
	}

	err := runtime.RepairRunCompletion(t.Context(), "run")
	require.ErrorIs(t, err, retrievalErr)
	meta, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusRunning, meta.Status)
	page, err := store.ListRunRecords(t.Context(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
}

func TestRepairRunCompletionRejectsOpenWorkflow(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	runtime := &Runtime{
		Store:  store,
		Bus:    hooks.NewBus(),
		Engine: completionQueryEngine{completion: engine.RunCompletion{Status: engine.RunStatusRunning}},
	}

	err := runtime.RepairRunCompletion(t.Context(), "run")
	require.ErrorIs(t, err, ErrRunCompletionNotReady)
}

func TestRepairRunCompletionStopsRetryingWhenCallerCancels(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	called := make(chan struct{}, 1)
	runtime := &Runtime{
		Store: unavailableTerminalStore{Store: stored, called: called},
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusFailed, CompletedAt: repairedCompletionTime, WorkflowError: errors.New("planner failed"),
		}},
	}
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() {
		result <- runtime.RepairRunCompletion(ctx, "run")
	}()
	<-called
	cancel()
	require.ErrorIs(t, <-result, context.Canceled)
}
