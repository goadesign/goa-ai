package inmem

// These tests prove that the in-memory reference store exposes lifecycle state
// and matching records as one change. Durable host implementations must satisfy
// the same retry, conflict, and ordering behavior.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	agent "goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRootStartPersistsOneImmutableOutcome(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)

	start := session.RunStart{AgentID: "agent", RunID: "run", SessionID: "session", StartedAt: now}
	command := rootStartCommand(t, start)
	first, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, first.Outcome)
	require.True(t, first.Started.Inserted)

	_, err = store.EndSession(ctx, "session", now.Add(time.Second))
	require.NoError(t, err)
	retry, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, retry.Outcome)
	require.False(t, retry.Started.Inserted)

	page, err := store.ListRunRecords(ctx, "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, hooks.RunStarted, page.Events[0].Type)
}

func TestRootStartRetryKeepsOriginalOutcomeAfterCompletion(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{AgentID: "agent", RunID: "run", SessionID: "session", StartedAt: now}
	command := rootStartCommand(t, start)
	_, err = store.StartRootRun(ctx, command)
	require.NoError(t, err)
	_, err = store.RecordRunTerminal(ctx, storage.RunTerminal{
		RunID: "run", Status: session.RunStatusCompleted,
		Record: completedRecord(t, "terminal", start, "success", nil),
	})
	require.NoError(t, err)
	retry, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, retry.Outcome)
	require.False(t, retry.Started.Inserted)
}

func TestStartValidatesBothPossibleLifecycleRecords(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{AgentID: "agent", RunID: "run", SessionID: "session", StartedAt: now}
	other := start
	other.RunID = "other-run"
	invalidCanceled := completedRecord(t, "terminal", other, "canceled", &run.Cancellation{Reason: run.CancellationReasonSessionEnded})
	_, err = store.StartRootRun(ctx, storage.RootRunStart{
		Run:      start,
		Started:  startedRecord(t, "started", start),
		Canceled: invalidCanceled,
	})
	require.ErrorIs(t, err, storage.ErrRunRecordOwnerMismatch)
	_, err = store.LoadRun(ctx, "run")
	require.ErrorIs(t, err, session.ErrRunNotFound)
}

func TestContinuationStartRequiresMatchingSuspendedPredecessor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		predecessor *session.RunMeta
		want        string
	}{
		{name: "missing", want: "run not found"},
		{
			name: "running",
			predecessor: &session.RunMeta{
				AgentID: "agent", RunID: "predecessor", SessionID: "session",
				Status: session.RunStatusRunning,
			},
			want: `has status "running", want "suspended"`,
		},
		{
			name: "completed",
			predecessor: &session.RunMeta{
				AgentID: "agent", RunID: "predecessor", SessionID: "session",
				Status: session.RunStatusCompleted,
			},
			want: `has status "completed", want "suspended"`,
		},
		{
			name: "session",
			predecessor: &session.RunMeta{
				AgentID: "agent", RunID: "predecessor", SessionID: "other-session",
				Status: session.RunStatusSuspended,
			},
			want: `session id "other-session" does not match successor "session"`,
		},
		{
			name: "agent",
			predecessor: &session.RunMeta{
				AgentID: "other-agent", RunID: "predecessor", SessionID: "session",
				Status: session.RunStatusSuspended,
			},
			want: `agent id "other-agent" does not match successor "agent"`,
		},
		{
			name: "parent",
			predecessor: &session.RunMeta{
				AgentID: "agent", RunID: "predecessor", SessionID: "session",
				ParentRunID: "other-parent", Status: session.RunStatusSuspended,
			},
			want: `parent run id "other-parent" does not match successor ""`,
		},
		{
			name: "valid",
			predecessor: &session.RunMeta{
				AgentID: "agent", RunID: "predecessor", SessionID: "session",
				Status: session.RunStatusSuspended,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			now := time.Now().UTC().Truncate(time.Millisecond)
			store := New()
			_, err := store.CreateSession(t.Context(), "session", now)
			require.NoError(t, err)
			if test.predecessor != nil {
				store.runs[test.predecessor.RunID] = *test.predecessor
			}
			start := session.RunStart{
				AgentID: "agent", RunID: "successor", SessionID: "session",
				PredecessorRunID: "predecessor", StartedAt: now,
			}

			_, err = store.StartRootRun(t.Context(), rootStartCommand(t, start))
			if test.want == "" {
				require.NoError(t, err)
				meta, loadErr := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, loadErr)
				require.Equal(t, session.RunStatusRunning, meta.Status)
				return
			}
			require.ErrorContains(t, err, test.want)
			var contractErr *storage.ContractError
			require.ErrorAs(t, err, &contractErr)
			_, loadErr := store.LoadRun(t.Context(), start.RunID)
			require.ErrorIs(t, loadErr, session.ErrRunNotFound)
			require.Empty(t, store.records[start.RunID])
		})
	}
}

