package inmem

// These tests submit canonical start records with a later execution time.
// Only the same accepted request may select a closed run's original records.

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

const (
	requestKindRoot         = "root"
	requestKindChild        = "child"
	requestKindOneShot      = "one shot"
	requestKindOneShotChild = "one shot child"
)

func TestClosedRequestSelectsOriginalStart(t *testing.T) {
	for _, kind := range []string{requestKindRoot, requestKindChild, requestKindOneShot, requestKindOneShotChild} {
		for _, status := range []session.RunStatus{
			session.RunStatusSuspended, session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled,
		} {
			t.Run(kind+"/"+string(status), func(t *testing.T) {
				store, start := requestStartFixture(t, kind)
				first, err := applyRequestStart(t, store, kind, start, [32]byte{1})
				require.NoError(t, err)
				closeReplayRun(t, store, start, status)
				meta, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				records := requestRecordSnapshot(store)
				later := start
				later.StartedAt = start.StartedAt.Add(time.Hour)
				result, err := applyRequestStart(t, store, kind, later, [32]byte{1})
				require.NoError(t, err)
				assert.Equal(t, status, result.runStatus)
				assert.Equal(t, first.outcome, result.outcome)
				require.Len(t, result.records, len(first.records))
				for i, record := range result.records {
					assert.Equal(t, first.records[i].ID, record.ID)
					assert.False(t, record.Inserted)
				}
				after, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				assert.Equal(t, meta, after)
				assert.Equal(t, records, requestRecordSnapshot(store))

				_, err = applyRequestStart(t, store, kind, later, [32]byte{2})
				require.ErrorIs(t, err, session.ErrRunConflict)
				_, err = applyRequestStart(t, store, kind, later, [32]byte{})
				require.ErrorContains(t, err, "digest is required")
				later.Labels = map[string]string{"site": "different"}
				_, err = applyRequestStart(t, store, kind, later, [32]byte{1})
				require.ErrorIs(t, err, session.ErrRunConflict)
				assert.Equal(t, records, requestRecordSnapshot(store))
			})
		}
	}
}

func TestClosedRequestRejectsMissingOrCorruptProof(t *testing.T) {
	for _, name := range []string{
		"binding", "start missing", "start key", "start time", "start payload",
		"start id", "closing missing", "closing payload", "closing identity",
		"checkpoint missing", "checkpoint identity", "parent link missing", "parent identity",
	} {
		t.Run(name, func(t *testing.T) {
			kind := requestKindRoot
			if name == "parent link missing" || name == "parent identity" {
				kind = requestKindChild
			}
			store, start := requestStartFixture(t, kind)
			_, err := applyRequestStart(t, store, kind, start, [32]byte{1})
			require.NoError(t, err)
			status := session.RunStatusCompleted
			if name == "checkpoint missing" || name == "checkpoint identity" {
				status = session.RunStatusSuspended
			}
			closeReplayRun(t, store, start, status)
			// These changes model corrupted saved data at the Store read boundary.
			keys := store.lifecycle[start.RunID]
			switch name {
			case "binding":
				keys.requestDigest = [32]byte{}
				store.lifecycle[start.RunID] = keys
			case "start missing":
				delete(store.recordsByKey[start.RunID], keys.start)
			case "start key":
				store.recordsByKey[start.RunID][keys.start].EventKey = "wrong"
			case "start time":
				store.recordsByKey[start.RunID][keys.start].Timestamp = start.StartedAt.Add(time.Second)
			case "start payload":
				store.recordsByKey[start.RunID][keys.start].Payload = []byte(`{}`)
			case "start id":
				store.recordsByKey[start.RunID][keys.start].ID = ""
			case "closing missing":
				delete(store.recordsByKey[start.RunID], keys.terminal)
			case "closing payload":
				store.recordsByKey[start.RunID][keys.terminal].Payload = []byte(`{}`)
			case "closing identity":
				store.recordsByKey[start.RunID][keys.terminal].RunID = "wrong"
			case "checkpoint missing":
				delete(store.suspensions, start.RunID)
			case "checkpoint identity":
				store.suspensions[start.RunID] = session.RunSuspension{ID: "different", Data: []byte(`{}`)}
			case "parent link missing":
				delete(store.recordsByKey[start.ParentRunID], keys.parentLink)
			case "parent identity":
				parent := store.runs[start.ParentRunID]
				parent.AgentID = "different"
				store.runs[start.ParentRunID] = parent
			}
			before := requestRecordSnapshot(store)
			start.StartedAt = start.StartedAt.Add(time.Hour)
			_, err = applyRequestStart(t, store, kind, start, [32]byte{1})
			require.Error(t, err)
			var contractErr *storage.ContractError
			require.ErrorAs(t, err, &contractErr)
			assert.Equal(t, before, requestRecordSnapshot(store))
		})
	}
}

