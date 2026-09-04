// Package runtime tests explicit completion recovery and redelivery from closed
// workflow history. Snapshot reads are covered separately and remain read-only.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
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
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/stream"
	"goa.design/goa-ai/runtime/agent/tools"
)

const completionParentRunID = "parent"

var ensuredCompletionTime = time.Date(2026, time.August, 29, 12, 0, 0, 0, time.UTC)

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
		endSession        bool
		reportedStatus    session.RunStatus
	}

	// workflowSuspensionWinsStore records the workflow's suspension after the
	// repair read but before the repair write reaches the integrated store.
	workflowSuspensionWinsStore struct {
		storage.Store
		workflowTimestamp time.Time
		endSession        bool
	}

	// differentTerminalRepairStore reports that another terminal record owns a
	// run even when repair supplies the stored record.
	differentTerminalRepairStore struct {
		storage.Store
	}

	// countingTerminalRepairStore records how often the runtime asks storage to
	// apply one terminal repair.
	countingTerminalRepairStore struct {
		storage.Store
		calls int
	}

	// sessionStatusRepairStore changes only the Session status returned beside
	// a successfully stored terminal record.
	sessionStatusRepairStore struct {
		storage.Store
		status session.SessionStatus
	}

	// lostTerminalRepairResponseStore commits its first repair but reports a
	// temporary response failure so the runtime must repeat the exact command.
	lostTerminalRepairResponseStore struct {
		storage.Store
		calls int
	}

	// completionReplayStore observes or changes stored completion reads for
	// focused recovery tests.
	completionReplayStore struct {
		storage.Store
		list              func(runlog.Page) runlog.Page
		listRun           func(string, runlog.Page) runlog.Page
		loadRun           func(session.RunMeta) session.RunMeta
		loadSuspension    func(session.RunSuspension) session.RunSuspension
		terminalCommand   *storage.RunTerminal
		suspensionCommand *storage.RunSuspension
	}

	// completionEventSink records the events sent while ensuring completion.
	completionEventSink struct {
		events []stream.Event
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
	if s.endSession {
		host := s.Store.(interface {
			EndSession(context.Context, string, time.Time) (session.Session, error)
		})
		if _, err := host.EndSession(ctx, command.Record.SessionID, time.Now().UTC()); err != nil {
			return storage.RunRepairResult{}, err
		}
	}
	result, err := s.Store.RepairRunTerminal(ctx, command)
	if err == nil && s.reportedStatus != "" {
		result.Status = s.reportedStatus
	}
	return result, err
}

func (s workflowSuspensionWinsStore) RepairRunSuspension(ctx context.Context, command storage.RunSuspension) (storage.RunRepairResult, error) {
	workflow := command
	record := *command.Record
	record.Timestamp = s.workflowTimestamp
	workflow.Record = &record
	if _, err := s.RecordRunSuspension(ctx, workflow); err != nil {
		return storage.RunRepairResult{}, err
	}
	if s.endSession {
		host := s.Store.(interface {
			EndSession(context.Context, string, time.Time) (session.Session, error)
		})
		if _, err := host.EndSession(ctx, command.Record.SessionID, time.Now().UTC()); err != nil {
			return storage.RunRepairResult{}, err
		}
	}
	return s.Store.RepairRunSuspension(ctx, command)
}

func (s differentTerminalRepairStore) RepairRunTerminal(context.Context, storage.RunTerminal) (storage.RunRepairResult, error) {
	return storage.RunRepairResult{
		Outcome: storage.RunRepairDifferentTerminal,
		Status:  session.RunStatusCompleted,
	}, nil
}

func (s *countingTerminalRepairStore) RepairRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	s.calls++
	return s.Store.RepairRunTerminal(ctx, command)
}

func (s sessionStatusRepairStore) RepairRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	result, err := s.Store.RepairRunTerminal(ctx, command)
	if err == nil {
		result.Record.SessionStatus = s.status
	}
	return result, err
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

func (s *completionReplayStore) ListRunRecords(ctx context.Context, runID, cursor string, limit int) (runlog.Page, error) {
	page, err := s.Store.ListRunRecords(ctx, runID, cursor, limit)
	if err != nil {
		return page, err
	}
	if s.list != nil {
		page = s.list(page)
	}
	if s.listRun != nil {
		page = s.listRun(runID, page)
	}
	return page, nil
}