func TestChildContinuationChecksPredecessorBeforeParentLink(t *testing.T) {
	t.Parallel()

	for _, status := range []session.RunStatus{
		session.RunStatusRunning,
		session.RunStatusSuspended,
	} {
		t.Run(string(status), func(t *testing.T) {
			t.Parallel()

			store, parent := runningRootStore(t)
			predecessor := session.RunMeta{
				AgentID: "child", RunID: "predecessor", SessionID: parent.SessionID,
				ParentRunID: parent.RunID, Status: status,
			}
			store.runs[predecessor.RunID] = predecessor
			child := session.RunStart{
				AgentID: "child", RunID: "successor", SessionID: parent.SessionID,
				ParentRunID: parent.RunID, PredecessorRunID: predecessor.RunID,
				StartedAt: parent.StartedAt,
			}
			command := storage.ChildRunStart{
				Run: child, ParentLinked: childLinkRecord(t, "child-link", parent, child),
				Started: startedRecord(t, "child-start", child),
				Canceled: completedRecord(t, "child-stop", child, "canceled", &run.Cancellation{
					Reason: run.CancellationReasonSessionEnded,
				}),
			}

			result, err := store.StartChildRun(t.Context(), command)
			page, pageErr := store.ListRunRecords(t.Context(), parent.RunID, "", 10)
			require.NoError(t, pageErr)
			if status == session.RunStatusSuspended {
				require.NoError(t, err)
				require.Equal(t, session.RunStartProceed, result.Outcome)
				require.Len(t, page.Events, 2)
				_, err = store.LoadRun(t.Context(), child.RunID)
				require.NoError(t, err)
				return
			}
			require.ErrorContains(t, err, `has status "running", want "suspended"`)
			require.Len(t, page.Events, 1)
			_, err = store.LoadRun(t.Context(), child.RunID)
			require.ErrorIs(t, err, session.ErrRunNotFound)
		})
	}
}

func TestRecordRetryRequiresExactTimestamp(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	start := session.RunStart{AgentID: "agent", RunID: "run", StartedAt: now}
	_, err := store.StartOneShotRun(ctx, storage.OneShotRunStart{Run: start, Started: startedRecord(t, "started", start)})
	require.NoError(t, err)
	record := record("note", "run", "agent", "", "note")
	_, err = store.AppendRunRecord(ctx, record)
	require.NoError(t, err)
	changed := *record
	changed.Timestamp = record.Timestamp.Add(time.Millisecond)
	_, err = store.AppendRunRecord(ctx, &changed)
	require.ErrorIs(t, err, runlog.ErrEventConflict)
}

func TestAppendRunRecordRejectsLifecycleRecord(t *testing.T) {
	store, start := runningRootStore(t)

	_, err := store.AppendRunRecord(
		t.Context(),
		completedRecord(t, "forged-terminal", start, "success", nil),
	)

	var contractErr *storage.ContractError
	require.ErrorAs(t, err, &contractErr)
	meta, err := store.LoadRun(t.Context(), start.RunID)
	require.NoError(t, err)
	require.Equal(t, session.RunStatusRunning, meta.Status)
}

func TestAppendRunRecordRejectsNewRecordsAfterRunStops(t *testing.T) {
	tests := []struct {
		name string
		stop func(*testing.T, *Store, session.RunStart)
	}{
		{
			name: "completed",
			stop: func(t *testing.T, store *Store, start session.RunStart) {
				_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
					RunID: start.RunID, Status: session.RunStatusCompleted,
					Record: completedRecord(t, "terminal", start, "success", nil),
				})
				require.NoError(t, err)
			},
		},
		{
			name: "failed",
			stop: func(t *testing.T, store *Store, start session.RunStart) {
				_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
					RunID: start.RunID, Status: session.RunStatusFailed,
					Record: completedRecord(t, "terminal", start, "failed", nil),
				})
				require.NoError(t, err)
			},
		},
		{
			name: "canceled",
			stop: func(t *testing.T, store *Store, start session.RunStart) {
				const reason = run.CancellationReasonUserRequested
				_, err := store.RecordRunCancellation(t.Context(), storage.RunCancellation{
					RunID: start.RunID, Reason: reason,
					Record: cancellationRecord(t, "cancel", start, reason),
				})
				require.NoError(t, err)
				_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
					RunID: start.RunID, Status: session.RunStatusCanceled,
					Record: completedRecord(t, "terminal", start, "canceled", &run.Cancellation{Reason: reason}),
				})
				require.NoError(t, err)
			},
		},
		{
			name: "suspended",
			stop: func(t *testing.T, store *Store, start session.RunStart) {
				_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{
					RunID: start.RunID,
					Suspension: session.RunSuspension{
						ID: "checkpoint", Data: []byte(`{"version":"v6"}`),
					},
					Record: suspendedRecord(t, "terminal", start, "checkpoint"),
				})
				require.NoError(t, err)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, start := runningRootStore(t)
			stored := record("ordinary", start.RunID, start.AgentID, start.SessionID, "ordinary")
			first, err := store.AppendRunRecord(t.Context(), stored)
			require.NoError(t, err)
			require.True(t, first.Inserted)
			test.stop(t, store, start)

			retry, err := store.AppendRunRecord(t.Context(), stored)
			require.NoError(t, err)
			require.False(t, retry.Inserted)
			require.Equal(t, first.ID, retry.ID)

			_, err = store.AppendRunRecord(
				t.Context(),
				record("late", start.RunID, start.AgentID, start.SessionID, "ordinary"),
			)
			require.ErrorIs(t, err, session.ErrRunNotActive)
			var contractErr *storage.ContractError
			require.ErrorAs(t, err, &contractErr)
		})
	}
}

