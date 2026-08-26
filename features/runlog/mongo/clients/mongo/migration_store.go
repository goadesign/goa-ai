// Package mongo uses this file for the MongoDB operations required by run-log
// migration. The migration scans projected documents, applies bounded writes,
// and verifies the validator and indexes before writing the schema sentinel.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type (
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
		SessionID eventValidationField `bson:"session_id"`
		Stream    eventValidationField `bson:"stream"`
		Sequence  eventValidationField `bson:"sequence"`
		Extra     bson.M               `bson:",inline"`
	}

	eventValidationField struct {
		BSONType string `bson:"bsonType"`
		Minimum  *int64 `bson:"minimum,omitempty"`
		Extra    bson.M `bson:",inline"`
	}

	malformedSessionDocument struct {
		ID        bson.ObjectID `bson:"_id"`
		SessionID bson.RawValue `bson:"session_id"`
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

	// eventIndexDocument retains every listIndexes option that can change
	// whether a required event index is safe for replay and append operations.
	eventIndexDocument struct {
		Name                    string        `bson:"name"`
		Keys                    bson.D        `bson:"key"`
		Unique                  bool          `bson:"unique,omitempty"`
		PartialFilterExpression bson.Raw      `bson:"partialFilterExpression,omitempty"`
		Sparse                  bool          `bson:"sparse,omitempty"`
		Hidden                  bool          `bson:"hidden,omitempty"`
		Collation               bson.Raw      `bson:"collation,omitempty"`
		ExpireAfterSeconds      bson.RawValue `bson:"expireAfterSeconds,omitempty"`
		Clustered               bool          `bson:"clustered,omitempty"`
	}
)

// validateSessionIDs asks MongoDB for one event whose session_id is not a
// string. The query also matches absent and null fields, so malformed rows are
// rejected before migration scans can classify them as sessionless events.
func (s mongoMigrationStore) validateSessionIDs(ctx context.Context) error {
	var malformed malformedSessionDocument
	err := s.events.FindOne(
		ctx,
		bson.D{{Key: "session_id", Value: bson.D{
			{Key: "$not", Value: bson.D{{Key: "$type", Value: "string"}}},
		}}},
		options.FindOne().SetProjection(bson.M{"_id": 1, "session_id": 1}),
	).Decode(&malformed)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return nil
	}
	if err != nil {
		return err
	}
	return fmt.Errorf(
		"runlog event %s has non-string session_id BSON type %s",
		malformed.ID.Hex(),
		malformed.SessionID.Type,
	)
}

// scanEvents reads only identity and ordering fields matching one migration
// pass. Payload bytes never enter migration memory.
func (s mongoMigrationStore) scanEvents(ctx context.Context, filter, sortOrder bson.D, visit func(eventDocument) error) error {
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
		filter,
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
		bson.D{},
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
		bson.D{},
		bson.D{{Key: "_id", Value: 1}},
		visit,
	)
}