func (s *completionReplayStore) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	meta, err := s.Store.LoadRun(ctx, runID)
	if err != nil || s.loadRun == nil {
		return meta, err
	}
	return s.loadRun(meta), nil
}

func (s *completionReplayStore) LoadRunSuspension(ctx context.Context, runID string) (session.RunSuspension, error) {
	suspension, err := s.Store.LoadRunSuspension(ctx, runID)
	if err != nil || s.loadSuspension == nil {
		return suspension, err
	}
	return s.loadSuspension(suspension), nil
}

func (s *completionReplayStore) RepairRunTerminal(ctx context.Context, command storage.RunTerminal) (storage.RunRepairResult, error) {
	s.terminalCommand = &command
	return s.Store.RepairRunTerminal(ctx, command)
}

func (s *completionReplayStore) RepairRunSuspension(ctx context.Context, command storage.RunSuspension) (storage.RunRepairResult, error) {
	s.suspensionCommand = &command
	return s.Store.RepairRunSuspension(ctx, command)
}

func (s *completionEventSink) Send(_ context.Context, event stream.Event) error {
	s.events = append(s.events, event)
	return nil
}

func (s *completionEventSink) Close(context.Context) error {
	return nil
}

// newCompletionSubscriber provides a working stream for tests whose success
// includes delivery to an active Session.
func newCompletionSubscriber(t *testing.T) *stream.Subscriber {
	t.Helper()
	subscriber, err := stream.NewSubscriber(&completionEventSink{}, stream.AgentDebugProfile())
	require.NoError(t, err)
	return subscriber
}

func TestEnsureRunCompletionStoresSuccessfulResultOnce(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session",
		Status: session.RunStatusRunning, Labels: map[string]string{"site": "one"},
	})
	runtime := &Runtime{
		Store: store,
		Bus:   hooks.NewBus(),
		Engine: completionQueryEngine{
			completion: engine.RunCompletion{Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime, Output: &api.RunOutput{
				AgentID: "svc.agent", RunID: "run", Final: &model.Message{},
			}},
		},
		streamSubscriber: newCompletionSubscriber(t),
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run"))
	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run"))
	meta, err := store.LoadRun(t.Context(), "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCompleted, meta.Status)
	page, err := store.ListRunRecords(t.Context(), "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, hooks.RunCompleted, page.Events[1].Type)
	require.Equal(t, ensuredCompletionTime, page.Events[1].Timestamp)
}

func TestEnsureRunCompletionReplaysStoredTerminalExactly(t *testing.T) {
	stored := newTestStore()
	meta := session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session",
		Status: session.RunStatusRunning, Labels: map[string]string{"site": "one"},
	}
	admitRunForTest(t, stored, meta)
	at := ensuredCompletionTime.Add(123 * time.Millisecond)
	record := testHookRecord(t, hooks.NewRunCompletedEvent(
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		"failed",
		run.PhaseFailed,
		meta.Labels,
		errors.New("planner kept its exact failure"),
		nil,
	), "stored-terminal", at)
	_, err := stored.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: meta.RunID, Status: session.RunStatusFailed, Record: record,
	})
	require.NoError(t, err)

	store := &completionReplayStore{Store: stored}
	bus := hooks.NewBus()
	busCalls := 0
	_, err = bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	sink := &retryingStreamSink{}
	subscriber, err := stream.NewSubscriber(sink, stream.AgentDebugProfile())
	require.NoError(t, err)
	runtime := &Runtime{
		Store: store,
		Bus:   bus,
		Engine: completionQueryEngine{
			completionErr: errors.New("engine history must not be queried"),
		},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), meta.RunID))
	require.NotNil(t, store.terminalCommand)
	require.Equal(t, record.EventKey, store.terminalCommand.Record.EventKey)
	require.Equal(t, record.Timestamp, store.terminalCommand.Record.Timestamp)
	require.Equal(t, []byte(record.Payload), []byte(store.terminalCommand.Record.Payload))
	require.Zero(t, busCalls)
	require.Equal(t, 2, sink.callCount())
}

