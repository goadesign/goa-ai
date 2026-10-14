// Standalone child admission claims one successor under the same lock that
// writes its parent link and start record. Exact retries preserve those records.
package inmem

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestOneShotChildContinuationClaimsOneSuccessor(t *testing.T) {
	store := New()
	parent := publishStartHistory(t, store, session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("parent")}, RunID: "parent", StartedAt: time.Now().UTC().Truncate(time.Millisecond)}, "")
	root := rootStartCommand(t, parent)
	_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart(root))
	require.NoError(t, err)
	child := oneShotChildContinuationCommand(t, store, parent, "child", "", "call")
	_, err = store.StartOneShotChildRun(t.Context(), child)
	require.NoError(t, err)
	_, err = store.RecordRunSuspension(t.Context(), storage.RunSuspension{RunID: child.Run.RunID, Suspension: session.RunSuspension{ID: "saved", Data: []byte(`{"state":"saved"}`)}, Record: suspendedRecord(t, "suspended", child.Run, "saved")})
	require.NoError(t, err)
	candidates := []storage.OneShotChildRunStart{
		oneShotChildContinuationCommand(t, store, parent, "first", child.Run.RunID, "call"),
		oneShotChildContinuationCommand(t, store, parent, "second", child.Run.RunID, "call"),
	}
	outcomes := make(chan error, 2)
	ready := make(chan struct{})
	for _, candidate := range candidates {
		go func() {
			<-ready
			_, err := store.StartOneShotChildRun(t.Context(), candidate)
			outcomes <- err
		}()
	}
	close(ready)
	a, b := <-outcomes, <-outcomes
	assert.NotEqual(t, a == nil, b == nil)
	if a != nil {
		assert.ErrorIs(t, a, session.ErrRunConflict) //nolint:testifylint // The other race result and saved winner must still be checked after this error.
	}
	if b != nil {
		assert.ErrorIs(t, b, session.ErrRunConflict) //nolint:testifylint // The saved winner must still be checked after this race error.
	}
	previous, err := store.LoadRun(t.Context(), child.Run.RunID)
	require.NoError(t, err)
	var winner storage.OneShotChildRunStart
	for _, candidate := range candidates {
		if candidate.Run.RunID == previous.SuspensionResolution.(session.ContinuedSuspension).RunID {
			winner = candidate
		} else {
			_, err := store.LoadRun(t.Context(), candidate.Run.RunID)
			assert.ErrorIs(t, err, session.ErrRunNotFound) //nolint:testifylint // Other candidates and the saved winner are checked independently of this losing Run lookup.
		}
	}
	require.NotEmpty(t, winner.Run.RunID)
	current, err := store.LoadRun(t.Context(), winner.Run.RunID)
	require.NoError(t, err)
	assert.Equal(t, previous.Origin, current.Origin)
	before, err := store.ListRunRecords(t.Context(), parent.RunID, "", 100)
	require.NoError(t, err)
	retried, err := store.StartOneShotChildRun(t.Context(), winner)
	require.NoError(t, err)
	assert.False(t, retried.ParentRecord.Inserted)
	assert.False(t, retried.Started.Inserted)
	after, err := store.ListRunRecords(t.Context(), parent.RunID, "", 100)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	wrong := oneShotChildContinuationCommand(t, store, parent, "wrong", child.Run.RunID, "different-call")
	_, err = store.StartOneShotChildRun(t.Context(), wrong)
	assert.ErrorContains(t, err, "does not match predecessor parent tool call") //nolint:testifylint // The winner completion and exact replay checks do not use the mismatched-call result.
	_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{RunID: winner.Run.RunID, Status: session.RunStatusCompleted, Record: completedRecord(t, "completed", winner.Run, "success", nil)})
	require.NoError(t, err)
	retried, err = store.StartOneShotChildRun(t.Context(), winner)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCompleted, retried.RunStatus)
}

// oneShotChildContinuationCommand publishes exact child history and encodes the
// original parent call in the two records supplied to atomic start admission.
func oneShotChildContinuationCommand(t *testing.T, store *Store, parent session.RunStart, runID, predecessor, callID string) storage.OneShotChildRunStart {
	t.Helper()
	start := publishStartHistory(t, store, session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("child")}, RunID: runID, ParentRunID: parent.RunID, PredecessorRunID: predecessor, StartedAt: parent.StartedAt}, callID)
	return storage.OneShotChildRunStart{RequestDigest: [32]byte{1}, Run: start, ParentLinked: hookRecord(t, "link-"+runID, start.StartedAt, hooks.NewChildRunLinkedEvent(parent.RunID, parent.Subject, "", "child", callID, runID, "child")), Started: startedRecord(t, "start", start)}
}
