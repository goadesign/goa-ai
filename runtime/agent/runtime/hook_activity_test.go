package runtime

// These tests cover the runtime activity's translation from workflow records
// to the unified storage commands. Store-level retry and conflict behavior is
// covered by runtime/agent/storage/inmem.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	// cancellationRecordConflictStore returns a stored-record conflict from the
	// cancellation command while delegating every other operation.
	cancellationRecordConflictStore struct {
		storage.Store
	}

	// startSessionStatusStore returns selected start results without applying a
	// write so tests can exercise the runtime's store-result checks.
	startSessionStatusStore struct {
		storage.Store
		root    storage.RootRunStartResult
		child   storage.ChildRunStartResult
		oneShot storage.OneShotRunStartResult
	}
)

func (s cancellationRecordConflictStore) RecordRunCancellation(context.Context, storage.RunCancellation) (storage.AppendResult, error) {
	return storage.AppendResult{}, storage.NewContractError(runlog.ErrEventConflict)
}

func (s startSessionStatusStore) StartRootRun(context.Context, storage.RootRunStart) (storage.RootRunStartResult, error) {
	return s.root, nil
}

func (s startSessionStatusStore) StartChildRun(context.Context, storage.ChildRunStart) (storage.ChildRunStartResult, error) {
	return s.child, nil
}

func (s startSessionStatusStore) StartOneShotRun(context.Context, storage.OneShotRunStart) (storage.OneShotRunStartResult, error) {
	return s.oneShot, nil
}

func TestRecordActivityStoresRootStartDecision(t *testing.T) {
	for _, test := range []struct {
		name    string
		end     bool
		outcome session.RunStartOutcome
		status  session.RunStatus
		type_   string
	}{
		{name: "active", outcome: session.RunStartProceed, status: session.RunStatusRunning, type_: string(hooks.RunStarted)},
		{name: "ended", end: true, outcome: session.RunStartStop, status: session.RunStatusCanceled, type_: string(hooks.RunStarted)},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore()
			_, err := store.CreateSession(ctx, "session", time.Now().UTC())
			require.NoError(t, err)
			if test.end {
				_, err = store.EndSession(ctx, "session", time.Now().UTC())
				require.NoError(t, err)
			}
			runtime := &Runtime{Store: store, Bus: hooks.NewBus()}
			event := hooks.NewRunStartedEvent(
				"run", agent.Ident("svc.agent"), "session", "", "", map[string]string{"site": "one"},
			)
			record, err := prepareHookRecordInput(ctx, event, "turn")
			require.NoError(t, err)

			output, err := runtime.executeStorageCommand(ctx, &api.StorageActivityCommand{
				RootStart: &api.RootRunStartCommand{Started: record},
			})
			require.NoError(t, err)
			start := output.RootStart
			require.Equal(t, test.outcome, start.Outcome)
			if test.end {
				require.Equal(t, run.CancellationReasonSessionEnded, start.CancellationReason)
			} else {
				require.Empty(t, start.CancellationReason)
			}
			meta, err := store.LoadRun(ctx, "run")
			require.NoError(t, err)
			require.Equal(t, test.status, meta.Status)
			page, err := store.ListRunRecords(ctx, "run", "", 10)
			require.NoError(t, err)
			wantRecords := 1
			if test.end {
				wantRecords = 2
			}
			require.Len(t, page.Events, wantRecords)
			require.Equal(t, test.type_, string(page.Events[0].Type))
			if test.end {
				require.Equal(t, string(hooks.RunCompleted), string(page.Events[1].Type))
			}
		})
	}
}

func TestRecordActivityStoresRenderedPromptsOnlyForStartedRun(t *testing.T) {
	for _, test := range []struct {
		name       string
		endSession bool
		wantTypes  []string
	}{
		{name: "active", wantTypes: []string{string(hooks.RunStarted), string(hooks.PromptRendered)}},
		{name: "ended", endSession: true, wantTypes: []string{string(hooks.RunStarted), string(hooks.RunCompleted)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			store := newTestStore()
			_, err := store.CreateSession(ctx, "session", time.Now().UTC())
			require.NoError(t, err)
			if test.endSession {
				_, err = store.EndSession(ctx, "session", time.Now().UTC())
				require.NoError(t, err)
			}
			runtime := &Runtime{Store: store, Bus: hooks.NewBus()}
			started, err := prepareHookRecordInput(ctx, hooks.NewRunStartedEvent(
				"run",
				agent.Ident("svc.agent"),
				"session",
				"",
				"",
				nil,
			), "turn")
			require.NoError(t, err)
			rendered, err := prepareHookRecordInput(ctx, hooks.NewPromptRenderedEvent(
				"run",
				agent.Ident("svc.agent"),
				"session",
				prompt.Ident("svc.agent.system"),
				"v1",
				prompt.Scope{SessionID: "session"},
			), "turn")
			require.NoError(t, err)

			output, err := runtime.executeStorageCommand(ctx, &api.StorageActivityCommand{
				RootStart: &api.RootRunStartCommand{Started: started},
			})
			require.NoError(t, err)
			wantStartRecords := 1
			if output.RootStart.Outcome == session.RunStartStop {
				wantStartRecords = 2
			}
			require.Len(t, output.RootStart.Records, wantStartRecords)
			if output.RootStart.Outcome == session.RunStartProceed {
				promptOutput, err := runtime.executeStorageCommand(ctx, testAppendCommand(rendered))
				require.NoError(t, err)
				require.Len(t, promptOutput.Append.Records, 1)
			}
			page, err := store.ListRunRecords(ctx, "run", "", 10)
			require.NoError(t, err)
			require.Len(t, page.Events, len(test.wantTypes))
			for index, want := range test.wantTypes {
				require.Equal(t, want, string(page.Events[index].Type))
			}
		})
	}
}