func TestEnsureRunCompletionRequiresStreamForActiveSession(t *testing.T) {
	stored := newTestStore()
	meta := session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session",
		Status: session.RunStatusRunning,
	}
	admitRunForTest(t, stored, meta)
	record := testHookRecord(t, hooks.NewRunCompletedEvent(
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		"success",
		run.PhaseCompleted,
		nil,
		nil,
		nil,
	), "stored-terminal", ensuredCompletionTime)
	_, err := stored.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: meta.RunID, Status: session.RunStatusCompleted, Record: record,
	})
	require.NoError(t, err)
	store := &completionReplayStore{Store: stored}
	runtime := &Runtime{
		Store:  store,
		Bus:    hooks.NewBus(),
		Engine: completionQueryEngine{completionErr: errors.New("engine history must not be queried")},
	}

	err = runtime.EnsureRunCompletion(t.Context(), meta.RunID)

	require.ErrorContains(t, err, `active Session "session" requires Runtime.WithStream`)
	require.NotNil(t, store.terminalCommand)
	require.Nil(t, store.suspensionCommand)
}

func TestEnsureRunCompletionNotifiesBusOnceBeforeMissingStream(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	bus := hooks.NewBus()
	busCalls := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	runtime := &Runtime{
		Store: store,
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime,
			Output: &api.RunOutput{AgentID: "svc.agent", RunID: "run", Final: &model.Message{}},
		}},
	}

	err = runtime.EnsureRunCompletion(t.Context(), "run")
	require.ErrorContains(t, err, `active Session "session" requires Runtime.WithStream`)
	require.Equal(t, 1, busCalls)

	sink := &retryingStreamSink{}
	runtime.streamSubscriber, err = stream.NewSubscriber(sink, stream.AgentDebugProfile())
	require.NoError(t, err)
	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "run"))
	require.Equal(t, 1, busCalls)
	require.Equal(t, 2, sink.callCount())
}

func TestEnsureRunCompletionValidatesSessionStatusBeforeBus(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	bus := hooks.NewBus()
	busCalls := 0
	_, err := bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	runtime := &Runtime{
		Store: sessionStatusRepairStore{Store: stored, status: "unknown"},
		Bus:   bus,
		Engine: completionQueryEngine{completion: engine.RunCompletion{
			Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime,
			Output: &api.RunOutput{AgentID: "svc.agent", RunID: "run", Final: &model.Message{}},
		}},
		streamSubscriber: newCompletionSubscriber(t),
	}

	err = runtime.EnsureRunCompletion(t.Context(), "run")
	require.ErrorContains(t, err, `Session "session" has unsupported status "unknown"`)
	require.Zero(t, busCalls)
}

func TestEnsureRunCompletionReplaysStoredSuspensionExactly(t *testing.T) {
	stored := newTestStore()
	meta := session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning,
	}
	admitRunForTest(t, stored, meta)
	publicSuspension := suspensionContractFixture(t, tools.Ident("svc.tools.lookup"))
	data, err := json.Marshal(publicSuspension)
	require.NoError(t, err)
	suspension := session.RunSuspension{ID: publicSuspension.ID, Data: data}
	at := ensuredCompletionTime.Add(456 * time.Millisecond)
	record := testHookRecord(t, hooks.NewRunSuspendedEvent(
		meta.RunID,
		agent.Ident(meta.AgentID),
		meta.SessionID,
		suspension.ID,
		publicSuspension.Version,
		len(publicSuspension.Pending),
		publicSuspension.RequiredTools,
	), "stored-suspension", at)
	_, err = stored.RecordRunSuspension(t.Context(), storage.RunSuspension{
		RunID: meta.RunID, Suspension: suspension, Record: record,
	})
	require.NoError(t, err)

	store := &completionReplayStore{Store: stored}
	sink := &retryingStreamSink{}
	subscriber, err := stream.NewSubscriber(sink, stream.AgentDebugProfile())
	require.NoError(t, err)
	runtime := &Runtime{
		Store:            store,
		Bus:              hooks.NewBus(),
		Engine:           completionQueryEngine{completionErr: errors.New("engine history must not be queried")},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), meta.RunID))
	require.NotNil(t, store.suspensionCommand)
	require.Equal(t, suspension, store.suspensionCommand.Suspension)
	require.Equal(t, record.EventKey, store.suspensionCommand.Record.EventKey)
	require.Equal(t, record.Timestamp, store.suspensionCommand.Record.Timestamp)
	require.Equal(t, []byte(record.Payload), []byte(store.suspensionCommand.Record.Payload))
	require.Equal(t, 2, sink.callCount())
}