func TestRequestBindingDoesNotResumeRunningRun(t *testing.T) {
	for _, kind := range []string{requestKindRoot, requestKindChild, requestKindOneShot, requestKindOneShotChild} {
		t.Run(kind, func(t *testing.T) {
			store, start := requestStartFixture(t, kind)
			first, err := applyRequestStart(t, store, kind, start, [32]byte{1})
			require.NoError(t, err)
			retry, err := applyRequestStart(t, store, kind, start, [32]byte{1})
			require.NoError(t, err)
			assert.Equal(t, first.records[0].ID, retry.records[0].ID)
			before := requestRecordSnapshot(store)
			later := start
			later.StartedAt = start.StartedAt.Add(time.Second)
			_, err = applyRequestStart(t, store, kind, later, [32]byte{1})
			require.ErrorIs(t, err, session.ErrRunConflict)
			_, err = applyRequestStart(t, store, kind, start, [32]byte{2})
			require.ErrorIs(t, err, session.ErrRunConflict)
			assert.Equal(t, before, requestRecordSnapshot(store))
			start.RunID = "next-run"
			start = publishStartHistory(t, store, start)
			_, err = applyRequestStart(t, store, kind, start, [32]byte{2})
			require.NoError(t, err)
		})
	}
}

func TestClosedRequestKeepsEndedSessionDecision(t *testing.T) {
	for _, kind := range []string{requestKindRoot, requestKindChild} {
		t.Run(kind, func(t *testing.T) {
			store, start := requestStartFixture(t, kind)
			_, err := store.EndSession(t.Context(), start.SessionID, start.StartedAt.Add(time.Second))
			require.NoError(t, err)
			first, err := applyRequestStart(t, store, kind, start, [32]byte{1})
			require.NoError(t, err)
			require.Equal(t, session.RunStartStop, first.outcome)
			before := requestRecordSnapshot(store)
			start.StartedAt = start.StartedAt.Add(time.Hour)
			retry, err := applyRequestStart(t, store, kind, start, [32]byte{1})
			require.NoError(t, err)
			assert.Equal(t, first.outcome, retry.outcome)
			assert.Equal(t, session.RunStatusCanceled, retry.runStatus)
			require.Len(t, retry.records, len(first.records))
			for i, record := range retry.records {
				assert.Equal(t, first.records[i].ID, record.ID)
				assert.False(t, record.Inserted)
			}
			assert.Equal(t, before, requestRecordSnapshot(store))
		})
	}
}

