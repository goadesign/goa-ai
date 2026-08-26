// Package mongo provides the MongoDB run-log client and its schema migration.
// Applications run Migrate before rolling out new writers. Mongo rejects legacy
// inserts during the cutover, and Client startup accepts only the fully
// migrated ordering schema.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type (
	// MigrationOptions identifies the MongoDB collections to inspect. Migrate is
	// a dry run unless Apply is true.
	MigrationOptions struct {
		// Client is the connected MongoDB client.
		Client *mongodriver.Client
		// Database is the database that owns the run-log collections.
		Database string
		// Collection is the event collection name. The default is
		// "agent_run_events".
		Collection string
		// Apply installs event admission, invokes PrepareLegacy, and writes event
		// sequences, run bindings, counters, indexes, and the schema sentinel.
		// False invokes PrepareLegacy without writes, validates storage, and
		// reports the required work.
		Apply bool
		// Timeout bounds the complete migration. Zero uses the caller's context
		// without adding another deadline.
		Timeout time.Duration
		// PrepareLegacy validates caller-owned facts before generic migration
		// begins. Migrate calls it with apply=false during a dry run; the
		// callback must not write in that mode. Apply calls it again with
		// apply=true after Mongo rejects new legacy inserts and before goa-ai
		// writes ordering fields. Apply repairs must be safe to repeat. A failed
		// apply callback leaves admission active and the sentinel unwritten.
		PrepareLegacy func(ctx context.Context, apply bool) error
	}

	// MigrationReport describes the validated ordering migration.
	MigrationReport struct {
		// ExaminedEvents is the number of persisted events checked.
		ExaminedEvents int
		// Streams is the number of session or sessionless-run ordering streams.
		Streams int
		// BackfillEvents is the number of events that need sequence fields.
		BackfillEvents int
		// Applied reports whether this call wrote the migration.
		Applied bool
		// AlreadyCurrent reports whether the current schema sentinel existed
		// before this call.
		AlreadyCurrent bool
	}

	migrationStore interface {
		scanEvents(ctx context.Context, sort bson.D, visit func(eventDocument) error) error
		scanBindings(ctx context.Context, visit func(runBindingDocument) error) error
		scanSequences(ctx context.Context, visit func(sequenceDocument) error) error
		loadSchema(ctx context.Context) (schemaDocument, bool, error)
		loadBinding(ctx context.Context, runID string) (runBindingDocument, bool, error)
		loadRunStream(ctx context.Context, runID string) (string, bool, error)
		loadSequence(ctx context.Context, stream string) (sequenceDocument, bool, error)
		updateEvent(ctx context.Context, event eventDocument, stream string, sequence int64) error
		setBinding(ctx context.Context, runID, stream string) error
		setSequence(ctx context.Context, stream string, sequence int64) error
		ensureIndexes(ctx context.Context) error
		removeLegacyIndexes(ctx context.Context) error
		setEventValidation(ctx context.Context, level string) error
		requireEventValidation(ctx context.Context, level string) error
		requireEventIndexes(ctx context.Context) error
		setSchema(ctx context.Context, version int) error
	}

	migrationPlan struct {
		maxSequences map[string]int64
		examined     int
		backfills    int
		current      bool
	}

	mongoMigrationStore struct {
		database   *mongodriver.Database
		collection string
		events     *mongodriver.Collection
		sequences  *mongodriver.Collection
		bindings   *mongodriver.Collection
		schemas    *mongodriver.Collection
	}

	eventValidationDocument struct {
		JSONSchema eventJSONSchema `bson:"$jsonSchema"`
		Extra      bson.M          `bson:",inline"`
	}

	eventJSONSchema struct {
		BSONType   string                    `bson:"bsonType"`
		Required   []string                  `bson:"required"`
		Properties eventValidationProperties `bson:"properties"`
		Extra      bson.M                    `bson:",inline"`
	}

	eventValidationProperties struct {
		Stream   eventValidationField `bson:"stream"`
		Sequence eventValidationField `bson:"sequence"`
		Extra    bson.M               `bson:",inline"`
	}

	eventValidationField struct {
		BSONType string `bson:"bsonType"`
		Minimum  *int64 `bson:"minimum,omitempty"`
		Extra    bson.M `bson:",inline"`
	}

	collectionValidationOptions struct {
		Validator        eventValidationDocument `bson:"validator"`
		ValidationLevel  string                  `bson:"validationLevel"`
		ValidationAction string                  `bson:"validationAction"`
	}

	indexRequirement struct {
		keys   bson.D
		unique bool
	}
)

