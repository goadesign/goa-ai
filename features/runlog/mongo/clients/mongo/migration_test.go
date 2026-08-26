// These tests exercise bounded, restartable Mongo run-log migration phases
// without requiring a live MongoDB server.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
)

const testRunStream = "run:run-1"

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
	require.Equal(t, []migrationEventScan{
		{
			filter: nil,
			sort: bson.D{
				{Key: "run_id", Value: 1},
				{Key: "event_key", Value: 1},
				{Key: "_id", Value: 1},
			},
		},
		{
			filter: nil,
			sort:   bson.D{{Key: "run_id", Value: 1}, {Key: "_id", Value: 1}},
		},
		{
			filter: bson.D{{Key: "session_id", Value: bson.D{
				{Key: "$type", Value: "string"},
				{Key: "$ne", Value: ""},
			}}},
			sort: bson.D{{Key: "session_id", Value: 1}, {Key: "_id", Value: 1}},
		},
		{
			filter: bson.D{{Key: "session_id", Value: bson.D{
				{Key: "$type", Value: "string"},
				{Key: "$eq", Value: ""},
			}}},
			sort: bson.D{{Key: "run_id", Value: 1}, {Key: "_id", Value: 1}},
		},
	}, store.eventScans)
	store.eventScans = nil

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
	assert.Equal(t, "session:session-1", store.bindings["run-1"])
	assert.Equal(t, "session:session-1", store.bindings["run-2"])
	assert.Equal(t, "run:run-3", store.bindings["run-3"])
	assert.Equal(t, schemaVersion, store.schema.Version)
	assert.Equal(t, 1, store.indexCalls)
	assert.Equal(t, 1, store.removeIndexCalls)
	assert.Equal(t, validationStrict, store.validationLevel)
	assert.True(t, store.indexesReady)
	assert.False(t, store.legacyIndexes)
	assert.Less(
		t,
		operationIndex(store.operations, "validation:strict"),
		operationIndex(store.operations, "schema"),
	)

	rerun, err := migrate(context.Background(), store, true)
	require.NoError(t, err)
	assert.True(t, rerun.AlreadyCurrent)
	assert.Zero(t, rerun.BackfillEvents)
	assert.False(t, rerun.Applied)
	assert.Equal(t, 1, store.indexCalls)
	assert.Equal(t, 1, store.removeIndexCalls)
}

func TestMigrateRejectsMalformedSessionIDsBeforeWrites(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		wantErr string
	}{
		{name: "absent", wantErr: "session_id is absent"},
		{name: "null", wantErr: "session_id is null"},
		{name: "wrong type", wantErr: "session_id has BSON type int"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeMigrationStore([]eventDocument{
				legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1"),
			})
			store.sessionIDValidationErr = errors.New(test.wantErr)

			_, err := migrate(context.Background(), store, true)

			require.ErrorContains(t, err, test.wantErr)
			require.Empty(t, store.operations)
			require.Empty(t, store.eventScans)
			require.False(t, store.schemaFound)
		})
	}
}

func TestMigrateAppliesCurrentSchemaToEmptyDatabase(t *testing.T) {
	t.Parallel()

	store := newFakeMigrationStore(nil)

	report, err := migrate(context.Background(), store, true)

	require.NoError(t, err)
	require.Zero(t, report.ExaminedEvents)
	require.Zero(t, report.Streams)
	require.Zero(t, report.BackfillEvents)
	require.True(t, report.Applied)
	require.Equal(t, schemaVersion, store.schema.Version)
	require.Equal(t, 1, store.indexCalls)
	require.Equal(t, 1, store.removeIndexCalls)
}

func TestExpectedEventValidationRequiresSessionIdentity(t *testing.T) {
	t.Parallel()

	encoded, err := bson.Marshal(expectedEventValidation())
	require.NoError(t, err)
	schema := bson.Raw(encoded).Lookup("$jsonSchema").Document()
	required, err := schema.Lookup("required").Array().Values()
	require.NoError(t, err)
	require.Len(t, required, 3)
	assert.Equal(t, "session_id", required[0].StringValue())
	assert.Equal(t, "stream", required[1].StringValue())
	assert.Equal(t, "sequence", required[2].StringValue())
	sessionID := schema.Lookup("properties").Document().Lookup("session_id").Document()
	assert.Equal(t, "string", sessionID.Lookup("bsonType").StringValue())
}

