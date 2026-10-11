// These tests prove that suspension closure preserves history and competes
// atomically with real continuation admission and session deletion.
package inmem

import (
	"crypto/sha256"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestRelinquishSuspensionPreservesHistoryAndRetry(t *testing.T) {
	for _, ended := range []bool{false, true} {
		t.Run(map[bool]string{false: "active session", true: "ended session"}[ended], func(t *testing.T) {
			store, root := runningRootStore(t)
			saved := session.RunSuspension{ID: "checkpoint", Data: []byte(`{"saved":"original immutable bytes"}`)}
			_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{RunID: root.RunID, Suspension: saved, Record: suspendedRecord(t, "suspended", root, saved.ID)})
			require.NoError(t, err)
			before, err := store.ListRunRecords(t.Context(), root.RunID, "", 100)
			require.NoError(t, err)
			if ended {
				_, err = store.EndSession(t.Context(), root.SessionID, time.Now().UTC())
				require.NoError(t, err)
				require.ErrorIs(t, store.PurgeSession(t.Context(), root.SessionID), session.ErrSessionHasActiveRuns)
			}
			command := storage.RunSuspensionRelinquishment{RunID: root.RunID, SuspensionID: saved.ID, Reason: "user_requested"}
			first, err := store.RelinquishRunSuspension(t.Context(), command)
			require.NoError(t, err)
			closed, ok := first.(session.RelinquishedSuspension)
			require.True(t, ok)
			assert.Equal(t, command.Reason, closed.Reason)
			assert.False(t, closed.ResolvedAt.IsZero())
			retried, err := store.RelinquishRunSuspension(t.Context(), command)
			require.NoError(t, err)
			assert.Equal(t, first, retried)
			command.Reason = "session_ended"
			_, err = store.RelinquishRunSuspension(t.Context(), command)
			require.ErrorIs(t, err, session.ErrRunCancellationConflict)
			after, err := store.ListRunRecords(t.Context(), root.RunID, "", 100)
			require.NoError(t, err)
			assert.Equal(t, before, after)
			checkpoint, err := store.LoadRunSuspension(t.Context(), root.RunID)
			require.NoError(t, err)
			assert.Equal(t, sha256.Sum256(saved.Data), sha256.Sum256(checkpoint.Data))
			meta, err := store.LoadRun(t.Context(), root.RunID)
			require.NoError(t, err)
			assert.Equal(t, session.RunStatusSuspended, meta.Status)
			assert.Empty(t, meta.CancellationReason)
			assert.Equal(t, first, meta.SuspensionResolution)
			if ended {
				require.NoError(t, store.PurgeSession(t.Context(), root.SessionID))
				_, err = store.RelinquishRunSuspension(t.Context(), command)
				require.ErrorIs(t, err, session.ErrRunNotFound)
			}
		})
	}
}

func TestRelinquishSuspensionCompetesWithContinuation(t *testing.T) {
	for _, order := range []string{"closure first", "continuation first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			store, root := runningRootStore(t)
			_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{RunID: root.RunID, Suspension: session.RunSuspension{ID: "checkpoint", Data: []byte(`{}`)}, Record: suspendedRecord(t, "suspended", root, "checkpoint")})
			require.NoError(t, err)
			successor := publishStartHistory(t, store, session.RunStart{AgentID: root.AgentID, RunID: "successor", SessionID: root.SessionID, PredecessorRunID: root.RunID, StartedAt: root.StartedAt})
			command := rootStartCommand(t, successor)
			closure := storage.RunSuspensionRelinquishment{RunID: root.RunID, SuspensionID: "checkpoint", Reason: "user_requested"}
			var resolution session.SuspensionResolution
			var closeErr, startErr error
			closeRun := func() { resolution, closeErr = store.RelinquishRunSuspension(t.Context(), closure) }
			startRun := func() { _, startErr = store.StartRootRun(t.Context(), command) }
			switch order {
			case "closure first":
				closeRun()
				startRun()
			case "continuation first":
				startRun()
				closeRun()
			case "concurrent":
				var workers sync.WaitGroup
				workers.Go(closeRun)
				workers.Go(startRun)
				workers.Wait()
			}
			require.NoError(t, closeErr)
			meta, err := store.LoadRun(t.Context(), root.RunID)
			require.NoError(t, err)
			assert.Equal(t, resolution, meta.SuspensionResolution)
			switch resolution.(type) {
			case session.RelinquishedSuspension:
				require.ErrorIs(t, startErr, session.ErrRunConflict)
				_, err := store.LoadRun(t.Context(), successor.RunID)
				require.ErrorIs(t, err, session.ErrRunNotFound)
				page, err := store.ListRunRecords(t.Context(), successor.RunID, "", 100)
				require.ErrorIs(t, err, session.ErrRunNotFound)
				assert.Empty(t, page.Events)
			case session.ContinuedSuspension:
				require.NoError(t, startErr)
				assert.Equal(t, session.ContinuedSuspension{RunID: successor.RunID}, resolution)
				_, err := store.StartRootRun(t.Context(), command)
				require.NoError(t, err)
			default:
				t.Fatal("missing exclusive resolution")
			}
		})
	}
}

func TestRelinquishSuspensionRejectsWrongOwnerOrCheckpoint(t *testing.T) {
	for _, variation := range []string{"running", "wrong checkpoint", "child", "empty reason"} {
		t.Run(variation, func(t *testing.T) {
			store, root := runningRootStore(t)
			if variation == "child" {
				child := publishStartHistory(t, store, session.RunStart{AgentID: "child.agent", RunID: "child", SessionID: root.SessionID, ParentRunID: root.RunID, StartedAt: root.StartedAt})
				_, err := store.StartChildRun(t.Context(), storage.ChildRunStart{
					RequestDigest: [32]byte{1}, Run: child,
					ParentLinked: childLinkRecord(t, "linked", root, child),
					Started:      startedRecord(t, "started", child),
					Cancellation: cancellationRecord(t, "canceled", child, "session_ended"),
				})
				require.NoError(t, err)
				root = child
			}
			if variation != "running" {
				_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{RunID: root.RunID, Suspension: session.RunSuspension{ID: "checkpoint", Data: []byte(`{}`)}, Record: suspendedRecord(t, "suspended", root, "checkpoint")})
				require.NoError(t, err)
			}
			command := storage.RunSuspensionRelinquishment{RunID: root.RunID, SuspensionID: "checkpoint", Reason: "user_requested"}
			switch variation {
			case "wrong checkpoint":
				command.SuspensionID = "different"
			case "empty reason":
				command.Reason = ""
			}
			_, err := store.RelinquishRunSuspension(t.Context(), command)
			require.Error(t, err)
			assert.Nil(t, store.runs[root.RunID].SuspensionResolution)
		})
	}
}