func TestRecordActivityStartsOneShotRun(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	runtime := &Runtime{Store: store, Bus: hooks.NewBus()}
	event := hooks.NewRunStartedEvent("run", agent.Ident("svc.agent"), "", "", "", nil)
	record, err := prepareHookRecordInput(ctx, event, "")
	require.NoError(t, err)

	output, err := runtime.executeStorageCommand(ctx, &api.StorageActivityCommand{
		OneShotStart: &api.OneShotRunStartCommand{Started: record},
	})
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, output.OneShotStart.Outcome)
	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Empty(t, meta.SessionID)
}

func TestRecordActivityStartsOneShotChildRun(t *testing.T) {
	store := newTestStore()
	runtime := &Runtime{Store: store, Bus: hooks.NewBus()}
	parentEvent := hooks.NewRunStartedEvent("parent", agent.Ident("parent.agent"), "", "", "", nil)
	parentRecord, err := prepareHookRecordInput(t.Context(), parentEvent, "turn")
	require.NoError(t, err)
	_, err = runtime.executeStorageCommand(t.Context(), &api.StorageActivityCommand{
		OneShotStart: &api.OneShotRunStartCommand{Started: parentRecord},
	})
	require.NoError(t, err)
	childEvent := hooks.NewRunStartedEvent(
		"child", agent.Ident("child.agent"), "", "parent", "", nil,
	)
	childRecord, err := prepareHookRecordInput(t.Context(), childEvent, "turn")
	require.NoError(t, err)
	linkedRecord, err := prepareHookRecordInput(t.Context(), hooks.NewChildRunLinkedEvent(
		"parent",
		agent.Ident("parent.agent"),
		"",
		tools.Ident("parent.tools.child"),
		"call",
		"child",
		agent.Ident("child.agent"),
	), "turn")
	require.NoError(t, err)

	output, err := runtime.executeStorageCommand(t.Context(), &api.StorageActivityCommand{
		OneShotChildStart: &api.OneShotChildRunStartCommand{
			ParentLinked: linkedRecord,
			Started:      childRecord,
		},
	})

	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, output.OneShotChildStart.Outcome)
	require.Len(t, output.OneShotChildStart.Records, 2)
	for _, record := range output.OneShotChildStart.Records {
		require.Empty(t, record.SessionStatus)
	}
	child, err := store.LoadRun(t.Context(), "child")
	require.NoError(t, err)
	require.Empty(t, child.SessionID)
	require.Equal(t, "parent", child.ParentRunID)
}