func TestMigrateCurrentSchemaRequiresExactStorageWithoutWrites(t *testing.T) {
	t.Parallel()

	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprintf("apply_%t", apply), func(t *testing.T) {
			t.Parallel()

			store := newFakeMigrationStore(nil)
			markFakeMigrationCurrent(store)
			store.validationLevel = "moderate"

			_, err := migrate(context.Background(), store, apply)

			require.ErrorContains(t, err, `validation level is "moderate", want "strict"`)
			require.Equal(t, "moderate", store.validationLevel)
			require.Empty(t, store.operations)
			require.Empty(t, store.eventScans)
			require.Zero(t, store.sessionIDValidationCalls)
		})
	}
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
					event.Stream = testRunStream
					event.Sequence = 7
					return event
				}(),
			},
			wantErr: `has ordering fields ("run:run-1", 7), want ("run:run-1", 2)`,
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

func TestMigrateCurrentSchemaDoesNotReconstructObjectIDOrder(t *testing.T) {
	t.Parallel()

	events := []eventDocument{
		legacyMigrationEvent("000000000000000000000002", "run-1", "", "evt-2"),
		legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1"),
	}
	events[0].Stream, events[0].Sequence = testRunStream, 1
	events[1].Stream, events[1].Sequence = testRunStream, 2
	for _, apply := range []bool{false, true} {
		t.Run(fmt.Sprintf("apply_%t", apply), func(t *testing.T) {
			t.Parallel()

			store := newFakeMigrationStore(events)
			markFakeMigrationCurrent(store)

			report, err := migrate(context.Background(), store, apply)

			require.NoError(t, err)
			require.True(t, report.AlreadyCurrent)
			require.Zero(t, report.ExaminedEvents)
			require.Zero(t, report.Streams)
			require.Zero(t, report.BackfillEvents)
			require.False(t, report.Applied)
			require.Empty(t, store.eventScans)
			require.Empty(t, store.operations)
		})
	}
}

func TestMigrateBackfillsBindingsFromPreviousSchema(t *testing.T) {
	t.Parallel()

	events := []eventDocument{
		legacyMigrationEvent("000000000000000000000001", "run-1", "session-1", "evt-1"),
		legacyMigrationEvent("000000000000000000000002", "run-1", "session-1", "evt-2"),
	}
	for index := range events {
		events[index].Stream = "session:session-1"
		events[index].Sequence = int64(index + 1)
	}
	store := newFakeMigrationStore(events)
	store.schema = schemaDocument{Name: schemaSentinelID, Version: schemaVersion - 1}
	store.schemaFound = true
	store.sequences["session:session-1"] = 2

	report, err := migrate(context.Background(), store, true)

	require.NoError(t, err)
	require.False(t, report.AlreadyCurrent)
	require.Zero(t, report.BackfillEvents)
	require.Equal(t, "session:session-1", store.bindings["run-1"])
	require.Equal(t, schemaVersion, store.schema.Version)
}

func TestMigrateUpgradesVersionTwoValidatorContract(t *testing.T) {
	t.Parallel()

	event := legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1")
	event.Stream = testRunStream
	event.Sequence = 1
	store := newFakeMigrationStore([]eventDocument{event})
	store.schema = schemaDocument{Name: schemaSentinelID, Version: 2}
	store.schemaFound = true
	store.bindings["run-1"] = testRunStream
	store.sequences[testRunStream] = 1
	store.validationLevel = validationStrict
	store.indexesReady = true
	store.legacyIndexes = false

	report, err := migrate(context.Background(), store, true)

	require.NoError(t, err)
	assert.True(t, report.Applied)
	assert.False(t, report.AlreadyCurrent)
	assert.Equal(t, 3, store.schema.Version)
	assert.Equal(t, 1, store.sessionIDValidationCalls)
	assert.Contains(t, store.operations, "validation:strict")
}

func TestMigrateRejectsConflictingLegacyBinding(t *testing.T) {
	t.Parallel()

	event := legacyMigrationEvent("000000000000000000000001", "run-1", "session-1", "evt-1")
	store := newFakeMigrationStore([]eventDocument{event})
	store.bindings["run-1"] = "session:other"

	_, err := migrate(context.Background(), store, false)

	require.ErrorContains(
		t,
		err,
		`runlog run "run-1" binding is "session:other", want "session:session-1"`,
	)
}

func TestMigrateRejectsConflictingLegacyCounter(t *testing.T) {
	t.Parallel()

	event := legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1")
	store := newFakeMigrationStore([]eventDocument{event})
	store.sequences[testRunStream] = 2

	_, err := migrate(context.Background(), store, false)

	require.ErrorContains(t, err, `runlog stream "run:run-1" counter is 2, want 1`)
}

