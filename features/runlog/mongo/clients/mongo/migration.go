// Package mongo provides the MongoDB run-log client and its schema migration.
// Applications run Migrate while no run-log appends occur, then roll out the
// new writers normally. Client startup accepts only the fully migrated ordering
// schema.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
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
		// Apply writes event sequences, run bindings, counters, indexes, the
		// strict validator, and the schema sentinel. False validates storage and
		// reports the required work without writes.
		Apply bool
		// Timeout bounds the complete migration. Zero uses the caller's context
		// without adding another deadline.
		Timeout time.Duration
	}

	// MigrationReport describes the validated ordering migration.
	MigrationReport struct {
		// ExaminedEvents is the number of persisted events checked.
		ExaminedEvents int
		// Streams is the number of session or sessionless-run ordering streams.
		Streams int
		// BackfillEvents is the number of events that need sequence fields.
		BackfillEvents int
		// Applied reports whether this call completed the migration writes.
		Applied bool
		// AlreadyCurrent reports whether this call observed and verified the
		// current schema without running migration write phases.
		AlreadyCurrent bool
	}

	migrationStore interface {
		validateSessionIDs(ctx context.Context) error
		scanEvents(ctx context.Context, filter, sort bson.D, visit func(eventDocument) error) error
		scanBindings(ctx context.Context, visit func(runBindingDocument) error) error
		scanSequences(ctx context.Context, visit func(sequenceDocument) error) error
		loadSchema(ctx context.Context) (schemaDocument, bool, error)
		loadBinding(ctx context.Context, runID string) (runBindingDocument, bool, error)
		loadRunStream(ctx context.Context, runID string) (string, bool, error)
		loadSequence(ctx context.Context, stream string) (sequenceDocument, bool, error)
		hasEventStream(ctx context.Context, stream string) (bool, error)
		updateEvent(ctx context.Context, event eventDocument, stream string, sequence int64) error
		setBinding(ctx context.Context, runID, stream string) error
		setSequence(ctx context.Context, stream string, sequence int64) error
		ensureIndexes(ctx context.Context) error
		removeLegacyIndexes(ctx context.Context) error
		setEventValidation(ctx context.Context) error
		requireEventValidation(ctx context.Context) error
		requireEventIndexes(ctx context.Context) error
		setSchema(ctx context.Context, version int) error
	}

	migrationPlan struct {
		examined  int
		streams   int
		backfills int
	}

	migrationStreamActions struct {
		event  func(eventDocument, string, int64) error
		stream func(string, int64) error
	}
)

const (
	validationStrict = "strict"
	validationAction = "error"
)

// Migrate validates and optionally applies the ordering schema required by New.
// Existing events receive sequence numbers in their current ObjectID order.
// Apply requires callers to prevent run-log appends during the direct state
// transition. The schema sentinel is written only after strict validation and
// every event, binding, counter, and index are ready.
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
	return migrate(ctx, store, opts.Apply)
}

