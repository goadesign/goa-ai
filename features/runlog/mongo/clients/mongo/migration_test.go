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
	assert.True(t, rerun.Applied)
	assert.Equal(t, 2, store.indexCalls)
	assert.Equal(t, 2, store.removeIndexCalls)
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

func TestMigratePrepareLegacyRunsBehindAdmissionBarrier(t *testing.T) {
	t.Parallel()

	store := newFakeMigrationStore([]eventDocument{
		legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1"),
	})
	callbackCalls := make([]bool, 0, 2)
	prepare := func(_ context.Context, apply bool) error {
		callbackCalls = append(callbackCalls, apply)
		store.operations = append(store.operations, fmt.Sprintf("callback:%t", apply))
		if !apply {
			require.Empty(t, store.validationLevel)
			return nil
		}
		require.Equal(t, validationModerate, store.validationLevel)
		require.ErrorContains(t, store.insertLegacyEvent(), "rejected by event validator")
		return nil
	}

	dryRun, err := migrateWithPrepare(context.Background(), store, false, prepare)
	require.NoError(t, err)
	require.False(t, dryRun.Applied)
	require.Equal(t, []bool{false}, callbackCalls)
	require.Empty(t, store.validationLevel)
	require.Equal(t, []string{"callback:false"}, store.operations)
	require.Empty(t, store.event("evt-1").Stream)

	applied, err := migrateWithPrepare(context.Background(), store, true, prepare)
	require.NoError(t, err)
	require.True(t, applied.Applied)
	require.Equal(t, []bool{false, true}, callbackCalls)
	require.Equal(t, validationStrict, store.validationLevel)
	require.Less(
		t,
		operationIndex(store.operations, "validation:moderate"),
		operationIndex(store.operations, "callback:true"),
	)
	require.Less(
		t,
		operationIndex(store.operations, "callback:true"),
		operationIndex(store.operations, "event"),
	)
	require.Less(
		t,
		operationIndex(store.operations, "validation:strict"),
		operationIndex(store.operations, "schema"),
	)
}

func TestMigratePrepareLegacyFailureLeavesBarrierForRerun(t *testing.T) {
	t.Parallel()

	store := newFakeMigrationStore([]eventDocument{
		legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1"),
	})
	prepareErr := errors.New("legacy validation failed")

	_, err := migrateWithPrepare(
		context.Background(),
		store,
		true,
		func(context.Context, bool) error {
			return prepareErr
		},
	)

	require.ErrorIs(t, err, prepareErr)
	require.ErrorContains(t, err, "prepare legacy runlog records after admission barrier")
	require.Equal(t, validationModerate, store.validationLevel)
	require.False(t, store.schemaFound)
	require.Empty(t, store.event("evt-1").Stream)
	require.ErrorContains(t, store.insertLegacyEvent(), "rejected by event validator")

	report, err := migrateWithPrepare(
		context.Background(),
		store,
		true,
		func(context.Context, bool) error {
			return nil
		},
	)
	require.NoError(t, err)
	require.True(t, report.Applied)
	require.Equal(t, validationStrict, store.validationLevel)
}

func TestMigrateDoesNotPrepareLegacyBeforeBarrierSucceeds(t *testing.T) {
	t.Parallel()

	store := newFakeMigrationStore(nil)
	store.failPhase = "barrier"
	called := false

	_, err := migrateWithPrepare(
		context.Background(),
		store,
		true,
		func(context.Context, bool) error {
			called = true
			return nil
		},
	)

	require.ErrorContains(t, err, "install runlog Mongo admission barrier")
	require.False(t, called)
	require.False(t, store.schemaFound)
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
			sequences: map[string]int64{testRunStream: 2},
			mutate: func(events []eventDocument) {
				events[1].Sequence = 3
			},
			wantErr: `has ordering fields ("run:run-1", 3), want ("run:run-1", 2)`,
		},
		{
			name:      "counter_behind",
			sequences: map[string]int64{testRunStream: 1},
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
				events[index].Stream = testRunStream
				events[index].Sequence = int64(index + 1)
			}
			test.mutate(events)
			store := newFakeMigrationStore(events)
			markFakeMigrationCurrent(store)
			store.sequences = test.sequences
			store.bindings["run-1"] = testRunStream

			_, err := migrate(context.Background(), store, false)

			require.ErrorContains(t, err, test.wantErr)
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