func TestEnsureRunCompletionRejectsSuspensionEventMismatch(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*hooks.RunSuspendedEvent)
	}{
		{
			name: "checkpoint id",
			mutate: func(event *hooks.RunSuspendedEvent) {
				event.SuspensionID = "different"
			},
		},
		{
			name: "checkpoint version",
			mutate: func(event *hooks.RunSuspendedEvent) {
				event.Version = "different"
			},
		},
		{
			name: "pending input count",
			mutate: func(event *hooks.RunSuspendedEvent) {
				event.PendingCount++
			},
		},
		{
			name: "required tools",
			mutate: func(event *hooks.RunSuspendedEvent) {
				event.RequiredTools = append(event.RequiredTools, "svc.tools.other")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stored := newTestStore()
			meta := session.RunMeta{
				AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
				Status: session.RunStatusRunning,
			}
			admitRunForTest(t, stored, meta)
			publicSuspension := suspensionContractFixture(t, tools.Ident("svc.tools.lookup"))
			data, err := json.Marshal(publicSuspension)
			require.NoError(t, err)
			suspension := session.RunSuspension{ID: publicSuspension.ID, Data: data}
			record := testHookRecord(t, hooks.NewRunSuspendedEvent(
				meta.RunID,
				agent.Ident(meta.AgentID),
				meta.SessionID,
				suspension.ID,
				publicSuspension.Version,
				len(publicSuspension.Pending),
				publicSuspension.RequiredTools,
			), "stored-suspension", ensuredCompletionTime)
			_, err = stored.RecordRunSuspension(t.Context(), storage.RunSuspension{
				RunID: meta.RunID, Suspension: suspension, Record: record,
			})
			require.NoError(t, err)

			store := &completionReplayStore{
				Store: stored,
				list: func(page runlog.Page) runlog.Page {
					page = cloneCompletionPage(page)
					storedEvent := page.Events[len(page.Events)-1]
					decoded, decodeErr := hooks.DecodeRunlogEvent(storedEvent)
					require.NoError(t, decodeErr)
					suspended := decoded.(*hooks.RunSuspendedEvent)
					test.mutate(suspended)
					payload, encodeErr := hooks.EncodeRecordPayload(suspended)
					require.NoError(t, encodeErr)
					storedEvent.Payload = payload
					return page
				},
			}
			runtime := &Runtime{
				Store: store,
				Bus:   hooks.NewBus(),
				Engine: completionQueryEngine{
					completionErr: errors.New("engine history must not be queried"),
				},
			}

			err = runtime.EnsureRunCompletion(t.Context(), meta.RunID)
			require.ErrorIs(t, err, ErrRunCompletionCorrupt)
			require.Nil(t, store.suspensionCommand)
		})
	}
}

func TestEnsureRunCompletionDoesNotReplayStoredCompletionForEndedSession(t *testing.T) {
	stored := newTestStore()
	meta := session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session",
		Status: session.RunStatusCompleted,
	}
	admitRunForTest(t, stored, meta)
	_, err := stored.EndSession(t.Context(), meta.SessionID, ensuredCompletionTime)
	require.NoError(t, err)
	store := &completionReplayStore{Store: stored}
	runtime := &Runtime{
		Store:  store,
		Bus:    hooks.NewBus(),
		Engine: completionQueryEngine{completionErr: errors.New("engine history must not be queried")},
	}

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), meta.RunID))
	require.NotNil(t, store.terminalCommand)
}

func TestEnsureChildRunLinkRequiresStreamForActiveSession(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "parent.agent", RunID: "parent", SessionID: "session",
		Status: session.RunStatusRunning,
	})
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "child.agent", RunID: "child", SessionID: "session",
		ParentRunID: "parent", Status: session.RunStatusCompleted,
	})
	runtime := &Runtime{Store: stored, Bus: hooks.NewBus()}

	err := runtime.EnsureChildRunLink(t.Context(), "child")

	require.ErrorContains(t, err, `active Session "session" requires Runtime.WithStream`)
}