func TestMigrateRejectsOrphanPrivateState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*fakeMigrationStore)
		wantError string
	}{
		{
			name: "binding",
			configure: func(store *fakeMigrationStore) {
				store.bindings["run-1"] = testRunStream
			},
			wantError: `runlog binding for run "run-1" has no event`,
		},
		{
			name: "counter",
			configure: func(store *fakeMigrationStore) {
				store.sequences[testRunStream] = 1
			},
			wantError: `runlog sequence "run:run-1" has no event stream`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeMigrationStore(nil)
			test.configure(store)

			_, err := migrate(context.Background(), store, false)

			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestMigrateRerunsAfterPartialPhaseFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		phase     string
		failAfter int
	}{
		{name: "event_backfill", phase: "events", failAfter: 1},
		{name: "binding_backfill", phase: "bindings", failAfter: 1},
		{name: "counter_initialization", phase: "counters", failAfter: 1},
		{name: "index_creation", phase: "indexes"},
		{name: "legacy_index_removal", phase: "legacy_indexes"},
		{name: "strict_validation", phase: "strict_validation"},
		{name: "schema_sentinel", phase: "schema"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			store := newFakeMigrationStore([]eventDocument{
				legacyMigrationEvent("000000000000000000000001", "run-1", "session-1", "evt-1"),
				legacyMigrationEvent("000000000000000000000002", "run-2", "session-1", "evt-2"),
				legacyMigrationEvent("000000000000000000000003", "run-3", "", "evt-3"),
			})
			store.failPhase = test.phase
			store.failAfter = test.failAfter

			_, err := migrate(context.Background(), store, true)
			require.ErrorContains(t, err, "injected "+test.phase+" failure")
			require.False(t, store.schemaFound)

			store.failPhase = ""
			report, err := migrate(context.Background(), store, true)
			require.NoError(t, err)
			require.True(t, report.Applied)
			require.Equal(t, schemaVersion, store.schema.Version)

			current, err := migrate(context.Background(), store, false)
			require.NoError(t, err)
			require.True(t, current.AlreadyCurrent)
			require.Zero(t, current.BackfillEvents)
		})
	}
}

