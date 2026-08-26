// Package mongo provides the MongoDB run-log client and its schema migration.
// Applications run Migrate while run-log writers are stopped, then construct a
// Client. Client startup accepts only the fully migrated sequence schema.
package mongo

import (
	"context"
	"errors"
	"fmt"
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
		// Apply writes event sequences, counters, indexes, and the schema
		// sentinel. False performs validation and reports the required work.
		Apply bool
		// Timeout bounds the complete migration. Zero uses the caller's context
		// without adding another deadline.
		Timeout time.Duration
	}

	// MigrationReport describes the validated sequence migration.
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
		listEvents(ctx context.Context) ([]eventDocument, error)
		loadSchema(ctx context.Context) (schemaDocument, bool, error)
		loadSequence(ctx context.Context, stream string) (sequenceDocument, bool, error)
		updateEvent(ctx context.Context, event eventDocument, stream string, sequence int64) error
		setSequence(ctx context.Context, stream string, sequence int64) error
		ensureIndexes(ctx context.Context) error
		removeLegacyIndexes(ctx context.Context) error
		setSchema(ctx context.Context, version int) error
	}

	migrationPlan struct {
		maxSequences map[string]int64
		backfills    []eventBackfill
		current      bool
	}

	eventBackfill struct {
		event    eventDocument
		stream   string
		sequence int64
	}

	// eventIdentity is the unique persisted key for one run event.
	eventIdentity struct {
		runID    string
		eventKey string
	}

	mongoMigrationStore struct {
		events    *mongodriver.Collection
		sequences *mongodriver.Collection
		schemas   *mongodriver.Collection
	}
)

// Migrate validates and optionally applies the sequence schema required by New.
// Existing events receive sequence numbers in their current ObjectID order.
// Writers must remain stopped until an apply run completes because the schema
// sentinel is written only after every event, counter, and index is ready.
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
		events:    database.Collection(collection),
		sequences: database.Collection(collection + sequenceCollectionSuffix),
		schemas:   database.Collection(collection + schemaCollectionSuffix),
	}
	if opts.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opts.Timeout)
		defer cancel()
	}
	return migrate(ctx, store, opts.Apply)
}