func TestEnsureChildRunLinkRejectsCorruptionBeforeMissingStream(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "parent.agent", RunID: "parent", SessionID: "session",
		Status: session.RunStatusRunning,
	})
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "child.agent", RunID: "child", SessionID: "session",
		ParentRunID: "parent", Status: session.RunStatusCompleted,
	})
	store := &completionReplayStore{
		Store: stored,
		loadRun: func(meta session.RunMeta) session.RunMeta {
			if meta.RunID == "parent" {
				meta.SessionID = "other"
			}
			return meta
		},
	}
	runtime := &Runtime{Store: store, Bus: hooks.NewBus()}

	err := runtime.EnsureChildRunLink(t.Context(), "child")
	require.ErrorIs(t, err, ErrRunCompletionCorrupt)
	require.ErrorContains(t, err, "stored parent and child belong to different sessions")
	require.NotContains(t, err.Error(), "requires Runtime.WithStream")
}

func TestEnsureRunCompletionRedeliversStoredChildLinkBeforeCompletion(t *testing.T) {
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{
		AgentID: "parent.agent", RunID: "parent", SessionID: "session", Status: session.RunStatusRunning,
	})
	started, err := prepareHookRecordInput(t.Context(), hooks.NewRunStartedEvent(
		"child",
		agent.Ident("child.agent"),
		"session",
		"parent",
		"",
		nil,
	), "turn")
	require.NoError(t, err)
	linkedInput, err := prepareHookRecordInput(t.Context(), hooks.NewChildRunLinkedEvent(
		"parent",
		agent.Ident("parent.agent"),
		"session",
		tools.Ident("parent.tools.child"),
		"tool-call",
		"child",
		agent.Ident("child.agent"),
	), "turn")
	require.NoError(t, err)
	failedSubscriber, err := stream.NewSubscriber(
		failingStreamSink{err: errors.New("child link delivery failed")},
		stream.AgentDebugProfile(),
	)
	require.NoError(t, err)
	startRuntime := &Runtime{
		Store: stored, Bus: hooks.NewBus(), streamSubscriber: failedSubscriber,
	}
	_, err = startRuntime.executeStorageCommand(t.Context(), &api.StorageActivityCommand{
		ChildStart: &api.ChildRunStartCommand{ParentLinked: linkedInput, Started: started},
	})
	require.ErrorContains(t, err, "child link delivery failed")
	completed := testHookRecord(t, hooks.NewRunCompletedEvent(
		"child",
		agent.Ident("child.agent"),
		"session",
		"success",
		run.PhaseCompleted,
		nil,
		nil,
		nil,
	), terminalRunEventKey, time.Now().UTC().Truncate(time.Millisecond))
	_, err = stored.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: "child", Status: session.RunStatusCompleted, Record: completed,
	})
	require.NoError(t, err)
	parentRecords, err := stored.ListRunRecords(t.Context(), "parent", "", 10)
	require.NoError(t, err)
	require.Equal(t, hooks.ChildRunLinked, parentRecords.Events[1].Type)
	childRecords, err := stored.ListRunRecords(t.Context(), "child", "", 10)
	require.NoError(t, err)
	require.Equal(t, []runlog.Type{hooks.RunStarted, hooks.RunCompleted}, []runlog.Type{
		childRecords.Events[0].Type,
		childRecords.Events[1].Type,
	})

	bus := hooks.NewBus()
	busCalls := 0
	_, err = bus.Register(hooks.SubscriberFunc(func(context.Context, hooks.Event) error {
		busCalls++
		return nil
	}))
	require.NoError(t, err)
	sink := &completionEventSink{}
	subscriber, err := stream.NewSubscriber(sink, stream.AgentDebugProfile())
	require.NoError(t, err)
	runtime := &Runtime{
		Store:            stored,
		Bus:              bus,
		Engine:           completionQueryEngine{completionErr: errors.New("engine history must not be queried")},
		streamSubscriber: subscriber,
	}

	require.NoError(t, runtime.EnsureChildRunLink(t.Context(), "child"))
	require.Zero(t, busCalls)
	require.Len(t, sink.events, 1)
	linked, ok := sink.events[0].(stream.ChildRunLinked)
	require.True(t, ok)
	require.Equal(t, "child", linked.Data.ChildRunID)
	sink.events = nil

	require.NoError(t, runtime.EnsureRunCompletion(t.Context(), "child"))
	require.Zero(t, busCalls)
	require.Len(t, sink.events, 3)
	linked, ok = sink.events[0].(stream.ChildRunLinked)
	require.True(t, ok)
	require.Equal(t, "child", linked.Data.ChildRunID)
	require.Equal(t, agent.Ident("child.agent"), linked.Data.ChildAgentID)
	require.Equal(t, parentRecords.Events[1].EventKey, linked.EventKey())
	require.Equal(t, parentRecords.Events[1].Timestamp, linked.OccurredAt())
	require.Equal(t, stream.EventWorkflow, sink.events[1].Type())
	require.Equal(t, stream.EventRunStreamEnd, sink.events[2].Type())
}