const (
	validationModerate = "moderate"
	validationStrict   = "strict"
	validationAction   = "error"
)

// Migrate validates and optionally applies the ordering schema required by New.
// Existing events receive sequence numbers in their current ObjectID order.
// Apply first installs a Mongo validator that rejects new sequence-less events,
// then runs the optional legacy callback and deterministic write phases. Old
// writers may remain running, but their appends fail until they are replaced.
// The schema sentinel is written only after strict validation and every event,
// binding, counter, and index are ready.
func Migrate(ctx context.Context, opts MigrationOptions) (MigrationReport, error) {
	if opts.Client == nil {
		return MigrationReport{}, errors.New("mongo client is required")
	}
	if opts.Database == "" {
		return MigrationReport{}, errors.New("database name is required")
	}
	collection := opts.Collection
	if collection == "" {
		collection = defaultCollection
	}
	database := opts.Client.Database(opts.Database)
	store := mongoMigrationStore{
		database:   database,
		collection: collection,
		events:     database.Collection(collection),
		sequences:  database.Collection(collection + sequenceCollectionSuffix),
		bindings:   database.Collection(collection + bindingCollectionSuffix),
		schemas:    database.Collection(collection + schemaCollectionSuffix),
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	return migrateWithPrepare(ctx, store, opts.Apply, opts.PrepareLegacy)
}

// migrate installs storage admission before any caller-owned repair or
// sequence write, validates every persisted field, and then repeats
// deterministic cursor passes for each write phase.
func migrate(ctx context.Context, store migrationStore, apply bool) (MigrationReport, error) {
	return migrateWithPrepare(ctx, store, apply, nil)
}

// migrateWithPrepare runs the same migration with an optional caller-owned
// legacy validation and repair callback.
func migrateWithPrepare(
	ctx context.Context,
	store migrationStore,
	apply bool,
	prepareLegacy func(context.Context, bool) error,
) (MigrationReport, error) {
	sentinel, found, err := store.loadSchema(ctx)
	if err != nil {
		return MigrationReport{}, fmt.Errorf("load runlog Mongo schema sentinel: %w", err)
	}
	if found {
		if sentinel.Name != schemaSentinelID {
			return MigrationReport{}, fmt.Errorf("runlog Mongo schema sentinel has unexpected id %q", sentinel.Name)
		}
		if sentinel.Version > schemaVersion {
			return MigrationReport{}, fmt.Errorf(
				"runlog Mongo schema version %d is newer than supported version %d",
				sentinel.Version,
				schemaVersion,
			)
		}
		if sentinel.Version < 0 {
			return MigrationReport{}, fmt.Errorf("runlog Mongo schema version %d is invalid", sentinel.Version)
		}
	}

	current := found && sentinel.Version == schemaVersion
	if !apply {
		if prepareLegacy != nil {
			if err := prepareLegacy(ctx, false); err != nil {
				return MigrationReport{}, fmt.Errorf("validate legacy runlog records: %w", err)
			}
		}
		if current {
			if err := requireStorageContract(ctx, store); err != nil {
				return MigrationReport{}, err
			}
		}
	} else {
		if current {
			if err := requireStorageContract(ctx, store); err != nil {
				return MigrationReport{}, err
			}
		} else if err := store.setEventValidation(ctx, validationModerate); err != nil {
			return MigrationReport{}, fmt.Errorf("install runlog Mongo admission barrier: %w", err)
		}
		if prepareLegacy != nil {
			if err := prepareLegacy(ctx, true); err != nil {
				return MigrationReport{}, fmt.Errorf(
					"prepare legacy runlog records after admission barrier: %w",
					err,
				)
			}
		}
	}
	plan, err := buildMigrationPlan(ctx, store, current)
	if err != nil {
		return MigrationReport{}, err
	}

	report := MigrationReport{
		ExaminedEvents: plan.examined,
		Streams:        len(plan.maxSequences),
		BackfillEvents: plan.backfills,
		AlreadyCurrent: current,
	}
	if !apply {
		return report, nil
	}
	if err := applyEventBackfills(ctx, store); err != nil {
		return MigrationReport{}, fmt.Errorf("backfill runlog event ordering fields: %w", err)
	}
	if err := applyRunBindings(ctx, store); err != nil {
		return MigrationReport{}, fmt.Errorf("backfill runlog run bindings: %w", err)
	}
	streams := sortedStreams(plan.maxSequences)
	for _, stream := range streams {
		sequence := plan.maxSequences[stream]
		if err := store.setSequence(ctx, stream, sequence); err != nil {
			return MigrationReport{}, fmt.Errorf("initialize runlog sequence %q: %w", stream, err)
		}
	}
	if err := store.ensureIndexes(ctx); err != nil {
		return MigrationReport{}, fmt.Errorf("create runlog sequence indexes: %w", err)
	}
	if err := store.removeLegacyIndexes(ctx); err != nil {
		return MigrationReport{}, fmt.Errorf("remove runlog ObjectID cursor indexes: %w", err)
	}
	if err := store.setEventValidation(ctx, validationStrict); err != nil {
		return MigrationReport{}, fmt.Errorf("tighten runlog Mongo event validation: %w", err)
	}
	if err := requireStorageContract(ctx, store); err != nil {
		return MigrationReport{}, err
	}
	if err := store.setSchema(ctx, schemaVersion); err != nil {
		return MigrationReport{}, fmt.Errorf("write runlog Mongo schema sentinel: %w", err)
	}
	report.Applied = true
	return report, nil
}

// buildMigrationPlan validates identities, immutable run bindings, ordering
// fields, and counters without retaining event payloads or per-event state.
func buildMigrationPlan(ctx context.Context, store migrationStore, current bool) (migrationPlan, error) {
	plan := migrationPlan{
		maxSequences: make(map[string]int64),
		current:      current,
	}
	if err := validateEventIdentities(ctx, store); err != nil {
		return migrationPlan{}, fmt.Errorf("validate runlog event identities: %w", err)
	}
	if err := validateRunBindings(ctx, store, current); err != nil {
		return migrationPlan{}, fmt.Errorf("validate runlog run bindings: %w", err)
	}
	if err := store.scanEvents(ctx, bson.D{{Key: "_id", Value: 1}}, func(event eventDocument) error {
		stream := streamKey(event.RunID, event.SessionID)
		sequence := plan.maxSequences[stream] + 1
		plan.maxSequences[stream] = sequence
		plan.examined++
		switch {
		case event.Stream == "" && event.Sequence == 0:
			if current {
				return fmt.Errorf("runlog event %s is missing ordering fields", event.ID.Hex())
			}
			plan.backfills++
		case event.Stream == stream && event.Sequence == sequence:
		default:
			return fmt.Errorf(
				"runlog event %s has ordering fields (%q, %d), want (%q, %d)",
				event.ID.Hex(),
				event.Stream,
				event.Sequence,
				stream,
				sequence,
			)
		}
		return nil
	}); err != nil {
		return migrationPlan{}, fmt.Errorf("validate runlog event ordering fields: %w", err)
	}
	if err := validateMigrationCounters(ctx, store, plan); err != nil {
		return migrationPlan{}, err
	}
	return plan, nil
}

// validateEventIdentities detects malformed or repeated logical event keys with
// a sorted pass that retains only the preceding identity.
func validateEventIdentities(ctx context.Context, store migrationStore) error {
	var previous eventDocument
	return store.scanEvents(
		ctx,
		bson.D{
			{Key: "run_id", Value: 1},
			{Key: "event_key", Value: 1},
			{Key: "_id", Value: 1},
		},
		func(event eventDocument) error {
			if event.RunID == "" {
				return fmt.Errorf("runlog event %s has empty run_id", event.ID.Hex())
			}
			if event.EventKey == "" {
				return fmt.Errorf("runlog event %s has empty event_key", event.ID.Hex())
			}
			if previous.RunID == event.RunID && previous.EventKey == event.EventKey {
				return fmt.Errorf(
					"runlog identity (%q, %q) is duplicated",
					event.RunID,
					event.EventKey,
				)
			}
			previous = event
			return nil
		},
	)
}

// validateRunBindings checks each run's events as one sorted group and confirms
// any persisted private binding. Current-schema runs must already be bound.
func validateRunBindings(ctx context.Context, store migrationStore, current bool) error {
	var (
		runID  string
		stream string
	)
	validatePrevious := func() error {
		if runID == "" {
			return nil
		}
		binding, found, err := store.loadBinding(ctx, runID)
		if err != nil {
			return fmt.Errorf("load run %q binding: %w", runID, err)
		}
		if !found {
			if current {
				return fmt.Errorf("runlog run %q is missing its stream binding", runID)
			}
			return nil
		}
		if binding.RunID != runID || binding.Stream != stream {
			return fmt.Errorf(
				"runlog run %q binding is %q, want %q",
				runID,
				binding.Stream,
				stream,
			)
		}
		return nil
	}
	err := store.scanEvents(
		ctx,
		bson.D{{Key: "run_id", Value: 1}, {Key: "_id", Value: 1}},
		func(event eventDocument) error {
			eventStream := streamKey(event.RunID, event.SessionID)
			if event.RunID != runID {
				if err := validatePrevious(); err != nil {
					return err
				}
				runID = event.RunID
				stream = eventStream
				return nil
			}
			if eventStream != stream {
				return fmt.Errorf(
					"runlog run %q spans ordering streams %q and %q",
					event.RunID,
					stream,
					eventStream,
				)
			}
			return nil
		},
	)
	if err != nil {
		return err
	}
	if err := validatePrevious(); err != nil {
		return err
	}
	return store.scanBindings(ctx, func(binding runBindingDocument) error {
		stream, found, err := store.loadRunStream(ctx, binding.RunID)
		if err != nil {
			return fmt.Errorf("load events for run binding %q: %w", binding.RunID, err)
		}
		if !found {
			return fmt.Errorf("runlog binding for run %q has no event", binding.RunID)
		}
		if binding.Stream != stream {
			return fmt.Errorf(
				"runlog run %q binding is %q, want %q",
				binding.RunID,
				binding.Stream,
				stream,
			)
		}
		return nil
	})
}

// validateMigrationCounters ensures a partial migration can be rerun safely
// and a current schema has exactly one counter at each stream maximum.
func validateMigrationCounters(ctx context.Context, store migrationStore, plan migrationPlan) error {
	streams := sortedStreams(plan.maxSequences)
	for _, stream := range streams {
		counter, found, err := store.loadSequence(ctx, stream)
		if err != nil {
			return fmt.Errorf("load runlog sequence %q: %w", stream, err)
		}
		want := plan.maxSequences[stream]
		if plan.current && !found {
			return fmt.Errorf("runlog stream %q is missing its sequence counter", stream)
		}
		if found && (counter.Stream != stream || counter.Sequence != want) {
			return fmt.Errorf(
				"runlog stream %q counter is %d, want %d",
				stream,
				counter.Sequence,
				want,
			)
		}
	}
	return store.scanSequences(ctx, func(counter sequenceDocument) error {
		want, ok := plan.maxSequences[counter.Stream]
		if !ok {
			return fmt.Errorf("runlog sequence %q has no event stream", counter.Stream)
		}
		if counter.Sequence != want {
			return fmt.Errorf(
				"runlog stream %q counter is %d, want %d",
				counter.Stream,
				counter.Sequence,
				want,
			)
		}
		return nil
	})
}

// applyEventBackfills repeats the ObjectID-ordered sequence calculation and
// writes only legacy rows whose ordering fields are still absent.
func applyEventBackfills(ctx context.Context, store migrationStore) error {
	maxSequences := make(map[string]int64)
	return store.scanEvents(ctx, bson.D{{Key: "_id", Value: 1}}, func(event eventDocument) error {
		stream := streamKey(event.RunID, event.SessionID)
		sequence := maxSequences[stream] + 1
		maxSequences[stream] = sequence
		if event.Stream == stream && event.Sequence == sequence {
			return nil
		}
		if err := store.updateEvent(ctx, event, stream, sequence); err != nil {
			return fmt.Errorf("event %s: %w", event.ID.Hex(), err)
		}
		return nil
	})
}

// applyRunBindings writes one deterministic private binding for each run after
// every event ordering field has been validated and backfilled.
func applyRunBindings(ctx context.Context, store migrationStore) error {
	previousRunID := ""
	return store.scanEvents(
		ctx,
		bson.D{{Key: "run_id", Value: 1}, {Key: "_id", Value: 1}},
		func(event eventDocument) error {
			if event.RunID == previousRunID {
				return nil
			}
			previousRunID = event.RunID
			if err := store.setBinding(ctx, event.RunID, streamKey(event.RunID, event.SessionID)); err != nil {
				return fmt.Errorf("run %q: %w", event.RunID, err)
			}
			return nil
		},
	)
}

// sortedStreams returns deterministic counter write order for repeatable
// failures and reruns.
func sortedStreams(maxSequences map[string]int64) []string {
	streams := make([]string, 0, len(maxSequences))
	for stream := range maxSequences {
		streams = append(streams, stream)
	}
	sort.Strings(streams)
	return streams
}

// scanEvents reads only identity and ordering fields. Payload bytes never enter
// migration memory.
func (s mongoMigrationStore) scanEvents(ctx context.Context, sortOrder bson.D, visit func(eventDocument) error) error {
	return scanDocuments(
		ctx,
		s.events,
		bson.M{
			"_id":        1,
			"stream":     1,
			"sequence":   1,
			"event_key":  1,
			"run_id":     1,
			"session_id": 1,
		},
		sortOrder,
		visit,
	)
}

// scanBindings visits private run bindings in run ID order without retaining
// the collection in memory.
func (s mongoMigrationStore) scanBindings(ctx context.Context, visit func(runBindingDocument) error) error {
	return scanDocuments(
		ctx,
		s.bindings,
		bson.M{"_id": 1, "stream": 1},
		bson.D{{Key: "_id", Value: 1}},
		visit,
	)
}

// scanSequences visits private stream counters without retaining the
// collection in memory.
func (s mongoMigrationStore) scanSequences(ctx context.Context, visit func(sequenceDocument) error) error {
	return scanDocuments(
		ctx,
		s.sequences,
		bson.M{"_id": 1, "sequence": 1},
		bson.D{{Key: "_id", Value: 1}},
		visit,
	)
}

// scanDocuments visits projected Mongo documents one at a time and propagates
// cursor decode, callback, iteration, and close errors.
func scanDocuments[T any](
	ctx context.Context,
	collection *mongodriver.Collection,
	projection bson.M,
	sortOrder bson.D,
	visit func(T) error,
) (err error) {
	cursor, err := collection.Find(
		ctx,
		bson.M{},
		options.Find().SetProjection(projection).SetSort(sortOrder),
	)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := cursor.Close(ctx); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	for cursor.Next(ctx) {
		var document T
		if err := cursor.Decode(&document); err != nil {
			return err
		}
		if err := visit(document); err != nil {
			return err
		}
	}
	if err := cursor.Err(); err != nil {
		return err
	}
	return nil
}

// loadSchema returns the current sentinel without treating absence as an error.
func (s mongoMigrationStore) loadSchema(ctx context.Context) (schemaDocument, bool, error) {
	var sentinel schemaDocument
	err := s.schemas.FindOne(ctx, bson.M{"_id": schemaSentinelID}).Decode(&sentinel)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return schemaDocument{}, false, nil
	}
	return sentinel, err == nil, err
}