func TestRecordListsRejectUnknownStateAndInvalidCursor(t *testing.T) {
	ctx := t.Context()
	store := New()
	_, err := store.ListRunRecords(ctx, "missing", "", 10)
	require.ErrorIs(t, err, session.ErrRunNotFound)
	_, err = store.ListSessionRunRecords(ctx, "missing", "", 10)
	require.ErrorIs(t, err, session.ErrSessionNotFound)
	_, err = store.ListRunsBySession(ctx, "missing", nil)
	require.ErrorIs(t, err, session.ErrSessionNotFound)

	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err = store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	_, err = store.ListSessionRunRecords(ctx, "session", "0", 10)
	require.ErrorContains(t, err, "invalid cursor")
	_, err = store.EndSession(ctx, "session", now.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, store.PurgeSession(ctx, "session"))
	_, err = store.ListSessionRunRecords(ctx, "session", "", 10)
	require.ErrorIs(t, err, session.ErrSessionPurged)
	_, err = store.ListRunsBySession(ctx, "session", nil)
	require.ErrorIs(t, err, session.ErrSessionPurged)
}

func TestEndedSessionStartStoresTerminalCancellation(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	_, err = store.EndSession(ctx, "session", now.Add(time.Second))
	require.NoError(t, err)

	start := session.RunStart{AgentID: "agent", RunID: "run", SessionID: "session", StartedAt: now}
	command := rootStartCommand(t, start)
	result, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.Equal(t, session.RunStartStop, result.Outcome)
	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCanceled, meta.Status)
	require.Equal(t, session.RunStartStop, meta.StartOutcome)
	require.NotEmpty(t, meta.CancellationReason)
	require.Equal(t, command.Started.EventKey, store.lifecycle[start.RunID].start)
	require.Equal(t, command.Canceled.EventKey, store.lifecycle[start.RunID].terminal)
	retry, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	require.False(t, retry.Started.Inserted)
	command.Canceled = completedRecord(t, "different-stopped", start, "canceled", &run.Cancellation{
		Reason: run.CancellationReasonSessionEnded,
	})
	_, err = store.StartRootRun(ctx, command)
	require.ErrorIs(t, err, session.ErrRunConflict)
	page, err := store.ListRunRecords(ctx, "run", "", 10)
	require.NoError(t, err)
	require.Equal(t, []runlog.Type{hooks.RunStarted, hooks.RunCompleted}, []runlog.Type{
		page.Events[0].Type,
		page.Events[1].Type,
	})
}

func TestChildStartStoresParentLinkAndChildStartTogether(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	parentStart := session.RunStart{AgentID: "parent", RunID: "parent", SessionID: "session", StartedAt: now}
	_, err = store.StartRootRun(ctx, rootStartCommand(t, parentStart))
	require.NoError(t, err)

	childStart := session.RunStart{
		AgentID: "child", RunID: "child", SessionID: "session", ParentRunID: "parent", StartedAt: now,
	}
	result, err := store.StartChildRun(ctx, storage.ChildRunStart{
		Run:          childStart,
		ParentLinked: childLinkRecord(t, "child-link", parentStart, childStart),
		Started:      startedRecord(t, "child-start", childStart),
		Canceled:     completedRecord(t, "child-terminal", childStart, "canceled", &run.Cancellation{Reason: run.CancellationReasonSessionEnded}),
	})
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, result.Outcome)
	require.True(t, result.ParentRecord.Inserted)
	require.True(t, result.Started.Inserted)
	parent, err := store.ListRunRecords(ctx, "parent", "", 10)
	require.NoError(t, err)
	require.Len(t, parent.Events, 2)
	child, err := store.ListRunRecords(ctx, "child", "", 10)
	require.NoError(t, err)
	require.Len(t, child.Events, 1)
}