func TestClosedRequestContendsWithClosure(t *testing.T) {
	store, start := requestStartFixture(t, requestKindRoot)
	_, err := applyRequestStart(t, store, requestKindRoot, start, [32]byte{1})
	require.NoError(t, err)
	later := start
	later.StartedAt = start.StartedAt.Add(time.Second)
	release := make(chan struct{})
	var group sync.WaitGroup
	var startErr, closeErr error
	var result startReplayResult
	terminal := completedRecord(t, "terminal", start, "success", nil)
	command := rootStartCommand(t, later)
	group.Add(2)
	go func() {
		defer group.Done()
		<-release
		stored, err := store.StartRootRun(context.Background(), command)
		startErr = err
		result = startReplayResult{stored.Outcome, stored.RunStatus, []storage.AppendResult{stored.Started}}
	}()
	go func() {
		defer group.Done()
		<-release
		_, closeErr = store.RecordRunTerminal(context.Background(), storage.RunTerminal{
			RunID: start.RunID, Status: session.RunStatusCompleted, Record: terminal,
		})
	}()
	close(release)
	group.Wait()
	require.NoError(t, closeErr)
	if startErr != nil {
		require.ErrorIs(t, startErr, session.ErrRunConflict)
	} else {
		assert.Equal(t, session.RunStatusCompleted, result.runStatus)
	}
	meta, err := store.LoadRun(t.Context(), start.RunID)
	require.NoError(t, err)
	assert.Equal(t, start.StartedAt, meta.StartedAt)
	require.Len(t, requestRecordSnapshot(store)[start.RunID], 2)
}

// requestStartFixture creates the Session and running parent required by the
// chosen operation. The tested run itself has not been created.
func requestStartFixture(t *testing.T, kind string) (*Store, session.RunStart) {
	t.Helper()
	store, start := activeRunStore(t)
	if kind == requestKindOneShot || kind == requestKindOneShotChild {
		start.SessionID = ""
	}
	if kind == requestKindChild || kind == requestKindOneShotChild {
		parent := start
		parent.RunID = "parent"
		parentKind := requestKindRoot
		if start.SessionID == "" {
			parentKind = requestKindOneShot
		}
		parent = publishStartHistory(t, store, parent)
		_, err := applyRequestStart(t, store, parentKind, parent, [32]byte{3})
		require.NoError(t, err)
		start.ParentRunID = parent.RunID
	}
	return store, publishStartHistory(t, store, start)
}

// applyRequestStart encodes the same event contracts as workflow code. Only the
// test-supplied request digest and immutable input distinguish each submission.
func applyRequestStart(t *testing.T, store *Store, kind string, start session.RunStart, digest [32]byte) (startReplayResult, error) {
	t.Helper()
	root := rootStartCommand(t, start)
	root.RequestDigest = digest
	parent := start
	parent.RunID, parent.ParentRunID = start.ParentRunID, ""
	switch kind {
	case requestKindRoot:
		result, err := store.StartRootRun(t.Context(), root)
		records := []storage.AppendResult{result.Started}
		if result.Outcome == session.RunStartStop {
			records = append(records, result.Canceled)
		}
		return startReplayResult{result.Outcome, result.RunStatus, records}, err
	case requestKindChild:
		result, err := store.StartChildRun(t.Context(), storage.ChildRunStart{
			RequestDigest: digest, Run: start, Started: root.Started, Canceled: root.Canceled,
			ParentLinked: childLinkRecord(t, "link-"+start.RunID, parent, start),
		})
		records := []storage.AppendResult{result.ParentRecord, result.Started}
		if result.Outcome == session.RunStartStop {
			records = append(records, result.Canceled)
		}
		return startReplayResult{result.Outcome, result.RunStatus, records}, err
	case requestKindOneShot:
		result, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{
			RequestDigest: digest, Run: start, Started: root.Started,
		})
		return startReplayResult{session.RunStartProceed, result.RunStatus, []storage.AppendResult{result.Record}}, err
	default:
		result, err := store.StartOneShotChildRun(t.Context(), storage.OneShotChildRunStart{
			RequestDigest: digest, Run: start, Started: root.Started,
			ParentLinked: childLinkRecord(t, "link-"+start.RunID, parent, start),
		})
		return startReplayResult{session.RunStartProceed, result.RunStatus, []storage.AppendResult{result.ParentRecord, result.Started}}, err
	}
}

// requestRecordSnapshot copies all synthetic records so mutation checks include
// the parent's event list as well as the child's.
func requestRecordSnapshot(store *Store) map[string][]*runlog.Event {
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make(map[string][]*runlog.Event)
	for id, records := range store.records {
		for _, record := range records {
			result[id] = append(result[id], cloneEvent(record))
		}
	}
	return result
}