// loadBinding returns one private run binding without treating absence as an
// error.
func (s mongoMigrationStore) loadBinding(ctx context.Context, runID string) (runBindingDocument, bool, error) {
	var binding runBindingDocument
	err := s.bindings.FindOne(ctx, bson.M{"_id": runID}).Decode(&binding)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return runBindingDocument{}, false, nil
	}
	return binding, err == nil, err
}

// loadRunStream derives one persisted run's expected stream from its first
// event. A separate sorted pass verifies that every event in the run agrees.
func (s mongoMigrationStore) loadRunStream(ctx context.Context, runID string) (string, bool, error) {
	var event eventDocument
	err := s.events.FindOne(
		ctx,
		bson.M{"run_id": runID},
		options.FindOne().
			SetProjection(bson.M{"_id": 1, "run_id": 1, "session_id": 1}).
			SetSort(bson.D{{Key: "_id", Value: 1}}),
	).Decode(&event)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return streamKey(event.RunID, event.SessionID), true, nil
}

// loadSequence returns one stream counter without treating absence as an error.
func (s mongoMigrationStore) loadSequence(ctx context.Context, stream string) (sequenceDocument, bool, error) {
	var counter sequenceDocument
	err := s.sequences.FindOne(ctx, bson.M{"_id": stream}).Decode(&counter)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return sequenceDocument{}, false, nil
	}
	return counter, err == nil, err
}