func TestOneShotChildStartStoresParentLinkAndChildStartTogether(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	parent := session.RunStart{AgentID: "parent", RunID: "parent", StartedAt: now}
	_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{
		Run: parent, Started: startedRecord(t, "parent-start", parent),
	})
	require.NoError(t, err)
	child := session.RunStart{
		AgentID: "child", RunID: "child", ParentRunID: parent.RunID, StartedAt: now,
	}
	command := storage.OneShotChildRunStart{
		Run:          child,
		ParentLinked: childLinkRecord(t, "child-link", parent, child),
		Started:      startedRecord(t, "child-start", child),
	}

	result, err := store.StartOneShotChildRun(t.Context(), command)

	require.NoError(t, err)
	require.True(t, result.ParentRecord.Inserted)
	require.True(t, result.Started.Inserted)
	require.Empty(t, result.ParentRecord.SessionStatus)
	require.Empty(t, result.Started.SessionStatus)
	retry, err := store.StartOneShotChildRun(t.Context(), command)
	require.NoError(t, err)
	require.False(t, retry.ParentRecord.Inserted)
	require.False(t, retry.Started.Inserted)
	parentRecords, err := store.ListRunRecords(t.Context(), parent.RunID, "", 10)
	require.NoError(t, err)
	require.Equal(t, []runlog.Type{hooks.RunStarted, hooks.ChildRunLinked}, []runlog.Type{
		parentRecords.Events[0].Type, parentRecords.Events[1].Type,
	})
	childRecords, err := store.ListRunRecords(t.Context(), child.RunID, "", 10)
	require.NoError(t, err)
	require.Len(t, childRecords.Events, 1)
	require.Equal(t, hooks.RunStarted, childRecords.Events[0].Type)
}

func TestOneShotChildStartRequiresRunningSessionlessParent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	childFor := func(parent session.RunStart) storage.OneShotChildRunStart {
		child := session.RunStart{
			AgentID: "child", RunID: "child", ParentRunID: parent.RunID, StartedAt: now,
		}
		return storage.OneShotChildRunStart{
			Run: child, ParentLinked: childLinkRecord(t, "child-link", parent, child),
			Started: startedRecord(t, "child-start", child),
		}
	}
	t.Run("session parent", func(t *testing.T) {
		store := New()
		_, err := store.CreateSession(t.Context(), "session", now)
		require.NoError(t, err)
		parent := session.RunStart{AgentID: "parent", RunID: "parent", SessionID: "session", StartedAt: now}
		_, err = store.StartRootRun(t.Context(), rootStartCommand(t, parent))
		require.NoError(t, err)
		_, err = store.StartOneShotChildRun(t.Context(), childFor(parent))
		require.ErrorContains(t, err, "parent identity does not match child run")
	})
	t.Run("completed parent", func(t *testing.T) {
		store := New()
		parent := session.RunStart{AgentID: "parent", RunID: "parent", StartedAt: now}
		_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{
			Run: parent, Started: startedRecord(t, "parent-start", parent),
		})
		require.NoError(t, err)
		_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID: parent.RunID, Status: session.RunStatusCompleted,
			Record: completedRecord(t, "parent-complete", parent, "success", nil),
		})
		require.NoError(t, err)
		_, err = store.StartOneShotChildRun(t.Context(), childFor(parent))
		require.ErrorIs(t, err, session.ErrRunNotActive)
	})
}

func TestSessionChildStartRequiresRunningParent(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Millisecond)
	newStore := func(t *testing.T) (*Store, session.RunStart) {
		t.Helper()
		store := New()
		_, err := store.CreateSession(t.Context(), "session", now)
		require.NoError(t, err)
		parent := session.RunStart{
			AgentID: "parent", RunID: "parent", SessionID: "session", StartedAt: now,
		}
		_, err = store.StartRootRun(t.Context(), rootStartCommand(t, parent))
		require.NoError(t, err)
		return store, parent
	}
	childCommand := func(t *testing.T, parent session.RunStart) storage.ChildRunStart {
		t.Helper()
		child := session.RunStart{
			AgentID: "child", RunID: "child", SessionID: parent.SessionID,
			ParentRunID: parent.RunID, StartedAt: now,
		}
		return storage.ChildRunStart{
			Run: child, ParentLinked: childLinkRecord(t, "child-link", parent, child),
			Started: startedRecord(t, "child-start", child),
			Canceled: completedRecord(
				t, "child-stop", child, "canceled",
				&run.Cancellation{Reason: run.CancellationReasonSessionEnded},
			),
		}
	}

	t.Run("new child", func(t *testing.T) {
		store, parent := newStore(t)
		_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID: parent.RunID, Status: session.RunStatusCompleted,
			Record: completedRecord(t, "parent-complete", parent, "success", nil),
		})
		require.NoError(t, err)

		_, err = store.StartChildRun(t.Context(), childCommand(t, parent))
		require.ErrorIs(t, err, session.ErrRunNotActive)
	})

	t.Run("exact retry", func(t *testing.T) {
		store, parent := newStore(t)
		command := childCommand(t, parent)
		first, err := store.StartChildRun(t.Context(), command)
		require.NoError(t, err)
		_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID: parent.RunID, Status: session.RunStatusCompleted,
			Record: completedRecord(t, "parent-complete", parent, "success", nil),
		})
		require.NoError(t, err)

		retry, err := store.StartChildRun(t.Context(), command)
		require.NoError(t, err)
		require.False(t, retry.ParentRecord.Inserted)
		require.False(t, retry.Started.Inserted)
		require.Equal(t, first.ParentRecord.ID, retry.ParentRecord.ID)
		require.Equal(t, first.Started.ID, retry.Started.ID)
	})
}