func TestEnsureRunCompletionRejectsCorruptStoredChildPublication(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(string, runlog.Page) runlog.Page
	}{
		{
			name: "unknown child link field",
			mutate: func(runID string, page runlog.Page) runlog.Page {
				if runID != completionParentRunID {
					return page
				}
				page = cloneCompletionPage(page)
				link := page.Events[len(page.Events)-1]
				link.Payload = append(link.Payload[:len(link.Payload)-1], []byte(`,"unknown":true}`)...)
				return page
			},
		},
		{
			name: "trailing child start value",
			mutate: func(runID string, page runlog.Page) runlog.Page {
				if runID != "child" {
					return page
				}
				page = cloneCompletionPage(page)
				page.Events[0].Payload = append(page.Events[0].Payload, []byte(` {}`)...)
				return page
			},
		},
		{
			name: "missing parent tool name",
			mutate: func(runID string, page runlog.Page) runlog.Page {
				if runID != completionParentRunID {
					return page
				}
				page = cloneCompletionPage(page)
				link := page.Events[len(page.Events)-1]
				decoded, err := hooks.DecodeRunlogEvent(link)
				require.NoError(t, err)
				linked := decoded.(*hooks.ChildRunLinkedEvent)
				linked.ToolName = ""
				payload, err := hooks.EncodeRecordPayload(linked)
				require.NoError(t, err)
				link.Payload = payload
				return page
			},
		},
		{
			name: "missing parent tool call id",
			mutate: func(runID string, page runlog.Page) runlog.Page {
				if runID != completionParentRunID {
					return page
				}
				page = cloneCompletionPage(page)
				link := page.Events[len(page.Events)-1]
				decoded, err := hooks.DecodeRunlogEvent(link)
				require.NoError(t, err)
				linked := decoded.(*hooks.ChildRunLinkedEvent)
				linked.ToolCallID = ""
				payload, err := hooks.EncodeRecordPayload(linked)
				require.NoError(t, err)
				link.Payload = payload
				return page
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stored := newTestStore()
			admitRunForTest(t, stored, session.RunMeta{
				AgentID: "parent.agent", RunID: completionParentRunID, SessionID: "session", Status: session.RunStatusRunning,
			})
			admitRunForTest(t, stored, session.RunMeta{
				AgentID: "child.agent", RunID: "child", SessionID: "session",
				ParentRunID: completionParentRunID, Status: session.RunStatusCompleted,
			})
			store := &completionReplayStore{Store: stored, listRun: test.mutate}
			runtime := &Runtime{
				Store:  store,
				Bus:    hooks.NewBus(),
				Engine: completionQueryEngine{completionErr: errors.New("engine history must not be queried")},
			}

			err := runtime.EnsureRunCompletion(t.Context(), "child")
			require.ErrorIs(t, err, ErrRunCompletionCorrupt)
			require.ErrorContains(t, err, `run "child" stored lifecycle`)
			require.NotContains(t, err.Error(), "requires Runtime.WithStream")
		})
	}
}