func TestVerifyEventIndexesRejectsBehaviorChangingRequiredIndexOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func([]eventIndexDocument)
		wantError bool
	}{
		{
			name:   "ordinary required indexes",
			mutate: func([]eventIndexDocument) {},
		},
		{
			name: "partial filter",
			mutate: func(indexes []eventIndexDocument) {
				indexes[0].PartialFilterExpression = bson.Raw{1}
			},
			wantError: true,
		},
		{
			name: "sparse",
			mutate: func(indexes []eventIndexDocument) {
				indexes[0].Sparse = true
			},
			wantError: true,
		},
		{
			name: "hidden",
			mutate: func(indexes []eventIndexDocument) {
				indexes[0].Hidden = true
			},
			wantError: true,
		},
		{
			name: "collation",
			mutate: func(indexes []eventIndexDocument) {
				indexes[0].Collation = bson.Raw{1}
			},
			wantError: true,
		},
		{
			name: "TTL",
			mutate: func(indexes []eventIndexDocument) {
				indexes[0].ExpireAfterSeconds = bson.RawValue{Type: bson.TypeInt32}
			},
			wantError: true,
		},
		{
			name: "clustered",
			mutate: func(indexes []eventIndexDocument) {
				indexes[0].Clustered = true
			},
			wantError: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			indexes := requiredEventIndexDocuments()
			test.mutate(indexes)

			err := verifyEventIndexes(indexes)
			if test.wantError {
				require.ErrorContains(t, err, "has behavior-changing options")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestVerifyEventIndexesIgnoresOptionsOnUnrelatedIndex(t *testing.T) {
	t.Parallel()

	indexes := append(requiredEventIndexDocuments(), eventIndexDocument{
		Name:                    "unrelated",
		Keys:                    bson.D{{Key: "other", Value: int32(1)}},
		PartialFilterExpression: bson.Raw{1},
		Sparse:                  true,
		Hidden:                  true,
		Collation:               bson.Raw{1},
		ExpireAfterSeconds:      bson.RawValue{Type: bson.TypeInt32},
		Clustered:               true,
	})

	require.NoError(t, verifyEventIndexes(indexes))
}

// requiredEventIndexDocuments creates the ordinary full indexes that satisfy
// every migration requirement before one test changes a focused option.
func requiredEventIndexDocuments() []eventIndexDocument {
	requirements := requiredEventIndexes()
	indexes := make([]eventIndexDocument, len(requirements))
	for index, requirement := range requirements {
		indexes[index] = eventIndexDocument{
			Name:   fmt.Sprintf("required-%d", index),
			Keys:   requirement.keys,
			Unique: requirement.unique,
		}
	}
	return indexes
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
	events                   []eventDocument
	eventScans               []migrationEventScan
	sequences                map[string]int64
	bindings                 map[string]string
	validationLevel          string
	indexesReady             bool
	legacyIndexes            bool
	schema                   schemaDocument
	schemaFound              bool
	indexCalls               int
	removeIndexCalls         int
	operations               []string
	failPhase                string
	failAfter                int
	phaseCalls               map[string]int
	sessionIDValidationErr   error
	sessionIDValidationCalls int
}

type migrationEventScan struct {
	filter bson.D
	sort   bson.D
}

func newFakeMigrationStore(events []eventDocument) *fakeMigrationStore {
	return &fakeMigrationStore{
		events:        append([]eventDocument(nil), events...),
		sequences:     make(map[string]int64),
		bindings:      make(map[string]string),
		legacyIndexes: true,
		phaseCalls:    make(map[string]int),
	}
}

// markFakeMigrationCurrent installs the storage state required by schema
// version 2 before tests introduce one focused corruption.
func markFakeMigrationCurrent(store *fakeMigrationStore) {
	store.schema = schemaDocument{Name: schemaSentinelID, Version: schemaVersion}
	store.schemaFound = true
	store.validationLevel = validationStrict
	store.indexesReady = true
	store.legacyIndexes = false
}

func (s *fakeMigrationStore) validateSessionIDs(context.Context) error {
	s.sessionIDValidationCalls++
	return s.sessionIDValidationErr
}

func (s *fakeMigrationStore) scanEvents(_ context.Context, filter, sortOrder bson.D, visit func(eventDocument) error) error {
	s.eventScans = append(s.eventScans, migrationEventScan{
		filter: append(bson.D(nil), filter...),
		sort:   append(bson.D(nil), sortOrder...),
	})
	events := append([]eventDocument(nil), s.events...)
	sort.Slice(events, func(i, j int) bool {
		return migrationEventLess(events[i], events[j], sortOrder)
	})
	for _, event := range events {
		if !migrationEventMatches(event, filter) {
			continue
		}
		if err := visit(event); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeMigrationStore) scanBindings(_ context.Context, visit func(runBindingDocument) error) error {
	runIDs := make([]string, 0, len(s.bindings))
	for runID := range s.bindings {
		runIDs = append(runIDs, runID)
	}
	sort.Strings(runIDs)
	for _, runID := range runIDs {
		if err := visit(runBindingDocument{RunID: runID, Stream: s.bindings[runID]}); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeMigrationStore) scanSequences(_ context.Context, visit func(sequenceDocument) error) error {
	streams := make([]string, 0, len(s.sequences))
	for stream := range s.sequences {
		streams = append(streams, stream)
	}
	sort.Strings(streams)
	for _, stream := range streams {
		if err := visit(sequenceDocument{Stream: stream, Sequence: s.sequences[stream]}); err != nil {
			return err
		}
	}
	return nil
}

func (s *fakeMigrationStore) loadSchema(context.Context) (schemaDocument, bool, error) {
	return s.schema, s.schemaFound, nil
}

func (s *fakeMigrationStore) loadBinding(_ context.Context, runID string) (runBindingDocument, bool, error) {
	stream, ok := s.bindings[runID]
	return runBindingDocument{RunID: runID, Stream: stream}, ok, nil
}

func (s *fakeMigrationStore) loadRunStream(_ context.Context, runID string) (string, bool, error) {
	var (
		first eventDocument
		found bool
	)
	for _, event := range s.events {
		if event.RunID != runID || found && event.ID.Hex() >= first.ID.Hex() {
			continue
		}
		first = event
		found = true
	}
	if !found {
		return "", false, nil
	}
	return streamKey(first.RunID, first.SessionID), true, nil
}

func (s *fakeMigrationStore) loadSequence(_ context.Context, stream string) (sequenceDocument, bool, error) {
	sequence, ok := s.sequences[stream]
	return sequenceDocument{Stream: stream, Sequence: sequence}, ok, nil
}

func (s *fakeMigrationStore) hasEventStream(_ context.Context, stream string) (bool, error) {
	for _, event := range s.events {
		if streamKey(event.RunID, event.SessionID) == stream {
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeMigrationStore) updateEvent(_ context.Context, event eventDocument, stream string, sequence int64) error {
	s.operations = append(s.operations, "event")
	if err := s.fail("events"); err != nil {
		return err
	}
	for index := range s.events {
		if s.events[index].ID == event.ID {
			s.events[index].Stream = stream
			s.events[index].Sequence = sequence
			return nil
		}
	}
	return fmt.Errorf("event %s not found", event.ID.Hex())
}

func (s *fakeMigrationStore) setBinding(_ context.Context, runID, stream string) error {
	s.operations = append(s.operations, "binding")
	if err := s.fail("bindings"); err != nil {
		return err
	}
	s.bindings[runID] = stream
	return nil
}

func (s *fakeMigrationStore) setSequence(_ context.Context, stream string, sequence int64) error {
	s.operations = append(s.operations, "counter")
	if err := s.fail("counters"); err != nil {
		return err
	}
	s.sequences[stream] = sequence
	return nil
}

func (s *fakeMigrationStore) ensureIndexes(context.Context) error {
	s.indexCalls++
	s.operations = append(s.operations, "indexes")
	if err := s.fail("indexes"); err != nil {
		return err
	}
	s.indexesReady = true
	return nil
}

func (s *fakeMigrationStore) removeLegacyIndexes(context.Context) error {
	s.removeIndexCalls++
	s.operations = append(s.operations, "legacy_indexes")
	if err := s.fail("legacy_indexes"); err != nil {
		return err
	}
	s.legacyIndexes = false
	return nil
}

func (s *fakeMigrationStore) setEventValidation(context.Context) error {
	s.operations = append(s.operations, "validation:"+validationStrict)
	if err := s.fail("strict_validation"); err != nil {
		return err
	}
	s.validationLevel = validationStrict
	return nil
}

func (s *fakeMigrationStore) requireEventValidation(context.Context) error {
	if s.validationLevel != validationStrict {
		return fmt.Errorf("validation level is %q, want %q", s.validationLevel, validationStrict)
	}
	return nil
}

func (s *fakeMigrationStore) requireEventIndexes(context.Context) error {
	if !s.indexesReady {
		return fmt.Errorf("required indexes are missing")
	}
	if s.legacyIndexes {
		return fmt.Errorf("legacy indexes remain")
	}
	return nil
}

func (s *fakeMigrationStore) setSchema(_ context.Context, version int) error {
	s.operations = append(s.operations, "schema")
	if err := s.fail("schema"); err != nil {
		return err
	}
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

// fail returns an injected error after the configured number of successful
// writes in one migration phase.
func (s *fakeMigrationStore) fail(phase string) error {
	s.phaseCalls[phase]++
	if s.failPhase == phase && s.phaseCalls[phase] > s.failAfter {
		return fmt.Errorf("injected %s failure", phase)
	}
	return nil
}

// operationIndex returns the recorded position of one migration phase.
func operationIndex(operations []string, want string) int {
	for index, operation := range operations {
		if operation == want {
			return index
		}
	}
	panic("operation not found: " + want)
}

// migrationEventLess applies the migration's requested field order to fake
// projected event rows.
func migrationEventLess(left, right eventDocument, sortOrder bson.D) bool {
	for _, field := range sortOrder {
		direction, ok := field.Value.(int)
		if !ok || direction != 1 {
			panic(fmt.Sprintf("unsupported event sort direction for %q: %v", field.Key, field.Value))
		}
		var leftValue, rightValue string
		switch field.Key {
		case "_id":
			leftValue, rightValue = left.ID.Hex(), right.ID.Hex()
		case "run_id":
			leftValue, rightValue = left.RunID, right.RunID
		case "event_key":
			leftValue, rightValue = left.EventKey, right.EventKey
		case "session_id":
			leftValue, rightValue = left.SessionID, right.SessionID
		default:
			panic("unsupported event sort field: " + field.Key)
		}
		if leftValue != rightValue {
			return leftValue < rightValue
		}
	}
	return false
}

// migrationEventMatches applies the two session filters emitted by the
// production migration store.
func migrationEventMatches(event eventDocument, filter bson.D) bool {
	if len(filter) == 0 {
		return true
	}
	if len(filter) != 1 || filter[0].Key != "session_id" {
		panic(fmt.Sprintf("unsupported event filter: %v", filter))
	}
	value, ok := filter[0].Value.(bson.D)
	if !ok || len(value) != 2 ||
		value[0].Key != "$type" || value[0].Value != "string" {
		panic(fmt.Sprintf("unsupported session event filter value: %v", filter[0].Value))
	}
	sessionID, ok := value[1].Value.(string)
	if !ok {
		panic(fmt.Sprintf("unsupported session event filter value: %v", value[1].Value))
	}
	switch value[1].Key {
	case "$eq":
		return event.SessionID == sessionID
	case "$ne":
		return event.SessionID != sessionID
	default:
		panic(fmt.Sprintf("unsupported session event filter value: %v", value))
	}
}