func TestChildStartAfterPurgeReportsPurgedSession(t *testing.T) {
	ctx := t.Context()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	parent := session.RunStart{AgentID: "parent", RunID: "parent", SessionID: "session", StartedAt: now}
	_, err = store.StartRootRun(ctx, rootStartCommand(t, parent))
	require.NoError(t, err)
	_, err = store.RecordRunTerminal(ctx, storage.RunTerminal{
		RunID: parent.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "parent-terminal", parent, "success", nil),
	})
	require.NoError(t, err)
	_, err = store.EndSession(ctx, parent.SessionID, now.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, store.PurgeSession(ctx, parent.SessionID))

	child := session.RunStart{
		AgentID: "child", RunID: "child", SessionID: parent.SessionID,
		ParentRunID: parent.RunID, StartedAt: now.Add(2 * time.Second),
	}
	_, err = store.StartChildRun(ctx, storage.ChildRunStart{
		Run:          child,
		ParentLinked: childLinkRecord(t, "child-link", parent, child),
		Started:      startedRecord(t, "child-start", child),
		Canceled: completedRecord(t, "child-stop", child, "canceled", &run.Cancellation{
			Reason: run.CancellationReasonSessionEnded,
		}),
	})
	require.ErrorIs(t, err, session.ErrSessionPurged)
}

func TestRunRecordsMustMatchStoredRunOwner(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{AgentID: "agent", RunID: "run", SessionID: "session", StartedAt: now}
	_, err = store.StartRootRun(ctx, rootStartCommand(t, start))
	require.NoError(t, err)

	_, err = store.AppendRunRecord(ctx, record("wrong-agent", "run", "other", "session", "note"))
	require.ErrorIs(t, err, storage.ErrRunRecordOwnerMismatch)
	_, err = store.RecordRunTerminal(ctx, storage.RunTerminal{
		RunID: "run", Status: session.RunStatusCompleted,
		Record: completedRecord(t, "wrong-session", session.RunStart{
			AgentID: "agent", RunID: "run", SessionID: "other", StartedAt: now,
		}, "success", nil),
	})
	require.ErrorIs(t, err, storage.ErrRunRecordOwnerMismatch)
}

func TestOneShotCancellationAndTerminalAreRecorded(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	start := session.RunStart{AgentID: "agent", RunID: "run", StartedAt: now}
	_, err := store.StartOneShotRun(ctx, storage.OneShotRunStart{Run: start, Started: startedRecord(t, "started", start)})
	require.NoError(t, err)

	_, err = store.RecordRunCancellation(ctx, storage.RunCancellation{
		RunID: "run", Reason: run.CancellationReasonUserRequested,
		Record: cancellationRecord(t, "cancel", start, run.CancellationReasonUserRequested),
	})
	require.NoError(t, err)
	_, err = store.RecordRunCancellation(ctx, storage.RunCancellation{
		RunID: "run", Reason: run.CancellationReasonSessionEnded,
		Record: cancellationRecord(t, "cancel", start, run.CancellationReasonSessionEnded),
	})
	require.ErrorIs(t, err, session.ErrRunCancellationConflict)
	terminal := storage.RunTerminal{
		RunID: "run", Status: session.RunStatusCompleted,
		Record: completedRecord(t, "terminal", start, "success", nil),
	}
	result, err := store.RecordRunTerminal(ctx, terminal)
	require.NoError(t, err)
	require.True(t, result.Inserted)
	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusCompleted, meta.Status)
	updatedAt := meta.UpdatedAt
	time.Sleep(time.Millisecond)
	result, err = store.RecordRunTerminal(ctx, terminal)
	require.NoError(t, err)
	require.False(t, result.Inserted)
	meta, err = store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, updatedAt, meta.UpdatedAt)
}