// updateEvent writes the ordering fields computed from the immutable legacy
// document identity.
func (s mongoMigrationStore) updateEvent(ctx context.Context, event eventDocument, stream string, sequence int64) error {
	result, err := s.events.UpdateOne(
		ctx,
		bson.M{
			"_id":       event.ID,
			"run_id":    event.RunID,
			"event_key": event.EventKey,
		},
		bson.M{"$set": bson.M{"stream": stream, "sequence": sequence}},
	)
	if err != nil {
		return err
	}
	if result.MatchedCount != 1 {
		return errors.New("event changed after migration validation")
	}
	return nil
}

// setBinding creates or confirms the private stream selected by one run's
// persisted session identity.
func (s mongoMigrationStore) setBinding(ctx context.Context, runID, stream string) error {
	_, err := s.bindings.ReplaceOne(
		ctx,
		bson.M{"_id": runID},
		runBindingDocument{RunID: runID, Stream: stream},
		options.Replace().SetUpsert(true),
	)
	return err
}

// setSequence initializes or confirms the next append position for one stream.
func (s mongoMigrationStore) setSequence(ctx context.Context, stream string, sequence int64) error {
	_, err := s.sequences.ReplaceOne(
		ctx,
		bson.M{"_id": stream},
		sequenceDocument{Stream: stream, Sequence: sequence},
		options.Replace().SetUpsert(true),
	)
	return err
}

