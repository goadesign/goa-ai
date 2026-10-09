// Continuation admission tests start competing workflows against one suspended
// predecessor. Only one start may publish records; retries and unrelated child
// calls retain their independent identities.
package inmem

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestContinuationAdmissionSelectsOneSuccessor(t *testing.T) {
	store, predecessor := runningRootStore(t)
	_, err := store.RecordRunSuspension(t.Context(), storage.RunSuspension{
		RunID:      predecessor.RunID,
		Suspension: session.RunSuspension{ID: "checkpoint", Data: []byte(`{"state":"saved"}`)},
		Record:     suspendedRecord(t, "suspended", predecessor, "checkpoint"),
	})
	require.NoError(t, err)
	commands := make([]storage.RootRunStart, 0, 8)
	for i := range 8 {
		start := publishStartHistory(t, store, session.RunStart{
			AgentID: predecessor.AgentID, RunID: fmt.Sprintf("successor-%d", i),
			SessionID: predecessor.SessionID, PredecessorRunID: predecessor.RunID,
			StartedAt: predecessor.StartedAt,
		})
		commands = append(commands, rootStartCommand(t, start))
	}
	type outcome struct {
		result storage.RootRunStartResult
		err    error
	}
	outcomes := make([]outcome, len(commands))
	ready := make(chan struct{})
	var workers sync.WaitGroup
	for i, command := range commands {
		workers.Go(func() {
			<-ready
			outcomes[i].result, outcomes[i].err = store.StartRootRun(t.Context(), command)
		})
	}
	close(ready)
	workers.Wait()
	var winner *storage.RootRunStart
	var selected storage.RootRunStartResult
	for i, current := range outcomes {
		if current.err == nil {
			require.Nil(t, winner, "a second continuation must not be admitted")
			winner = &commands[i]
			selected = current.result
			continue
		}
		require.ErrorIs(t, current.err, session.ErrRunConflict)
		_, err := store.LoadRun(t.Context(), commands[i].Run.RunID)
		require.ErrorIs(t, err, session.ErrRunNotFound)
	}
	require.NotNil(t, winner)
	meta, err := store.LoadRun(t.Context(), predecessor.RunID)
	require.NoError(t, err)
	assert.Equal(t, winner.Run.RunID, meta.SuccessorRunID)
	assert.Equal(t, session.RunStatusSuspended, meta.Status)
	before, err := store.ListSessionRunRecords(t.Context(), predecessor.SessionID, "", 100)
	require.NoError(t, err)
	retried, err := store.StartRootRun(t.Context(), *winner)
	require.NoError(t, err)
	assert.Equal(t, selected.Started.ID, retried.Started.ID)
	assert.False(t, retried.Started.Inserted)
	_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{
		RunID: winner.Run.RunID, Status: session.RunStatusCompleted,
		Record: completedRecord(t, "completed", winner.Run, "success", nil),
	})
	require.NoError(t, err)
	retried, err = store.StartRootRun(t.Context(), *winner)
	require.NoError(t, err)
	assert.Equal(t, session.RunStatusCompleted, retried.RunStatus)
	assert.Equal(t, selected.Started.ID, retried.Started.ID)
	for _, command := range commands {
		if command.Run.RunID != winner.Run.RunID {
			_, err := store.StartRootRun(t.Context(), command)
			require.ErrorIs(t, err, session.ErrRunConflict)
		}
	}
	after, err := store.ListSessionRunRecords(t.Context(), predecessor.SessionID, "", 100)
	require.NoError(t, err)
	assert.Len(t, after.Events, len(before.Events)+1, "only completion may add a record after admission")
}