// migrate validates every persisted field before applying deterministic,
// rerunnable write phases.
func migrate(ctx context.Context, store migrationStore, apply bool) (MigrationReport, error) {
	current, err := loadCurrentSchema(ctx, store)
	if err != nil {
		return MigrationReport{}, err
	}
	if current {
		if err := requireStorageContract(ctx, store); err != nil {
			return MigrationReport{}, err
		}
		return MigrationReport{AlreadyCurrent: true}, nil
	}
	if err := store.validateSessionIDs(ctx); err != nil {
		return MigrationReport{}, fmt.Errorf("validate runlog event session identities: %w", err)
	}
	plan, err := buildMigrationPlan(ctx, store)
	if err != nil {
		return MigrationReport{}, err
	}

	report := MigrationReport{
		ExaminedEvents: plan.examined,
		Streams:        plan.streams,
		BackfillEvents: plan.backfills,
	}
	if !apply {
		return report, nil
	}
	if err := applyMigrationStreams(ctx, store); err != nil {
		return MigrationReport{}, fmt.Errorf("backfill runlog event ordering fields: %w", err)
	}
	if err := applyRunBindings(ctx, store); err != nil {
		return MigrationReport{}, fmt.Errorf("backfill runlog run bindings: %w", err)
	}
	if err := store.ensureIndexes(ctx); err != nil {
		return MigrationReport{}, fmt.Errorf("create runlog sequence indexes: %w", err)
	}
	if err := store.removeLegacyIndexes(ctx); err != nil {
		return MigrationReport{}, fmt.Errorf("remove runlog ObjectID cursor indexes: %w", err)
	}
	if err := store.setEventValidation(ctx); err != nil {
		return MigrationReport{}, fmt.Errorf("install strict runlog Mongo event validation: %w", err)
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

// loadCurrentSchema validates the sentinel identity and version, then reports
// whether storage has completed the schema version supported by this client.
func loadCurrentSchema(ctx context.Context, store migrationStore) (bool, error) {
	sentinel, found, err := store.loadSchema(ctx)
	if err != nil {
		return false, fmt.Errorf("load runlog Mongo schema sentinel: %w", err)
	}
	if !found {
		return false, nil
	}
	if sentinel.Name != schemaSentinelID {
		return false, fmt.Errorf("runlog Mongo schema sentinel has unexpected id %q", sentinel.Name)
	}
	if sentinel.Version > schemaVersion {
		return false, fmt.Errorf(
			"runlog Mongo schema version %d is newer than supported version %d",
			sentinel.Version,
			schemaVersion,
		)
	}
	if sentinel.Version < 0 {
		return false, fmt.Errorf("runlog Mongo schema version %d is invalid", sentinel.Version)
	}
	return sentinel.Version == schemaVersion, nil
}

// buildMigrationPlan validates identities, immutable run bindings, ordering
// fields, and counters without retaining event payloads or per-stream state.
func buildMigrationPlan(ctx context.Context, store migrationStore) (migrationPlan, error) {
	if err := validateEventIdentities(ctx, store); err != nil {
		return migrationPlan{}, fmt.Errorf("validate runlog event identities: %w", err)
	}
	if err := validateRunBindings(ctx, store); err != nil {
		return migrationPlan{}, fmt.Errorf("validate runlog run bindings: %w", err)
	}
	plan, err := walkMigrationStreams(ctx, store, migrationStreamActions{})
	if err != nil {
		return migrationPlan{}, fmt.Errorf("validate runlog event ordering fields: %w", err)
	}
	if err := validateNoOrphanCounters(ctx, store); err != nil {
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
		bson.D{},
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
// any persisted private binding. Legacy runs without a binding are backfilled.
func validateRunBindings(ctx context.Context, store migrationStore) error {
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
		bson.D{},
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

// walkMigrationStreams processes session streams and sessionless run streams
// in separate sorted scans. It retains only the current stream and sequence.
func walkMigrationStreams(ctx context.Context, store migrationStore, actions migrationStreamActions) (migrationPlan, error) {
	var plan migrationPlan
	scans := []struct {
		filter bson.D
		sort   bson.D
		stream func(eventDocument) string
	}{
		{
			filter: bson.D{{Key: "session_id", Value: bson.D{
				{Key: "$type", Value: "string"},
				{Key: "$ne", Value: ""},
			}}},
			sort: bson.D{{Key: "session_id", Value: 1}, {Key: "_id", Value: 1}},
			stream: func(event eventDocument) string {
				return "session:" + event.SessionID
			},
		},
		{
			filter: bson.D{{Key: "session_id", Value: bson.D{
				{Key: "$type", Value: "string"},
				{Key: "$eq", Value: ""},
			}}},
			sort: bson.D{{Key: "run_id", Value: 1}, {Key: "_id", Value: 1}},
			stream: func(event eventDocument) string {
				return "run:" + event.RunID
			},
		},
	}
	for _, scan := range scans {
		currentStream := ""
		var sequence int64
		finishStream := func() error {
			if currentStream == "" {
				return nil
			}
			counter, found, err := store.loadSequence(ctx, currentStream)
			if err != nil {
				return fmt.Errorf("load runlog sequence %q: %w", currentStream, err)
			}
			if found && (counter.Stream != currentStream || counter.Sequence != sequence) {
				return fmt.Errorf(
					"runlog stream %q counter is %d, want %d",
					currentStream,
					counter.Sequence,
					sequence,
				)
			}
			if actions.stream != nil {
				if err := actions.stream(currentStream, sequence); err != nil {
					return err
				}
			}
			return nil
		}
		err := store.scanEvents(ctx, scan.filter, scan.sort, func(event eventDocument) error {
			stream := scan.stream(event)
			if stream != currentStream {
				if err := finishStream(); err != nil {
					return err
				}
				currentStream = stream
				sequence = 0
				plan.streams++
			}
			sequence++
			plan.examined++
			switch {
			case event.Stream == "" && event.Sequence == 0:
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
			if actions.event != nil {
				if err := actions.event(event, stream, sequence); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			return migrationPlan{}, err
		}
		if err := finishStream(); err != nil {
			return migrationPlan{}, err
		}
	}
	return plan, nil
}

// validateNoOrphanCounters rejects private counters that do not belong to any
// persisted event stream without retaining the counters or streams in memory.
func validateNoOrphanCounters(ctx context.Context, store migrationStore) error {
	return store.scanSequences(ctx, func(counter sequenceDocument) error {
		found, err := store.hasEventStream(ctx, counter.Stream)
		if err != nil {
			return fmt.Errorf("load events for runlog sequence %q: %w", counter.Stream, err)
		}
		if !found {
			return fmt.Errorf("runlog sequence %q has no event stream", counter.Stream)
		}
		return nil
	})
}

// applyMigrationStreams writes missing ordering fields and each stream's exact
// final counter through the same grouping logic used by migration validation.
func applyMigrationStreams(ctx context.Context, store migrationStore) error {
	_, err := walkMigrationStreams(ctx, store, migrationStreamActions{
		event: func(event eventDocument, stream string, sequence int64) error {
			if event.Stream != "" || event.Sequence != 0 {
				return nil
			}
			if err := store.updateEvent(ctx, event, stream, sequence); err != nil {
				return fmt.Errorf("event %s: %w", event.ID.Hex(), err)
			}
			return nil
		},
		stream: func(stream string, sequence int64) error {
			if err := store.setSequence(ctx, stream, sequence); err != nil {
				return fmt.Errorf("initialize runlog sequence %q: %w", stream, err)
			}
			return nil
		},
	})
	return err
}

// applyRunBindings writes one deterministic private binding for each run after
// every event ordering field has been validated and backfilled.
func applyRunBindings(ctx context.Context, store migrationStore) error {
	previousRunID := ""
	return store.scanEvents(
		ctx,
		bson.D{},
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