func TestSuspensionCheckpointAndTerminalRecordAreAtomic(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(ctx, "session", now)
	require.NoError(t, err)
	start := session.RunStart{AgentID: "agent", RunID: "run", SessionID: "session", StartedAt: now}
	_, err = store.StartRootRun(ctx, rootStartCommand(t, start))
	require.NoError(t, err)

	suspension := storage.RunSuspension{
		RunID: "run", Suspension: session.RunSuspension{ID: "suspension", Data: []byte(`{"id":"suspension"}`)},
		Record: suspendedRecord(t, "terminal", start, "suspension"),
	}
	result, err := store.RecordRunSuspension(ctx, suspension)
	require.NoError(t, err)
	require.True(t, result.Inserted)
	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, session.RunStatusSuspended, meta.Status)
	updatedAt := meta.UpdatedAt
	checkpoint, err := store.LoadRunSuspension(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, "suspension", checkpoint.ID)
	time.Sleep(time.Millisecond)
	result, err = store.RecordRunSuspension(ctx, suspension)
	require.NoError(t, err)
	require.False(t, result.Inserted)
	meta, err = store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, updatedAt, meta.UpdatedAt)
}

func TestRepairRunTerminalKeepsWorkflowResult(t *testing.T) {
	store, start := runningRootStore(t)
	workflow := storage.RunTerminal{
		RunID: start.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "terminal", start, "success", nil),
	}
	_, err := store.RecordRunTerminal(t.Context(), workflow)
	require.NoError(t, err)
	repair := workflow
	record := *workflow.Record
	repair.Record = &record
	repair.Record.Timestamp = repair.Record.Timestamp.Add(time.Second)

	result, err := store.RepairRunTerminal(t.Context(), repair)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairDifferentTerminal, result.Outcome)
	require.Equal(t, session.RunStatusCompleted, result.Status)
	require.Equal(t, storage.AppendResult{}, result.Record)
	page, err := store.ListRunRecords(t.Context(), start.RunID, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, workflow.Record.Timestamp, page.Events[1].Timestamp)
}

func TestRepairRunTerminalRetryReportsExistingState(t *testing.T) {
	store, start := runningRootStore(t)
	command := storage.RunTerminal{
		RunID: start.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "terminal", start, "success", nil),
	}

	first, err := store.RepairRunTerminal(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairStored, first.Outcome)
	require.True(t, first.Record.Inserted)
	retry, err := store.RepairRunTerminal(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairAlreadyStored, retry.Outcome)
	require.Equal(t, session.RunStatusCompleted, retry.Status)
	require.Equal(t, first.Record.ID, retry.Record.ID)
	require.False(t, retry.Record.Inserted)
	require.Equal(t, first.Record.SessionStatus, retry.Record.SessionStatus)
}

func TestRepairRunSuspensionKeepsWorkflowResult(t *testing.T) {
	store, start := runningRootStore(t)
	workflow := storage.RunSuspension{
		RunID: start.RunID,
		Suspension: session.RunSuspension{
			ID: "suspension", Data: []byte(`{"version":"v6"}`),
		},
		Record: suspendedRecord(t, "terminal", start, "suspension"),
	}
	_, err := store.RecordRunSuspension(t.Context(), workflow)
	require.NoError(t, err)
	repair := workflow
	record := *workflow.Record
	repair.Record = &record
	repair.Record.Timestamp = repair.Record.Timestamp.Add(time.Second)

	result, err := store.RepairRunSuspension(t.Context(), repair)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairDifferentTerminal, result.Outcome)
	require.Equal(t, session.RunStatusSuspended, result.Status)
	require.Equal(t, storage.AppendResult{}, result.Record)
	page, err := store.ListRunRecords(t.Context(), start.RunID, "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 2)
	require.Equal(t, workflow.Record.Timestamp, page.Events[1].Timestamp)
}

func TestRepairRunSuspensionRetryReportsExistingState(t *testing.T) {
	store, start := runningRootStore(t)
	command := storage.RunSuspension{
		RunID: start.RunID,
		Suspension: session.RunSuspension{
			ID: "suspension", Data: []byte(`{"version":"v6"}`),
		},
		Record: suspendedRecord(t, "terminal", start, "suspension"),
	}

	first, err := store.RepairRunSuspension(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairStored, first.Outcome)
	require.True(t, first.Record.Inserted)
	retry, err := store.RepairRunSuspension(t.Context(), command)
	require.NoError(t, err)
	require.Equal(t, storage.RunRepairAlreadyStored, retry.Outcome)
	require.Equal(t, session.RunStatusSuspended, retry.Status)
	require.Equal(t, first.Record.ID, retry.Record.ID)
	require.False(t, retry.Record.Inserted)
	require.Equal(t, first.Record.SessionStatus, retry.Record.SessionStatus)
}

