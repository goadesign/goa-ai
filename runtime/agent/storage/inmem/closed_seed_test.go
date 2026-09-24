package inmem

// Closed-request selection keeps the published history checks used by a fresh
// start. A matching engine digest never authorizes missing or different history.

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestClosedRequestRequiresExactPublishedHistory(t *testing.T) {
	for _, kind := range []string{requestKindRoot, requestKindChild, requestKindOneShot, requestKindOneShotChild} {
		for _, change := range []string{"position", "missing", "unpublished", "owner", "predecessor"} {
			t.Run(kind+"/"+change, func(t *testing.T) {
				store, start := requestStartFixture(t, kind)
				_, err := applyRequestStart(t, store, kind, start, [32]byte{1})
				require.NoError(t, err)
				closeReplayRun(t, store, start, session.RunStatusCompleted)
				before := requestRecordSnapshot(store)
				meta, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				later := start
				later.StartedAt = start.StartedAt.Add(time.Hour)
				// Missing or corrupt publications are Store read-boundary cases.
				// The candidate's own position is checked without altering storage.
				switch change {
				case "position":
					later.SeedEndID = "99"
				case "missing":
					delete(store.seeds, start.RunID)
				case "unpublished":
					store.seeds[start.RunID].published = false
				case "owner":
					store.seeds[start.RunID].seed.Declaration.AgentID = "another-agent"
				case "predecessor":
					store.seeds[start.RunID].seed.Declaration.Kind = storage.SeedContinuation
					store.seeds[start.RunID].seed.Declaration.SourceRunID = "another-run"
				}
				_, err = applyRequestStart(t, store, kind, later, [32]byte{1})
				var rejected *storage.ContractError
				require.ErrorAs(t, err, &rejected)
				after, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				assert.Equal(t, meta, after)
				assert.Equal(t, before, requestRecordSnapshot(store))
			})
		}
	}
}

func TestClosedRequestCannotRecreatePurgedPublication(t *testing.T) {
	store, start := requestStartFixture(t, requestKindRoot)
	_, err := applyRequestStart(t, store, requestKindRoot, start, [32]byte{1})
	require.NoError(t, err)
	closeReplayRun(t, store, start, session.RunStatusCompleted)
	_, err = store.EndSession(t.Context(), start.SessionID, start.StartedAt.Add(time.Second))
	require.NoError(t, err)
	require.NoError(t, store.PurgeSession(t.Context(), start.SessionID))
	start.StartedAt = start.StartedAt.Add(time.Hour)
	_, err = applyRequestStart(t, store, requestKindRoot, start, [32]byte{1})
	require.ErrorIs(t, err, session.ErrSessionPurged)
	assert.Empty(t, store.runs)
	assert.Empty(t, store.records)
	assert.Empty(t, store.seeds)
}