// setEventValidation installs the event ordering validator at the requested
// level. A missing event collection is created with validation already enabled
// before collMod confirms the final settings.
func (s mongoMigrationStore) setEventValidation(ctx context.Context, level string) error {
	if err := s.runEventCollMod(ctx, level); err == nil {
		return nil
	} else if !hasMongoCommandCode(err, 26) {
		return err
	}

	err := s.database.CreateCollection(
		ctx,
		s.collection,
		options.CreateCollection().
			SetValidator(expectedEventValidation()).
			SetValidationLevel(level).
			SetValidationAction(validationAction),
	)
	if err != nil && !hasMongoCommandCode(err, 48) {
		return fmt.Errorf("create runlog event collection with validation: %w", err)
	}
	if err := s.runEventCollMod(ctx, level); err != nil {
		return fmt.Errorf("confirm runlog event collection validation: %w", err)
	}
	return nil
}

// requireEventValidation verifies that Mongo enforces the exact ordering
// fields at the requested level with rejection rather than warning.
func (s mongoMigrationStore) requireEventValidation(ctx context.Context, level string) error {
	specifications, err := s.database.ListCollectionSpecifications(
		ctx,
		bson.D{{Key: "name", Value: s.collection}},
	)
	if err != nil {
		return err
	}
	if len(specifications) != 1 || specifications[0].Name != s.collection || specifications[0].Type != "collection" {
		return fmt.Errorf("event collection %q is missing", s.collection)
	}
	var actual collectionValidationOptions
	if err := bson.Unmarshal(specifications[0].Options, &actual); err != nil {
		return fmt.Errorf("decode event collection validation options: %w", err)
	}
	if actual.ValidationLevel != level {
		return fmt.Errorf("event validation level is %q, want %q", actual.ValidationLevel, level)
	}
	if actual.ValidationAction != validationAction {
		return fmt.Errorf(
			"event validation action is %q, want %q",
			actual.ValidationAction,
			validationAction,
		)
	}
	if !reflect.DeepEqual(actual.Validator, expectedEventValidation()) {
		return errors.New("event validator does not require string stream and positive int64 sequence")
	}
	return nil
}

