package inmem

// Synchronous callbacks reuse one exact prepared start. These tests verify
// command retries and permanent separation from engine-owned Run identities.

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestSynchronousStartPreservesExactCommand(t *testing.T) {
	for _, status := range []session.RunStatus{
		session.RunStatusRunning, session.RunStatusSuspended, session.RunStatusCompleted,
		session.RunStatusFailed, session.RunStatusCanceled,
	} {
		t.Run(string(status), func(t *testing.T) {
			store, start := requestStartFixture(t, requestKindOneShot)
			command := storage.SynchronousRunStart{Run: start, Started: startedRecord(t, "start", start)}
			first, err := store.StartSynchronousRun(t.Context(), command)
			require.NoError(t, err)
			assert.True(t, first.Record.Inserted)
			if status != session.RunStatusRunning {
				closeReplayRun(t, store, start, status)
			}
			before := requestRecordSnapshot(store)
			meta, err := store.LoadRun(t.Context(), start.RunID)
			require.NoError(t, err)

			retry, err := store.StartSynchronousRun(t.Context(), command)
			require.NoError(t, err)
			assert.Equal(t, status, retry.RunStatus)
			assert.Equal(t, first.Record.ID, retry.Record.ID)
			assert.False(t, retry.Record.Inserted)

			for _, changed := range []string{"time", "labels", "agent", "seed", "event key", "turn"} {
				t.Run(changed, func(t *testing.T) {
					proposed := start
					if changed == "time" {
						proposed.StartedAt = proposed.StartedAt.Add(time.Millisecond)
					}
					if changed == "labels" {
						proposed.Labels = map[string]string{"different": "metadata"}
					}
					if changed == "agent" {
						proposed.AgentID = "other-agent"
					}
					if changed == "seed" {
						proposed.SeedEndID = "99"
					}
					record := startedRecord(t, "start", proposed)
					if changed == "event key" {
						record.EventKey = "another-event-key"
					}
					if changed == "turn" {
						record.TurnID = "another-turn"
					}
					_, err := store.StartSynchronousRun(t.Context(), storage.SynchronousRunStart{
						Run: proposed, Started: record,
					})
					var contractErr *storage.ContractError
					require.ErrorAs(t, err, &contractErr)
					assert.Equal(t, before, requestRecordSnapshot(store))
				})
			}
			after, err := store.LoadRun(t.Context(), start.RunID)
			require.NoError(t, err)
			assert.Equal(t, meta, after)
		})
	}
}

func TestSynchronousAndEngineStartIdentitiesCannotMix(t *testing.T) {
	for _, firstKind := range []string{"synchronous", "engine"} {
		for _, closed := range []bool{false, true} {
			name := firstKind + "/running"
			if closed {
				name = firstKind + "/closed"
			}
			t.Run(name, func(t *testing.T) {
				store, start := requestStartFixture(t, requestKindOneShot)
				record := startedRecord(t, "start", start)
				synchronous := storage.SynchronousRunStart{Run: start, Started: record}
				engine := storage.OneShotRunStart{RequestDigest: [32]byte{1}, Run: start, Started: record}
				if firstKind == "synchronous" {
					_, err := store.StartSynchronousRun(t.Context(), synchronous)
					require.NoError(t, err)
				} else {
					_, err := store.StartOneShotRun(t.Context(), engine)
					require.NoError(t, err)
				}
				if closed {
					closeReplayRun(t, store, start, session.RunStatusCompleted)
				}
				before := requestRecordSnapshot(store)
				var err error
				if firstKind == "synchronous" {
					_, err = store.StartOneShotRun(t.Context(), engine)
				} else {
					_, err = store.StartSynchronousRun(t.Context(), synchronous)
				}
				require.ErrorIs(t, err, session.ErrRunConflict)
				assert.Equal(t, before, requestRecordSnapshot(store))
			})
		}
	}
}

func TestSynchronousAndEngineStartsContendAtomically(t *testing.T) {
	store, start := requestStartFixture(t, requestKindOneShot)
	record := startedRecord(t, "start", start)
	synchronous := storage.SynchronousRunStart{Run: start, Started: record}
	engine := storage.OneShotRunStart{RequestDigest: [32]byte{1}, Run: start, Started: record}
	release := make(chan struct{})
	var group sync.WaitGroup
	var syncErr, engineErr error
	group.Add(2)
	go func() {
		defer group.Done()
		<-release
		_, syncErr = store.StartSynchronousRun(t.Context(), synchronous)
	}()
	go func() {
		defer group.Done()
		<-release
		_, engineErr = store.StartOneShotRun(t.Context(), engine)
	}()
	close(release)
	group.Wait()
	if syncErr == nil {
		require.ErrorIs(t, engineErr, session.ErrRunConflict)
	} else {
		require.ErrorIs(t, syncErr, session.ErrRunConflict)
		require.NoError(t, engineErr)
	}
	page, err := store.ListRunRecords(t.Context(), start.RunID, "", 10)
	require.NoError(t, err)
	assert.Len(t, page.Events, 1)
}

func TestStartRejectsMissingOrCorruptExecutionOwner(t *testing.T) {
	for _, kind := range []runStartKind{0, 99, synchronousRunStart} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			store, start := requestStartFixture(t, requestKindOneShot)
			command := storage.OneShotRunStart{
				RequestDigest: [32]byte{1}, Run: start, Started: startedRecord(t, "start", start),
			}
			_, err := store.StartOneShotRun(t.Context(), command)
			require.NoError(t, err)
			// Corrupt retained identity at the Store boundary. The last case
			// combines a synchronous kind with an illegal engine digest.
			keys := store.lifecycle[start.RunID]
			keys.startKind = kind
			store.lifecycle[start.RunID] = keys
			before := requestRecordSnapshot(store)
			_, err = store.StartOneShotRun(t.Context(), command)
			require.Error(t, err)
			_, err = store.StartSynchronousRun(t.Context(), storage.SynchronousRunStart{
				Run: start, Started: command.Started,
			})
			require.Error(t, err)
			assert.Equal(t, before, requestRecordSnapshot(store))
		})
	}
}