func TestLifecycleRetriesRequireOriginalRecordKeys(t *testing.T) {
	t.Run("root start", func(t *testing.T) {
		store, start := activeRunStore(t)
		command := rootStartCommand(t, start)
		_, err := store.StartRootRun(t.Context(), command)
		require.NoError(t, err)
		command.Started = startedRecord(t, "different-start", start)
		_, err = store.StartRootRun(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunConflict)
	})

	t.Run("cancellation", func(t *testing.T) {
		store, start := runningRootStore(t)
		command := storage.RunCancellation{
			RunID:  start.RunID,
			Reason: run.CancellationReasonUserRequested,
			Record: cancellationRecord(t, "cancel", start, run.CancellationReasonUserRequested),
		}
		_, err := store.RecordRunCancellation(t.Context(), command)
		require.NoError(t, err)
		command.Record = cancellationRecord(t, "different-cancel", start, command.Reason)
		_, err = store.RecordRunCancellation(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunCancellationConflict)
	})

	t.Run("suspension", func(t *testing.T) {
		store, start := runningRootStore(t)
		command := storage.RunSuspension{
			RunID:      start.RunID,
			Suspension: session.RunSuspension{ID: "suspension", Data: []byte(`{"version":"v6"}`)},
			Record:     suspendedRecord(t, "suspend", start, "suspension"),
		}
		_, err := store.RecordRunSuspension(t.Context(), command)
		require.NoError(t, err)
		command.Record = suspendedRecord(t, "different-suspend", start, "suspension")
		_, err = store.RecordRunSuspension(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunSuspensionConflict)
	})

	t.Run("terminal", func(t *testing.T) {
		store, start := runningRootStore(t)
		command := storage.RunTerminal{
			RunID:  start.RunID,
			Status: session.RunStatusCompleted,
			Record: completedRecord(t, "complete", start, "success", nil),
		}
		_, err := store.RecordRunTerminal(t.Context(), command)
		require.NoError(t, err)
		command.Record = completedRecord(t, "different-complete", start, "success", nil)
		_, err = store.RecordRunTerminal(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunTerminalConflict)
	})

	t.Run("child parent link", func(t *testing.T) {
		store, parent := runningRootStore(t)
		child := session.RunStart{
			AgentID: "child", RunID: "child", SessionID: parent.SessionID,
			ParentRunID: parent.RunID, StartedAt: parent.StartedAt,
		}
		command := storage.ChildRunStart{
			Run:          child,
			ParentLinked: childLinkRecord(t, "child-link", parent, child),
			Started:      startedRecord(t, "child-start", child),
			Canceled: completedRecord(t, "child-stop", child, "canceled", &run.Cancellation{
				Reason: run.CancellationReasonSessionEnded,
			}),
		}
		_, err := store.StartChildRun(t.Context(), command)
		require.NoError(t, err)
		command.ParentLinked = childLinkRecord(t, "different-link", parent, child)
		_, err = store.StartChildRun(t.Context(), command)
		require.ErrorIs(t, err, session.ErrRunConflict)
	})
}

func TestLifecycleCommandsRejectContradictoryTypedRecords(t *testing.T) {
	t.Run("start labels", func(t *testing.T) {
		store, start := activeRunStore(t)
		command := rootStartCommand(t, start)
		changed := start
		changed.Labels = map[string]string{"site": "other"}
		command.Started = startedRecord(t, "started", changed)
		_, err := store.StartRootRun(t.Context(), command)
		require.ErrorContains(t, err, "labels do not match run")
	})

	t.Run("cancellation reason", func(t *testing.T) {
		store, start := runningRootStore(t)
		_, err := store.RecordRunCancellation(t.Context(), storage.RunCancellation{
			RunID:  start.RunID,
			Reason: run.CancellationReasonUserRequested,
			Record: cancellationRecord(t, "cancel", start, run.CancellationReasonSessionEnded),
		})
		require.ErrorContains(t, err, "reason does not match command")
	})

	t.Run("suspension id", func(t *testing.T) {
		store, start := runningRootStore(t)
		_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{
			RunID:      start.RunID,
			Suspension: session.RunSuspension{ID: "expected", Data: []byte(`{"version":"v6"}`)},
			Record:     suspendedRecord(t, "suspend", start, "different"),
		})
		require.ErrorContains(t, err, "does not match checkpoint")
	})

	t.Run("terminal status", func(t *testing.T) {
		store, start := runningRootStore(t)
		_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID:  start.RunID,
			Status: session.RunStatusCompleted,
			Record: completedRecord(t, "terminal", start, "failed", nil),
		})
		require.ErrorContains(t, err, "outcome does not match command")
	})

	t.Run("terminal labels", func(t *testing.T) {
		store, start := runningRootStore(t)
		changed := start
		changed.Labels = map[string]string{"site": "other"}
		_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID:  start.RunID,
			Status: session.RunStatusCompleted,
			Record: completedRecord(t, "terminal", changed, "success", nil),
		})
		require.ErrorContains(t, err, "labels do not match run")
	})

	t.Run("terminal cancellation reason", func(t *testing.T) {
		store, start := runningRootStore(t)
		_, err := store.RecordRunCancellation(t.Context(), storage.RunCancellation{
			RunID:  start.RunID,
			Reason: run.CancellationReasonUserRequested,
			Record: cancellationRecord(t, "cancel", start, run.CancellationReasonUserRequested),
		})
		require.NoError(t, err)
		_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
			RunID:  start.RunID,
			Status: session.RunStatusCanceled,
			Record: completedRecord(t, "terminal", start, "canceled", &run.Cancellation{
				Reason: run.CancellationReasonEngineCanceled,
			}),
		})
		require.ErrorContains(t, err, "cancellation reason does not match run")
	})
}