func TestMigrateRejectsMissingOrConflictingCurrentBinding(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		binding   string
		wantError string
	}{
		{
			name:      "missing",
			wantError: `runlog run "run-1" is missing its stream binding`,
		},
		{
			name:      "conflicting",
			binding:   "session:other",
			wantError: `runlog run "run-1" binding is "session:other", want "session:session-1"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			event := legacyMigrationEvent("000000000000000000000001", "run-1", "session-1", "evt-1")
			event.Stream = "session:session-1"
			event.Sequence = 1
			store := newFakeMigrationStore([]eventDocument{event})
			markFakeMigrationCurrent(store)
			store.sequences["session:session-1"] = 1
			if test.binding != "" {
				store.bindings["run-1"] = test.binding
			}

			_, err := migrate(context.Background(), store, false)

			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestMigrateRejectsMissingAndOrphanPrivateState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*fakeMigrationStore)
		wantError string
	}{
		{
			name: "missing_current_counter",
			configure: func(store *fakeMigrationStore) {
				event := legacyMigrationEvent("000000000000000000000001", "run-1", "", "evt-1")
				event.Stream = testRunStream
				event.Sequence = 1
				store.events = []eventDocument{event}
				store.bindings["run-1"] = testRunStream
				markFakeMigrationCurrent(store)
			},
			wantError: `runlog stream "run:run-1" is missing its sequence counter`,
		},
		{
			name: "orphan_binding",
			configure: func(store *fakeMigrationStore) {
				store.bindings["run-1"] = testRunStream
			},
			wantError: `runlog binding for run "run-1" has no event`,
		},
		{
			name: "orphan_counter",
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
		{name: "admission_barrier", phase: "barrier"},
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
	bindings         map[string]string
	validationLevel  string
	indexesReady     bool
	legacyIndexes    bool
	schema           schemaDocument
	schemaFound      bool
	indexCalls       int
	removeIndexCalls int
	operations       []string
	failPhase        string
	failAfter        int
	phaseCalls       map[string]int
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

func (s *fakeMigrationStore) scanEvents(_ context.Context, sortOrder bson.D, visit func(eventDocument) error) error {
	events := append([]eventDocument(nil), s.events...)
	sort.Slice(events, func(i, j int) bool {
		return migrationEventLess(events[i], events[j], sortOrder)
	})
	for _, event := range events {
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

func (s *fakeMigrationStore) setEventValidation(_ context.Context, level string) error {
	s.operations = append(s.operations, "validation:"+level)
	phase := "strict_validation"
	if level == validationModerate {
		phase = "barrier"
	}
	if err := s.fail(phase); err != nil {
		return err
	}
	s.validationLevel = level
	return nil
}

func (s *fakeMigrationStore) requireEventValidation(_ context.Context, level string) error {
	if s.validationLevel != level {
		return fmt.Errorf("validation level is %q, want %q", s.validationLevel, level)
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

// insertLegacyEvent simulates an old writer that omits both ordering fields.
func (s *fakeMigrationStore) insertLegacyEvent() error {
	if s.validationLevel == validationModerate || s.validationLevel == validationStrict {
		return fmt.Errorf("legacy insert rejected by event validator")
	}
	s.events = append(s.events, legacyMigrationEvent(
		"000000000000000000000099",
		"legacy-run",
		"",
		"legacy-event",
	))
	return nil
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
		var leftValue, rightValue string
		switch field.Key {
		case "_id":
			leftValue, rightValue = left.ID.Hex(), right.ID.Hex()
		case "run_id":
			leftValue, rightValue = left.RunID, right.RunID
		case "event_key":
			leftValue, rightValue = left.EventKey, right.EventKey
		default:
			panic("unsupported event sort field: " + field.Key)
		}
		if leftValue != rightValue {
			return leftValue < rightValue
		}
	}
	return false
}
