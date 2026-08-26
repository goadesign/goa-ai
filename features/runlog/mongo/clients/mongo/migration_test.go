package mongo

import (
	"context"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestMigrateDryRunApplyAndRerun(t *testing.T) {
	t.Parallel()

	store := newFakeMigrationStore([]eventDocument{
		legacyMigrationEvent("000000000000000000000003", "run-3", "", "evt-3"),
		legacyMigrationEvent("000000000000000000000001", "run-1", "session-1", "evt-1"),
		legacyMigrationEvent("000000000000000000000002", "run-2", "session-1", "evt-2"),
	})

	dryRun, err := migrate(context.Background(), store, false)
	require.NoError(t, err)
	assert.Equal(t, 3, dryRun.ExaminedEvents)
	assert.Equal(t, 2, dryRun.Streams)
	assert.Equal(t, 3, dryRun.BackfillEvents)
	assert.False(t, dryRun.Applied)
	assert.False(t, store.schemaFound)
	for _, event := range store.events {
		assert.Empty(t, event.Stream)
		assert.Zero(t, event.Sequence)
	}

	applied, err := migrate(context.Background(), store, true)
	require.NoError(t, err)
	assert.True(t, applied.Applied)
	assert.Equal(t, "session:session-1", store.event("evt-1").Stream)
	assert.EqualValues(t, 1, store.event("evt-1").Sequence)
	assert.Equal(t, "session:session-1", store.event("evt-2").Stream)
	assert.EqualValues(t, 2, store.event("evt-2").Sequence)
	assert.Equal(t, "run:run-3", store.event("evt-3").Stream)
	assert.EqualValues(t, 1, store.event("evt-3").Sequence)
	assert.EqualValues(t, 2, store.sequences["session:session-1"])
	assert.EqualValues(t, 1, store.sequences["run:run-3"])
	assert.Equal(t, schemaVersion, store.schema.Version)
	assert.Equal(t, 1, store.indexCalls)
	assert.Equal(t, 1, store.removeIndexCalls)

	rerun, err := migrate(context.Background(), store, true)
	require.NoError(t, err)
	assert.True(t, rerun.AlreadyCurrent)
	assert.Zero(t, rerun.BackfillEvents)
	assert.True(t, rerun.Applied)
	assert.Equal(t, 2, store.indexCalls)
	assert.Equal(t, 2, store.removeIndexCalls)
}

func TestMigrateRejectsMalformedAndConflictingLegacyData(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		events  []eventDocument
		wantErr string
	}{
		{
			name: "missing_run",
			events: []eventDocument{
				legacyMigrationEvent("000000000000000000000001", "", "", "evt-1"),
			},
			wantErr: "has empty run_id",
		},
		{
			name: "duplicate_identity",
			events: []eventDocument{
				legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1"),
				legacyMigrationEvent("000000000000000000000002", "run-1", "", "evt-1"),
			},
			wantErr: `runlog identity ("run-1", "evt-1") is duplicated`,
		},
		{
			name: "run_changes_stream",
			events: []eventDocument{
				legacyMigrationEvent("000000000000000000000001", "run-1", "session-1", "evt-1"),
				legacyMigrationEvent("000000000000000000000002", "run-1", "session-2", "evt-2"),
			},
			wantErr: `runlog run "run-1" spans ordering streams`,
		},
		{
			name: "partial_sequence_conflict",
			events: []eventDocument{
				legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1"),
				func() eventDocument {
					event := legacyMigrationEvent("000000000000000000000002", "run-1", "", "evt-2")
					event.Stream = "run:run-1"
					event.Sequence = 7
					return event
				}(),
			},
			wantErr: "has conflicting sequence 7, want 2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeMigrationStore(test.events)
			_, err := migrate(context.Background(), store, true)
			require.ErrorContains(t, err, test.wantErr)
			assert.False(t, store.schemaFound)
			assert.Zero(t, store.indexCalls)
			assert.Zero(t, store.removeIndexCalls)
		})
	}
}