func TestEnsureRunCompletionRejectsCorruptStoredStartBeforeWritingCompletion(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*runlog.Event)
	}{
		{
			name: "malformed payload",
			mutate: func(record *runlog.Event) {
				record.Payload = []byte(`{`)
			},
		},
		{
			name: "unknown payload field",
			mutate: func(record *runlog.Event) {
				record.Payload = []byte(`{"labels":{"site":"one"},"unknown":true}`)
			},
		},
		{
			name: "different timestamp",
			mutate: func(record *runlog.Event) {
				record.Timestamp = record.Timestamp.Add(time.Millisecond)
			},
		},
		{
			name: "different parent",
			mutate: func(record *runlog.Event) {
				record.Payload = []byte(`{"parent_run_id":"other","labels":{"site":"one"}}`)
			},
		},
		{
			name: "self predecessor",
			mutate: func(record *runlog.Event) {
				record.Payload = []byte(`{"predecessor_run_id":"run","labels":{"site":"one"}}`)
			},
		},
		{
			name: "different labels",
			mutate: func(record *runlog.Event) {
				record.Payload = []byte(`{"labels":{"site":"other"}}`)
			},
		},
		{
			name: "different session",
			mutate: func(record *runlog.Event) {
				record.SessionID = "other"
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stored := newTestStore()
			meta := session.RunMeta{
				AgentID: "svc.agent", RunID: "run", SessionID: "session",
				Status: session.RunStatusRunning, Labels: map[string]string{"site": "one"},
			}
			admitRunForTest(t, stored, meta)
			store := &completionReplayStore{
				Store: stored,
				list: func(page runlog.Page) runlog.Page {
					page = cloneCompletionPage(page)
					test.mutate(page.Events[0])
					return page
				},
			}
			runtime := &Runtime{
				Store: store,
				Bus:   hooks.NewBus(),
				Engine: completionQueryEngine{completion: engine.RunCompletion{
					Status: engine.RunStatusCompleted, CompletedAt: ensuredCompletionTime,
					Output: &api.RunOutput{
						AgentID: agent.Ident(meta.AgentID), RunID: meta.RunID, Final: &model.Message{},
					},
				}},
			}

			err := runtime.EnsureRunCompletion(t.Context(), meta.RunID)
			require.ErrorIs(t, err, ErrRunCompletionCorrupt)
			require.Nil(t, store.terminalCommand)
			storedMeta, loadErr := stored.LoadRun(t.Context(), meta.RunID)
			require.NoError(t, loadErr)
			require.Equal(t, session.RunStatusRunning, storedMeta.Status)
		})
	}
}

func TestEnsureRunCompletionRejectsCorruptStoredCompletion(t *testing.T) {
	for _, test := range []struct {
		name           string
		status         session.RunStatus
		list           func(runlog.Page) runlog.Page
		loadRun        func(session.RunMeta) session.RunMeta
		loadSuspension func(session.RunSuspension) session.RunSuspension
	}{
		{
			name:   "malformed payload",
			status: session.RunStatusCompleted,
			list: func(page runlog.Page) runlog.Page {
				page = cloneCompletionPage(page)
				page.Events[len(page.Events)-1].Payload = []byte(`{`)
				return page
			},
		},
		{
			name:   "multiple completion records",
			status: session.RunStatusCompleted,
			list: func(page runlog.Page) runlog.Page {
				page = cloneCompletionPage(page)
				duplicate := *page.Events[len(page.Events)-1]
				duplicate.ID += "-duplicate"
				page.Events = append(page.Events, &duplicate)
				return page
			},
		},
		{
			name:   "mismatched owner",
			status: session.RunStatusCompleted,
			list: func(page runlog.Page) runlog.Page {
				page = cloneCompletionPage(page)
				page.Events[len(page.Events)-1].AgentID = "other.agent"
				return page
			},
		},
		{
			name:   "mismatched status",
			status: session.RunStatusCompleted,
			loadRun: func(meta session.RunMeta) session.RunMeta {
				meta.Status = session.RunStatusFailed
				return meta
			},
		},
		{
			name:   "mismatched suspension checkpoint",
			status: session.RunStatusSuspended,
			loadSuspension: func(suspension session.RunSuspension) session.RunSuspension {
				suspension.ID = "other-suspension"
				return suspension
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			stored := newTestStore()
			meta := session.RunMeta{
				AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: test.status,
			}
			admitRunForTest(t, stored, meta)
			store := &completionReplayStore{
				Store: stored, list: test.list, loadRun: test.loadRun,
				loadSuspension: test.loadSuspension,
			}
			runtime := &Runtime{
				Store: store,
				Bus:   hooks.NewBus(),
				Engine: completionQueryEngine{
					completionErr: errors.New("engine history must not be queried"),
				},
			}

			err := runtime.EnsureRunCompletion(t.Context(), meta.RunID)
			require.ErrorIs(t, err, ErrRunCompletionCorrupt)
			require.Nil(t, store.terminalCommand)
			require.Nil(t, store.suspensionCommand)
		})
	}
}