// scanDocuments visits projected Mongo documents one at a time and propagates
// cursor decode, callback, iteration, and close errors. Mongo may spill a large
// migration sort to disk rather than rejecting a valid collection scan.
func scanDocuments[T any](
	ctx context.Context,
	collection *mongodriver.Collection,
	projection bson.M,
	filter bson.D,
	sortOrder bson.D,
	visit func(T) error,
) (err error) {
	cursor, err := collection.Find(
		ctx,
		filter,
		options.Find().
			SetProjection(projection).
			SetSort(sortOrder).
			SetAllowDiskUse(true),
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

// hasEventStream reports whether a private counter belongs to a persisted
// session stream or sessionless run stream.
func (s mongoMigrationStore) hasEventStream(ctx context.Context, stream string) (bool, error) {
	var filter bson.D
	if sessionID, ok := strings.CutPrefix(stream, "session:"); ok && sessionID != "" {
		filter = bson.D{{Key: "session_id", Value: sessionID}}
	} else if runID, ok := strings.CutPrefix(stream, "run:"); ok && runID != "" {
		filter = bson.D{
			{Key: "run_id", Value: runID},
			{Key: "session_id", Value: ""},
		}
	} else {
		return false, nil
	}
	var event eventDocument
	err := s.events.FindOne(
		ctx,
		filter,
		options.FindOne().SetProjection(bson.M{"_id": 1}),
	).Decode(&event)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return false, nil
	}
	return err == nil, err
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

// setSequence stores the exact final event position observed while appends are
// paused. Replacing the same value makes a partial migration safe to rerun.
func (s mongoMigrationStore) setSequence(ctx context.Context, stream string, sequence int64) error {
	_, err := s.sequences.ReplaceOne(
		ctx,
		bson.M{"_id": stream},
		sequenceDocument{Stream: stream, Sequence: sequence},
		options.Replace().SetUpsert(true),
	)
	return err
}

// setEventValidation installs the final strict event ordering validator. A
// missing event collection is created with validation already enabled before
// collMod confirms the final settings.
func (s mongoMigrationStore) setEventValidation(ctx context.Context) error {
	if err := s.runEventCollMod(ctx); err == nil {
		return nil
	} else if !hasMongoCommandCode(err, 26) {
		return err
	}

	err := s.database.CreateCollection(
		ctx,
		s.collection,
		options.CreateCollection().
			SetValidator(expectedEventValidation()).
			SetValidationLevel(validationStrict).
			SetValidationAction(validationAction),
	)
	if err != nil && !hasMongoCommandCode(err, 48) {
		return fmt.Errorf("create runlog event collection with validation: %w", err)
	}
	if err := s.runEventCollMod(ctx); err != nil {
		return fmt.Errorf("confirm runlog event collection validation: %w", err)
	}
	return nil
}

// requireEventValidation verifies that Mongo strictly enforces the exact
// ordering fields with rejection rather than warning.
func (s mongoMigrationStore) requireEventValidation(ctx context.Context) error {
	specifications, err := s.database.ListCollectionSpecifications(
		ctx,
		bson.D{{Key: "name", Value: s.collection}},
	)
	if err != nil {
		return err
	}
	if len(specifications) != 1 ||
		specifications[0].Name != s.collection ||
		specifications[0].Type != "collection" {
		return fmt.Errorf("event collection %q is missing", s.collection)
	}
	var actual collectionValidationOptions
	if err := bson.Unmarshal(specifications[0].Options, &actual); err != nil {
		return fmt.Errorf("decode event collection validation options: %w", err)
	}
	if actual.ValidationLevel != validationStrict {
		return fmt.Errorf(
			"event validation level is %q, want %q",
			actual.ValidationLevel,
			validationStrict,
		)
	}
	if actual.ValidationAction != validationAction {
		return fmt.Errorf(
			"event validation action is %q, want %q",
			actual.ValidationAction,
			validationAction,
		)
	}
	if !reflect.DeepEqual(actual.Validator, expectedEventValidation()) {
		return errors.New("event validator does not require string session_id and stream with positive int64 sequence")
	}
	return nil
}

// requireEventIndexes verifies every sequence-backed query and uniqueness
// index and rejects the legacy ObjectID cursor indexes.
func (s mongoMigrationStore) requireEventIndexes(ctx context.Context) error {
	cursor, err := s.events.Indexes().List(ctx)
	if err != nil {
		return err
	}
	var indexes []eventIndexDocument
	if err := cursor.All(ctx, &indexes); err != nil {
		return fmt.Errorf("decode runlog event indexes: %w", err)
	}
	return verifyEventIndexes(indexes)
}

// verifyEventIndexes checks that each required key and uniqueness combination
// is backed by an ordinary full index. Options that filter documents, hide the
// index, change comparison rules, or expire records do not satisfy the storage
// contract. Unrelated indexes are outside this contract.
func verifyEventIndexes(indexes []eventIndexDocument) error {
	requirements := requiredEventIndexes()
	found := make([]bool, len(requirements))
	unsuitable := make([]bool, len(requirements))
	for _, indexDocument := range indexes {
		switch indexDocument.Name {
		case "run_id_1__id_1", "session_id_1__id_1":
			return fmt.Errorf("legacy ObjectID cursor index %q still exists", indexDocument.Name)
		}
		for index, requirement := range requirements {
			if !reflect.DeepEqual(indexDocument.Keys, requirement.keys) ||
				indexDocument.Unique != requirement.unique {
				continue
			}
			if eventIndexChangesRequiredBehavior(indexDocument) {
				unsuitable[index] = true
				continue
			}
			found[index] = true
		}
	}
	for index, ok := range found {
		if ok {
			continue
		}
		if unsuitable[index] {
			return fmt.Errorf(
				"required event index %v has behavior-changing options",
				requirements[index].keys,
			)
		}
		return fmt.Errorf("required event index %v is missing", requirements[index].keys)
	}
	return nil
}

// eventIndexChangesRequiredBehavior reports options that make an otherwise
// matching index omit records, become unavailable to queries, compare values
// differently, expire records, or represent clustered collection storage.
func eventIndexChangesRequiredBehavior(index eventIndexDocument) bool {
	return len(index.PartialFilterExpression) > 0 ||
		index.Sparse ||
		index.Hidden ||
		len(index.Collation) > 0 ||
		!index.ExpireAfterSeconds.IsZero() ||
		index.Clustered
}

// runEventCollMod applies the final strict validator to the existing event
// collection.
func (s mongoMigrationStore) runEventCollMod(ctx context.Context) error {
	return s.database.RunCommand(ctx, bson.D{
		{Key: "collMod", Value: s.collection},
		{Key: "validator", Value: expectedEventValidation()},
		{Key: "validationLevel", Value: validationStrict},
		{Key: "validationAction", Value: validationAction},
	}).Err()
}

// requireStorageContract verifies the state that must exist immediately before
// the schema sentinel is written and whenever a current migration is rerun.
func requireStorageContract(ctx context.Context, store migrationStore) error {
	if err := store.requireEventValidation(ctx); err != nil {
		return fmt.Errorf("verify strict runlog Mongo event validator: %w", err)
	}
	if err := store.requireEventIndexes(ctx); err != nil {
		return fmt.Errorf("verify runlog Mongo event indexes: %w", err)
	}
	return nil
}

// expectedEventValidation returns the strict steady-state event validator.
func expectedEventValidation() eventValidationDocument {
	minimum := int64(1)
	return eventValidationDocument{
		JSONSchema: eventJSONSchema{
			BSONType: "object",
			Required: []string{"session_id", "stream", "sequence"},
			Properties: eventValidationProperties{
				SessionID: eventValidationField{
					BSONType: "string",
				},
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