func TestMigrateRejectsCurrentSequenceAndCounterConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		sequences map[string]int64
		mutate    func([]eventDocument)
		wantErr   string
	}{
		{
			name:      "missing_sequence",
			sequences: map[string]int64{"run:run-1": 2},
			mutate: func(events []eventDocument) {
				events[1].Sequence = 3
			},
			wantErr: `stream "run:run-1" is missing sequence 2`,
		},
		{
			name:      "counter_behind",
			sequences: map[string]int64{"run:run-1": 1},
			mutate:    func([]eventDocument) {},
			wantErr:   `counter is 1, want 2`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			events := []eventDocument{
				legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1"),
				legacyMigrationEvent("000000000000000000000002", "run-1", "", "evt-2"),
			}
			for index := range events {
				events[index].Stream = "run:run-1"
				events[index].Sequence = int64(index + 1)
			}
			test.mutate(events)
			store := newFakeMigrationStore(events)
			store.schema = schemaDocument{Name: schemaSentinelID, Version: schemaVersion}
			store.schemaFound = true
			store.sequences = test.sequences

			_, err := migrate(context.Background(), store, false)

			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func legacyMigrationEvent(id, runID, sessionID, eventKey string) eventDocument {
	objectID, err := bson.ObjectIDFromHex(id)
	if err != nil {
		panic(err)
	}
	return eventDocument{
		ID:        objectID,
		RunID:     runID,
		SessionID: sessionID,
		EventKey:  eventKey,
	}
}

type fakeMigrationStore struct {
	events           []eventDocument
	sequences        map[string]int64
	schema           schemaDocument
	schemaFound      bool
	indexCalls       int
	removeIndexCalls int
}

func newFakeMigrationStore(events []eventDocument) *fakeMigrationStore {
	return &fakeMigrationStore{
		events:    append([]eventDocument(nil), events...),
		sequences: make(map[string]int64),
	}
}

func (s *fakeMigrationStore) listEvents(context.Context) ([]eventDocument, error) {
	events := append([]eventDocument(nil), s.events...)
	sort.Slice(events, func(i, j int) bool {
		return events[i].ID.Hex() < events[j].ID.Hex()
	})
	return events, nil
}

func (s *fakeMigrationStore) loadSchema(context.Context) (schemaDocument, bool, error) {
	return s.schema, s.schemaFound, nil
}

func (s *fakeMigrationStore) loadSequence(_ context.Context, stream string) (sequenceDocument, bool, error) {
	sequence, ok := s.sequences[stream]
	return sequenceDocument{Stream: stream, Sequence: sequence}, ok, nil
}

func (s *fakeMigrationStore) updateEvent(_ context.Context, event eventDocument, stream string, sequence int64) error {
	for index := range s.events {
		if s.events[index].ID == event.ID {
			s.events[index].Stream = stream
			s.events[index].Sequence = sequence
			return nil
		}
	}
	return fmt.Errorf("event %s not found", event.ID.Hex())
}

func (s *fakeMigrationStore) setSequence(_ context.Context, stream string, sequence int64) error {
	s.sequences[stream] = sequence
	return nil
}

func (s *fakeMigrationStore) ensureIndexes(context.Context) error {
	s.indexCalls++
	return nil
}

func (s *fakeMigrationStore) removeLegacyIndexes(context.Context) error {
	s.removeIndexCalls++
	return nil
}

func (s *fakeMigrationStore) setSchema(_ context.Context, version int) error {
	s.schema = schemaDocument{Name: schemaSentinelID, Version: version}
	s.schemaFound = true
	return nil
}

func (s *fakeMigrationStore) event(eventKey string) eventDocument {
	for _, event := range s.events {
		if event.EventKey == eventKey {
			return event
		}
	}
	panic("event not found: " + eventKey)
}