// migrate computes the complete write set before changing storage. A failed
// validation therefore leaves a legacy database untouched.
func migrate(ctx context.Context, store migrationStore, apply bool) (MigrationReport, error) {
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

	events, err := store.listEvents(ctx)
	if err != nil {
		return MigrationReport{}, fmt.Errorf("list runlog events for migration: %w", err)
	}
	current := found && sentinel.Version == schemaVersion
	plan, err := buildMigrationPlan(events, current)
	if err != nil {
		return MigrationReport{}, err
	}
	if err := validateMigrationCounters(ctx, store, plan); err != nil {
		return MigrationReport{}, err
	}

	report := MigrationReport{
		ExaminedEvents: len(events),
		Streams:        len(plan.maxSequences),
		BackfillEvents: len(plan.backfills),
		AlreadyCurrent: current,
	}
	if !apply {
		return report, nil
	}
	for _, backfill := range plan.backfills {
		if err := store.updateEvent(ctx, backfill.event, backfill.stream, backfill.sequence); err != nil {
			return MigrationReport{}, fmt.Errorf(
				"backfill runlog event %s: %w",
				backfill.event.ID.Hex(),
				err,
			)
		}
	}
	for stream, sequence := range plan.maxSequences {
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
	if err := store.setSchema(ctx, schemaVersion); err != nil {
		return MigrationReport{}, fmt.Errorf("write runlog Mongo schema sentinel: %w", err)
	}
	report.Applied = true
	return report, nil
}

// buildMigrationPlan validates persisted identity and ordering fields. Legacy
// events are numbered in ObjectID order; current events must already contain a
// contiguous positive sequence in their declared stream.
func buildMigrationPlan(events []eventDocument, current bool) (migrationPlan, error) {
	plan := migrationPlan{
		maxSequences: make(map[string]int64),
		current:      current,
	}
	runStreams := make(map[string]string)
	identities := make(map[eventIdentity]struct{})
	positions := make(map[string][]int64)
	seenPositions := make(map[string]map[int64]struct{})
	for _, event := range events {
		if event.RunID == "" {
			return migrationPlan{}, fmt.Errorf("runlog event %s has empty run_id", event.ID.Hex())
		}
		if event.EventKey == "" {
			return migrationPlan{}, fmt.Errorf("runlog event %s has empty event_key", event.ID.Hex())
		}
		stream := streamKey(event.RunID, event.SessionID)
		if previous, ok := runStreams[event.RunID]; ok && previous != stream {
			return migrationPlan{}, fmt.Errorf(
				"runlog run %q spans ordering streams %q and %q",
				event.RunID,
				previous,
				stream,
			)
		}
		runStreams[event.RunID] = stream
		identity := eventIdentity{runID: event.RunID, eventKey: event.EventKey}
		if _, ok := identities[identity]; ok {
			return migrationPlan{}, fmt.Errorf(
				"runlog identity (%q, %q) is duplicated",
				event.RunID,
				event.EventKey,
			)
		}
		identities[identity] = struct{}{}

		if current {
			if event.Stream != stream {
				return migrationPlan{}, fmt.Errorf(
					"runlog event %s has stream %q, want %q",
					event.ID.Hex(),
					event.Stream,
					stream,
				)
			}
			if event.Sequence <= 0 {
				return migrationPlan{}, fmt.Errorf(
					"runlog event %s has invalid sequence %d",
					event.ID.Hex(),
					event.Sequence,
				)
			}
			if seenPositions[stream] == nil {
				seenPositions[stream] = make(map[int64]struct{})
			}
			if _, ok := seenPositions[stream][event.Sequence]; ok {
				return migrationPlan{}, fmt.Errorf(
					"runlog stream %q repeats sequence %d",
					stream,
					event.Sequence,
				)
			}
			seenPositions[stream][event.Sequence] = struct{}{}
			positions[stream] = append(positions[stream], event.Sequence)
			plan.maxSequences[stream] = max(plan.maxSequences[stream], event.Sequence)
			continue
		}

		sequence := plan.maxSequences[stream] + 1
		plan.maxSequences[stream] = sequence
		if event.Stream != "" && event.Stream != stream {
			return migrationPlan{}, fmt.Errorf(
				"runlog event %s has conflicting stream %q, want %q",
				event.ID.Hex(),
				event.Stream,
				stream,
			)
		}
		if event.Sequence != 0 && event.Sequence != sequence {
			return migrationPlan{}, fmt.Errorf(
				"runlog event %s has conflicting sequence %d, want %d",
				event.ID.Hex(),
				event.Sequence,
				sequence,
			)
		}
		if event.Stream != stream || event.Sequence != sequence {
			plan.backfills = append(plan.backfills, eventBackfill{
				event:    event,
				stream:   stream,
				sequence: sequence,
			})
		}
	}
	if current {
		for stream, streamPositions := range positions {
			sort.Slice(streamPositions, func(i, j int) bool {
				return streamPositions[i] < streamPositions[j]
			})
			for index, sequence := range streamPositions {
				want := int64(index + 1)
				if sequence != want {
					return migrationPlan{}, fmt.Errorf(
						"runlog stream %q is missing sequence %d",
						stream,
						want,
					)
				}
			}
		}
	}
	return plan, nil
}

// validateMigrationCounters ensures a partial legacy attempt can be rerun
// safely and a current schema cannot start with a counter behind its events.
func validateMigrationCounters(ctx context.Context, store migrationStore, plan migrationPlan) error {
	streams := make([]string, 0, len(plan.maxSequences))
	for stream := range plan.maxSequences {
		streams = append(streams, stream)
	}
	sort.Strings(streams)
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
	return nil
}

// listEvents loads legacy documents in the exact ObjectID order used for
// backfill. Once migrated, ObjectID remains only the Mongo document identity.
func (s mongoMigrationStore) listEvents(ctx context.Context) (events []eventDocument, err error) {
	cursor, err := s.events.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := cursor.Close(ctx); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	for cursor.Next(ctx) {
		var event eventDocument
		if err := cursor.Decode(&event); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if err := cursor.Err(); err != nil {
		return nil, err
	}
	return events, nil
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
