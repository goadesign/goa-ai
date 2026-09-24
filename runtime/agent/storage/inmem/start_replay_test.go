package inmem

// Start retries return the current run state while preserving the original
// decision and record IDs. No retry may replace a terminal result.

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type startReplayResult struct {
	outcome   session.RunStartOutcome
	runStatus session.RunStatus
	records   []storage.AppendResult
}

func TestStartReplayReturnsCurrentRunStatus(t *testing.T) {
	for _, kind := range []string{"root", "child", "one shot", "one shot child"} {
		for _, status := range []session.RunStatus{
			session.RunStatusRunning, session.RunStatusSuspended, session.RunStatusCompleted,
			session.RunStatusFailed, session.RunStatusCanceled,
		} {
			t.Run(kind+"/"+string(status), func(t *testing.T) {
				store, start := activeRunStore(t)
				retry := replayStartOperation(t, store, start, kind)
				first := retry()
				assert.Equal(t, session.RunStatusRunning, first.runStatus)
				assert.Equal(t, session.RunStartProceed, first.outcome)
				for _, record := range first.records {
					assert.True(t, record.Inserted)
				}
				if status != session.RunStatusRunning {
					closeReplayRun(t, store, start, status)
				}
				before, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				page, err := store.ListRunRecords(t.Context(), start.RunID, "", 100)
				require.NoError(t, err)

				result := retry()

				assert.Equal(t, status, result.runStatus)
				assert.Equal(t, first.outcome, result.outcome)
				require.Len(t, result.records, len(first.records))
				for i, record := range result.records {
					assert.Equal(t, first.records[i].ID, record.ID)
					assert.False(t, record.Inserted)
				}
				after, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				assert.Equal(t, before, after)
				afterPage, err := store.ListRunRecords(t.Context(), start.RunID, "", 100)
				require.NoError(t, err)
				assert.Equal(t, page, afterPage)
				if status == session.RunStatusSuspended {
					checkpoint, err := store.LoadRunSuspension(t.Context(), start.RunID)
					require.NoError(t, err)
					assert.JSONEq(t, `{"version":"v6"}`, string(checkpoint.Data))
				}
			})
		}
	}
}

// replayStartOperation freezes one complete command for the selected start.
// Its parent, when present, stays running so the child's status is unambiguous.
func replayStartOperation(t *testing.T, store *Store, start session.RunStart, kind string) func() startReplayResult {
	t.Helper()
	if kind == "one shot" || kind == "one shot child" {
		start.SessionID = ""
	}
	start = publishStartHistory(t, store, start)
	parent := start
	parent.RunID = "parent"
	if kind == "child" || kind == "one shot child" {
		parent = publishStartHistory(t, store, parent)
		if start.SessionID == "" {
			_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{
				Run: parent, Started: startedRecord(t, "parent-start", parent),
			})
			require.NoError(t, err)
		} else {
			_, err := store.StartRootRun(t.Context(), rootStartCommand(t, parent))
			require.NoError(t, err)
		}
		start.ParentRunID = parent.RunID
	}
	root := rootStartCommand(t, start)
	switch kind {
	case "root":
		return func() startReplayResult {
			result, err := store.StartRootRun(t.Context(), root)
			require.NoError(t, err)
			return startReplayResult{result.Outcome, result.RunStatus, []storage.AppendResult{result.Started}}
		}
	case "child":
		command := storage.ChildRunStart{
			Run: start, ParentLinked: childLinkRecord(t, "link", parent, start),
			Started: root.Started, Canceled: root.Canceled,
		}
		return func() startReplayResult {
			result, err := store.StartChildRun(t.Context(), command)
			require.NoError(t, err)
			return startReplayResult{
				result.Outcome, result.RunStatus, []storage.AppendResult{result.ParentRecord, result.Started},
			}
		}
	case "one shot":
		command := storage.OneShotRunStart{Run: start, Started: root.Started}
		return func() startReplayResult {
			result, err := store.StartOneShotRun(t.Context(), command)
			require.NoError(t, err)
			return startReplayResult{session.RunStartProceed, result.RunStatus, []storage.AppendResult{result.Record}}
		}
	default:
		command := storage.OneShotChildRunStart{
			Run: start, ParentLinked: childLinkRecord(t, "link", parent, start), Started: root.Started,
		}
		return func() startReplayResult {
			result, err := store.StartOneShotChildRun(t.Context(), command)
			require.NoError(t, err)
			return startReplayResult{
				session.RunStartProceed, result.RunStatus, []storage.AppendResult{result.ParentRecord, result.Started},
			}
		}
	}
}

// closeReplayRun uses lifecycle writes to close the stored run, including its
// actual Session and parent identity.
func closeReplayRun(t *testing.T, store *Store, start session.RunStart, status session.RunStatus) {
	t.Helper()
	meta, err := store.LoadRun(t.Context(), start.RunID)
	require.NoError(t, err)
	start.SessionID, start.ParentRunID = meta.SessionID, meta.ParentRunID
	if status == session.RunStatusSuspended {
		_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{
			RunID:      start.RunID,
			Suspension: session.RunSuspension{ID: "checkpoint", Data: []byte(`{"version":"v6"}`)},
			Record:     suspendedRecord(t, "terminal", start, "checkpoint"),
		})
		require.NoError(t, err)
		return
	}
	recordStatus := string(status)
	if status == session.RunStatusCompleted {
		recordStatus = "success"
	}
	var cancellation *run.Cancellation
	if status == session.RunStatusCanceled {
		cancellation = &run.Cancellation{Reason: run.CancellationReasonUserRequested}
		_, err := store.RecordRunCancellation(t.Context(), storage.RunCancellation{
			RunID: start.RunID, Reason: cancellation.Reason,
			Record: cancellationRecord(t, "cancel", start, cancellation.Reason),
		})
		require.NoError(t, err)
	}
	_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: start.RunID, Status: status,
		Record: completedRecord(t, "terminal", start, recordStatus, cancellation),
	})
	require.NoError(t, err)
}