func TestRecordActivityRejectsStartRecordsWithWrongSessionStatus(t *testing.T) {
	active := storage.AppendResult{SessionStatus: session.StatusActive}
	ended := storage.AppendResult{SessionStatus: session.StatusEnded}
	for _, test := range []struct {
		name  string
		kind  storageCommandKind
		store startSessionStatusStore
		want  string
	}{
		{
			name: "root proceed invalid status",
			kind: storageCommandRootStart,
			store: startSessionStatusStore{root: storage.RootRunStartResult{
				Outcome: session.RunStartProceed,
				Started: storage.AppendResult{Inserted: true, SessionStatus: session.StatusEnded},
			}},
			want: `newly inserted proceeding start record 0 reports ended session`,
		},
		{
			name: "root stop",
			kind: storageCommandRootStart,
			store: startSessionStatusStore{root: storage.RootRunStartResult{
				Outcome:  session.RunStartStop,
				Started:  ended,
				Canceled: active,
			}},
			want: `start record 1 has session status "active", want all records to report "ended"`,
		},
		{
			name: "child proceed parent link",
			kind: storageCommandChildStart,
			store: startSessionStatusStore{child: storage.ChildRunStartResult{
				Outcome:      session.RunStartProceed,
				ParentRecord: ended,
				Started:      active,
			}},
			want: `start record 1 has session status "active", want all records to report "ended"`,
		},
		{
			name: "child stop start",
			kind: storageCommandChildStart,
			store: startSessionStatusStore{child: storage.ChildRunStartResult{
				Outcome:      session.RunStartStop,
				ParentRecord: ended,
				Started:      active,
				Canceled:     ended,
			}},
			want: `start record 1 has session status "active", want all records to report "ended"`,
		},
		{
			name: "child stop cancellation",
			kind: storageCommandChildStart,
			store: startSessionStatusStore{child: storage.ChildRunStartResult{
				Outcome:      session.RunStartStop,
				ParentRecord: ended,
				Started:      ended,
				Canceled:     active,
			}},
			want: `start record 2 has session status "active", want all records to report "ended"`,
		},
		{
			name: "one shot",
			kind: storageCommandOneShotStart,
			store: startSessionStatusStore{oneShot: storage.OneShotRunStartResult{
				Record: active,
			}},
			want: `one-shot start has session status "active", want empty`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := hooks.NewRunStartedEvent(
				"run", agent.Ident("svc.agent"), "session", "", "", nil,
			)
			if test.kind == storageCommandChildStart {
				started = hooks.NewRunStartedEvent(
					"run", agent.Ident("svc.agent"), "session", "parent", "", nil,
				)
			}
			if test.kind == storageCommandOneShotStart {
				started = hooks.NewRunStartedEvent(
					"run", agent.Ident("svc.agent"), "", "", "", nil,
				)
			}
			startedInput, err := prepareHookRecordInput(t.Context(), started, "turn")
			require.NoError(t, err)
			var linkedInput *RecordActivityInput
			if test.kind == storageCommandChildStart {
				linkedInput, err = prepareHookRecordInput(t.Context(), hooks.NewChildRunLinkedEvent(
					"parent",
					agent.Ident("svc.parent"),
					"session",
					tools.Ident("svc.parent.child"),
					"call",
					"run",
					agent.Ident("svc.agent"),
				), "turn")
				require.NoError(t, err)
			}
			runtime := &Runtime{Store: test.store, Bus: hooks.NewBus()}

			_, err = runtime.storeRunStart(t.Context(), test.kind, startedInput, linkedInput)

			require.ErrorContains(t, err, test.want)
			require.True(t, engine.IsActivityErrorNonRetryable(err))
		})
	}
}

func TestRecordActivityAcceptsProceedRetryAfterSessionEnds(t *testing.T) {
	store := newTestStore()
	_, err := store.CreateSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)
	runtime := &Runtime{Store: store, Bus: hooks.NewBus()}
	started, err := prepareHookRecordInput(t.Context(), hooks.NewRunStartedEvent(
		"run", agent.Ident("svc.agent"), "session", "", "", nil,
	), "turn")
	require.NoError(t, err)
	command := &api.StorageActivityCommand{
		RootStart: &api.RootRunStartCommand{Started: started},
	}

	first, err := runtime.executeStorageCommand(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, first.RootStart.Outcome)
	require.Equal(t, session.StatusActive, first.RootStart.Records[0].SessionStatus)
	_, err = store.EndSession(t.Context(), "session", time.Now().UTC())
	require.NoError(t, err)

	retry, err := runtime.executeStorageCommand(t.Context(), command)

	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, retry.RootStart.Outcome)
	require.Equal(t, session.StatusEnded, retry.RootStart.Records[0].SessionStatus)
}

func TestRecordActivityStoresFirstCancellationReason(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		AgentID: "svc.agent", RunID: "run", SessionID: "session", Status: session.RunStatusRunning,
	})
	runtime := &Runtime{Store: store, Bus: hooks.NewBus()}
	record := func(reason string) *RecordActivityInput {
		payload, err := json.Marshal(cancellationIntentPayload{Reason: reason})
		require.NoError(t, err)
		return &RecordActivityInput{
			Type: storage.CancellationRecordType, EventKey: cancellationIntentEventKey,
			RunID: "run", AgentID: agent.Ident("svc.agent"), SessionID: "session",
			TimestampMS: time.Now().UnixMilli(), Payload: payload,
		}
	}

	firstRecord := record(run.CancellationReasonUserRequested)
	first, err := runtime.executeStorageCommand(ctx, &api.StorageActivityCommand{
		Cancellation: &api.RunCancellationCommand{Record: firstRecord},
	})
	require.NoError(t, err)
	require.Equal(t, api.RunCancellationAccepted, first.Cancellation.Outcome)
	require.True(t, first.Cancellation.Record.Inserted)

	retry, err := runtime.executeStorageCommand(ctx, &api.StorageActivityCommand{
		Cancellation: &api.RunCancellationCommand{Record: firstRecord},
	})
	require.NoError(t, err)
	require.Equal(t, api.RunCancellationAccepted, retry.Cancellation.Outcome)
	require.False(t, retry.Cancellation.Record.Inserted)

	conflict, err := runtime.executeStorageCommand(ctx, &api.StorageActivityCommand{
		Cancellation: &api.RunCancellationCommand{Record: record(run.CancellationReasonSessionEnded)},
	})
	require.NoError(t, err)
	require.Equal(t, api.RunCancellationConflict, conflict.Cancellation.Outcome)

	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, run.CancellationReasonUserRequested, meta.CancellationReason)
}