// requireEventIndexes verifies every sequence-backed query and uniqueness
// index and rejects the legacy ObjectID cursor indexes.
func (s mongoMigrationStore) requireEventIndexes(ctx context.Context) error {
	specifications, err := s.events.Indexes().ListSpecifications(ctx)
	if err != nil {
		return err
	}
	requirements := requiredEventIndexes()
	found := make([]bool, len(requirements))
	for _, specification := range specifications {
		switch specification.Name {
		case "run_id_1__id_1", "session_id_1__id_1":
			return fmt.Errorf("legacy ObjectID cursor index %q still exists", specification.Name)
		}
		var keys bson.D
		if err := bson.Unmarshal(specification.KeysDocument, &keys); err != nil {
			return fmt.Errorf("decode index %q keys: %w", specification.Name, err)
		}
		unique := specification.Unique != nil && *specification.Unique
		for index, requirement := range requirements {
			if reflect.DeepEqual(keys, requirement.keys) && unique == requirement.unique {
				found[index] = true
			}
		}
	}
	for index, ok := range found {
		if !ok {
			return fmt.Errorf("required event index %v is missing", requirements[index].keys)
		}
	}
	return nil
}

// runEventCollMod applies the storage admission rule to the existing event
// collection.
func (s mongoMigrationStore) runEventCollMod(ctx context.Context, level string) error {
	return s.database.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: s.collection},
		{Key: "validator", Value: expectedEventValidation()},
		{Key: "validationLevel", Value: level},
		{Key: "validationAction", Value: validationAction},
	}).Err()
}