// activeRunStore returns a store with one active session and an unpersisted
// root-run identity.
func activeRunStore(t *testing.T) (*Store, session.RunStart) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Millisecond)
	store := New()
	_, err := store.CreateSession(t.Context(), "session", now)
	require.NoError(t, err)
	return store, session.RunStart{
		AgentID: "agent", RunID: "run", SessionID: "session", StartedAt: now,
		Labels: map[string]string{"site": "one"},
	}
}

// runningRootStore starts the root identity returned with an active session.
func runningRootStore(t *testing.T) (*Store, session.RunStart) {
	t.Helper()
	store, start := activeRunStore(t)
	_, err := store.StartRootRun(t.Context(), rootStartCommand(t, start))
	require.NoError(t, err)
	return store, start
}

// rootStartCommand builds the two typed records whose selection depends on the
// session state observed by StartRootRun.
func rootStartCommand(t *testing.T, start session.RunStart) storage.RootRunStart {
	t.Helper()
	return storage.RootRunStart{
		Run:      start,
		Started:  startedRecord(t, "started", start),
		Canceled: completedRecord(t, "stopped", start, "canceled", &run.Cancellation{Reason: run.CancellationReasonSessionEnded}),
	}
}

// startedRecord encodes the immutable run identity with the hooks codec used
// by production workflows.
func startedRecord(t *testing.T, key string, start session.RunStart) *runlog.Event {
	t.Helper()
	return hookRecord(t, key, start.StartedAt, hooks.NewRunStartedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		start.ParentRunID,
		start.PredecessorRunID,
		start.Labels,
	))
}

// completedRecord encodes one final lifecycle outcome for a run.
func completedRecord(t *testing.T, key string, start session.RunStart, status string, cancellation *run.Cancellation) *runlog.Event {
	t.Helper()
	phase := run.PhaseCompleted
	var eventErr error
	switch status {
	case "failed":
		phase = run.PhaseFailed
		eventErr = errors.New("failed")
	case "canceled":
		phase = run.PhaseCanceled
		eventErr = context.Canceled
	}
	return hookRecord(t, key, start.StartedAt, hooks.NewRunCompletedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		status,
		phase,
		start.Labels,
		eventErr,
		cancellation,
	))
}

// childLinkRecord encodes the parent-owned record that names the exact child
// run accepted by StartChildRun.
func childLinkRecord(t *testing.T, key string, parent, child session.RunStart) *runlog.Event {
	t.Helper()
	return hookRecord(t, key, child.StartedAt, hooks.NewChildRunLinkedEvent(
		parent.RunID,
		agent.Ident(parent.AgentID),
		parent.SessionID,
		tools.Ident("child"),
		"tool-call",
		child.RunID,
		agent.Ident(child.AgentID),
	))
}

// suspendedRecord encodes the terminal event that makes one checkpoint
// available for continuation.
func suspendedRecord(t *testing.T, key string, start session.RunStart, suspensionID string) *runlog.Event {
	t.Helper()
	return hookRecord(t, key, start.StartedAt, hooks.NewRunSuspendedEvent(
		start.RunID,
		agent.Ident(start.AgentID),
		start.SessionID,
		suspensionID,
		"v6",
		1,
		nil,
	))
}

// cancellationRecord builds the runtime-owned record that preserves the first
// accepted cancellation reason.
func cancellationRecord(t *testing.T, key string, start session.RunStart, reason string) *runlog.Event {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"reason": reason})
	require.NoError(t, err)
	return &runlog.Event{
		EventKey:  key,
		RunID:     start.RunID,
		AgentID:   agent.Ident(start.AgentID),
		SessionID: start.SessionID,
		Type:      storage.CancellationRecordType,
		Payload:   payload,
		Timestamp: start.StartedAt,
	}
}

// hookRecord converts a typed hook event into the immutable storage envelope
// accepted by the integrated store.
func hookRecord(t *testing.T, key string, at time.Time, event hooks.Event) *runlog.Event {
	t.Helper()
	input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{EventKey: key, TimestampMS: at.UnixMilli()})
	require.NoError(t, err)
	return &runlog.Event{
		EventKey:  input.EventKey,
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   input.Payload,
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	}
}

func record(key, runID, agentID, sessionID string, typ runlog.Type) *runlog.Event {
	return &runlog.Event{
		EventKey: key, RunID: runID, AgentID: agent.Ident(agentID), SessionID: sessionID,
		Type: typ, Payload: []byte(`{}`), Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	}
}