func TestRecordActivityPreservesCancellationRecordConflict(t *testing.T) {
	ctx := context.Background()
	store := newTestStore()
	runtime := &Runtime{
		Store: cancellationRecordConflictStore{Store: store},
		Bus:   hooks.NewBus(),
	}
	payload, err := json.Marshal(cancellationIntentPayload{Reason: run.CancellationReasonUserRequested})
	require.NoError(t, err)

	output, err := runtime.executeStorageCommand(ctx, &api.StorageActivityCommand{
		Cancellation: &api.RunCancellationCommand{Record: &RecordActivityInput{
			Type: storage.CancellationRecordType, EventKey: cancellationIntentEventKey,
			RunID: "run", AgentID: agent.Ident("svc.agent"), SessionID: "session",
			TimestampMS: time.Now().UnixMilli(), Payload: payload,
		}},
	})

	require.Nil(t, output)
	require.ErrorIs(t, err, runlog.ErrEventConflict)
	require.True(t, engine.IsActivityErrorNonRetryable(err))
}

func TestClassifyStorageActivityErrorRetriesOnlyTemporaryFailures(t *testing.T) {
	permanent := classifyStorageActivityError(fmt.Errorf(
		"store start: %w",
		storage.NewContractError(session.ErrSessionPurged),
	))
	require.ErrorIs(t, permanent, session.ErrSessionPurged)
	require.True(t, engine.IsActivityErrorNonRetryable(permanent))

	temporary := errors.New("database unavailable")
	require.Same(t, temporary, classifyStorageActivityError(temporary))
	require.False(t, engine.IsActivityErrorNonRetryable(temporary))
}

func TestStorageActivityRejectsCommandsWithoutExactlyOneOperation(t *testing.T) {
	runtime := &Runtime{Store: newTestStore(), Bus: hooks.NewBus()}
	for _, command := range []*api.StorageActivityCommand{
		{},
		{
			Append:   &api.AppendRecordsCommand{Records: []*RecordActivityInput{{}}},
			Terminal: &api.RunTerminalCommand{Record: &RecordActivityInput{}},
		},
	} {
		output, err := runtime.executeStorageCommand(t.Context(), command)
		require.Nil(t, output)
		require.ErrorContains(t, err, "exactly one operation")
		require.True(t, engine.IsActivityErrorNonRetryable(err))
	}
}

func TestMalformedStorageCommandIsNotAStoreContractError(t *testing.T) {
	runtime := &Runtime{Store: newTestStore(), Bus: hooks.NewBus()}
	for _, test := range []struct {
		name    string
		command *api.StorageActivityCommand
		want    string
	}{
		{
			name:    "empty append",
			command: &api.StorageActivityCommand{Append: &api.AppendRecordsCommand{}},
			want:    "append command is empty",
		},
		{
			name: "nil record",
			command: &api.StorageActivityCommand{Append: &api.AppendRecordsCommand{
				Records: []*RecordActivityInput{nil},
			}},
			want: "record input is nil",
		},
		{
			name: "malformed transcript",
			command: &api.StorageActivityCommand{Append: &api.AppendRecordsCommand{
				Records: []*RecordActivityInput{{
					Type: transcript.RunLogMessagesAppended, Payload: []byte(`{"messages":`),
				}},
			}},
			want: "decode transcript delta",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := runtime.executeStorageCommand(t.Context(), test.command)
			require.ErrorContains(t, err, test.want)
			require.True(t, engine.IsActivityErrorNonRetryable(err))
			var contractErr *storage.ContractError
			require.NotErrorAs(t, err, &contractErr)
		})
	}
}

func TestStorageActivityRejectsUnknownStartOutcome(t *testing.T) {
	result := &api.StorageActivityResult{
		RootStart: &api.StartRunResult{Outcome: session.RunStartOutcome("unknown")},
	}
	require.ErrorContains(t, validateStorageResult(storageCommandRootStart, result), "unknown start outcome")
}

func TestStorageActivityRejectsMismatchedResult(t *testing.T) {
	result := &api.StorageActivityResult{Append: &api.AppendRecordsResult{}}
	require.ErrorContains(t, validateStorageResult(storageCommandTerminal, result), "does not match command")
}