// requireStorageContract verifies the state that must exist immediately before
// the schema sentinel is written and whenever a current migration is rerun.
func requireStorageContract(ctx context.Context, store migrationStore) error {
	if err := store.requireEventValidation(ctx, validationStrict); err != nil {
		return fmt.Errorf("verify strict runlog Mongo event validator: %w", err)
	}
	if err := store.requireEventIndexes(ctx); err != nil {
		return fmt.Errorf("verify runlog Mongo event indexes: %w", err)
	}
	return nil
}

// expectedEventValidation returns the one validator used for moderate admission
// and strict steady-state enforcement.
func expectedEventValidation() eventValidationDocument {
	minimum := int64(1)
	return eventValidationDocument{
		JSONSchema: eventJSONSchema{
			BSONType: "object",
			Required: []string{"stream", "sequence"},
			Properties: eventValidationProperties{
				Stream: eventValidationField{
					BSONType: "string",
				},
				Sequence: eventValidationField{
					BSONType: "long",
					Minimum:  &minimum,
				},
			},
		},
	}
}

// requiredEventIndexes lists the private indexes that make run and session
// replay deterministic and append identities unique.
func requiredEventIndexes() []indexRequirement {
	return []indexRequirement{
		{
			keys: bson.D{
				{Key: "run_id", Value: int32(1)},
				{Key: "sequence", Value: int32(1)},
			},
		},
		{
			keys: bson.D{
				{Key: "session_id", Value: int32(1)},
				{Key: "sequence", Value: int32(1)},
			},
		},
		{
			keys: bson.D{
				{Key: "stream", Value: int32(1)},
				{Key: "sequence", Value: int32(1)},
			},
			unique: true,
		},
		{
			keys: bson.D{
				{Key: "run_id", Value: int32(1)},
				{Key: "event_key", Value: int32(1)},
			},
			unique: true,
		},
	}
}

// hasMongoCommandCode reports whether Mongo returned the named server command
// code.
func hasMongoCommandCode(err error, code int) bool {
	var commandError mongodriver.CommandError
	return errors.As(err, &commandError) && commandError.HasErrorCode(code)
}

// ensureIndexes creates the sequence-backed lookup and identity indexes.
func (s mongoMigrationStore) ensureIndexes(ctx context.Context) error {
	return ensureIndexes(ctx, mongoCollection{coll: s.events})
}

// removeLegacyIndexes deletes the two indexes that made ObjectID part of the
// old replay order. The replacement sequence indexes already exist when this
// method runs.
func (s mongoMigrationStore) removeLegacyIndexes(ctx context.Context) error {
	indexes := s.events.Indexes()
	specifications, err := indexes.ListSpecifications(ctx)
	if err != nil {
		return err
	}
	legacy := map[string]struct{}{
		"run_id_1__id_1":     {},
		"session_id_1__id_1": {},
	}
	for _, specification := range specifications {
		if _, ok := legacy[specification.Name]; !ok {
			continue
		}
		if err := indexes.DropOne(ctx, specification.Name); err != nil {
			return err
		}
	}
	return nil
}

// setSchema marks the cutover complete after every required write succeeds.
func (s mongoMigrationStore) setSchema(ctx context.Context, version int) error {
	_, err := s.schemas.ReplaceOne(
		ctx,
		bson.M{"_id": schemaSentinelID},
		schemaDocument{Name: schemaSentinelID, Version: version},
		options.Replace().SetUpsert(true),
	)
	return err
}
