// Package mongo implements the low-level MongoDB client used by the run log
// store. Each append transaction confirms one run's immutable stream binding,
// reserves the next position in that stream, and inserts the event. Event
// ObjectIDs remain MongoDB document identities and never control replay order.
package mongo

//go:generate cmg gen .

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"goa.design/clue/health"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/runlog"
)

type (
	// Client exposes Mongo-backed operations for the run event log.
	Client interface {
		health.Pinger

		Append(ctx context.Context, e *runlog.Event) (runlog.AppendResult, error)
		List(ctx context.Context, runID string, cursor string, limit int) (runlog.Page, error)
		ListSession(ctx context.Context, sessionID string, cursor string, limit int) (runlog.Page, error)
	}

	// Options configures the Mongo client implementation.
	Options struct {
		// Client is the connected MongoDB client.
		Client *mongodriver.Client
		// Database is the database that owns the run-log collections.
		Database string
		// Collection is the event collection name. Empty uses
		// "agent_run_events".
		Collection string
		// Timeout bounds each client operation. Non-positive values use five
		// seconds.
		Timeout time.Duration
	}

	client struct {
		mongo        *mongodriver.Client
		coll         collection
		sequences    collection
		bindings     collection
		transactions transactionRunner
		timeout      time.Duration
	}

	eventDocument struct {
		ID        bson.ObjectID `bson:"_id,omitempty"`
		Stream    string        `bson:"stream"`
		Sequence  int64         `bson:"sequence"`
		EventKey  string        `bson:"event_key"`
		RunID     string        `bson:"run_id"`
		AgentID   string        `bson:"agent_id"`
		SessionID string        `bson:"session_id"`
		TurnID    string        `bson:"turn_id"`
		Type      string        `bson:"type"`
		Payload   []byte        `bson:"payload"`
		Timestamp time.Time     `bson:"timestamp"`
	}

	sequenceDocument struct {
		Stream   string `bson:"_id"`
		Sequence int64  `bson:"sequence"`
	}

	runBindingDocument struct {
		RunID  string `bson:"_id"`
		Stream string `bson:"stream"`
	}

	schemaDocument struct {
		Name    string `bson:"_id"`
		Version int    `bson:"version"`
	}

	collection interface {
		InsertOne(ctx context.Context, document any, opts ...options.Lister[options.InsertOneOptions]) (*mongodriver.InsertOneResult, error)
		FindOne(ctx context.Context, filter any, opts ...options.Lister[options.FindOneOptions]) singleResult
		FindOneAndUpdate(ctx context.Context, filter, update any, opts ...options.Lister[options.FindOneAndUpdateOptions]) singleResult
		Find(ctx context.Context, filter any, opts ...options.Lister[options.FindOptions]) (cursor, error)
		Indexes() indexView
	}

	transactionRunner interface {
		WithTransaction(ctx context.Context, fn func(context.Context) error) error
	}

	indexView interface {
		CreateOne(ctx context.Context, model mongodriver.IndexModel, opts ...options.Lister[options.CreateIndexesOptions]) (string, error)
	}

	cursor interface {
		Next(ctx context.Context) bool
		Decode(val any) error
		Err() error
		Close(ctx context.Context) error
	}

	singleResult interface {
		Decode(val any) error
	}

	storageContractReader interface {
		requireEventValidation(ctx context.Context, level string) error
		requireEventIndexes(ctx context.Context) error
	}

	mongoCollection struct {
		coll *mongodriver.Collection
	}

	mongoTransactionRunner struct {
		client *mongodriver.Client
	}

	mongoCursor struct {
		cur *mongodriver.Cursor
	}

	mongoIndexView struct {
		view mongodriver.IndexView
	}
)

const (
	defaultCollection        = "agent_run_events"
	defaultTimeout           = 5 * time.Second
	clientName               = "runlog-mongo"
	sequenceCollectionSuffix = "_sequences"
	bindingCollectionSuffix  = "_run_bindings"
	schemaCollectionSuffix   = "_schema"
	schemaSentinelID         = "runlog"
	schemaVersion            = 2
)

// New returns a Client backed by the provided MongoDB client.
func New(opts Options) (Client, error) {
	if opts.Client == nil {
		return nil, errors.New("mongo client is required")
	}
	if opts.Database == "" {
		return nil, errors.New("database name is required")
	}
	collection := opts.Collection
	if collection == "" {
		collection = defaultCollection
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	database := opts.Client.Database(opts.Database)
	mcoll := database.Collection(collection)
	sequenceColl := database.Collection(collection + sequenceCollectionSuffix)
	bindingColl := database.Collection(collection + bindingCollectionSuffix)
	schemaColl := database.Collection(collection + schemaCollectionSuffix)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	wrapper := mongoCollection{coll: mcoll}
	if err := requireReadyStorage(ctx, mongoCollection{coll: schemaColl}, mongoMigrationStore{
		database:   database,
		events:     mcoll,
		collection: collection,
	}); err != nil {
		return nil, err
	}
	return newClientWithCollections(
		opts.Client,
		wrapper,
		mongoCollection{coll: sequenceColl},
		mongoCollection{coll: bindingColl},
		mongoTransactionRunner{client: opts.Client},
		timeout,
	)
}

// Name returns the identifier used by health reporting.
func (c *client) Name() string {
	return clientName
}

// Ping verifies that the MongoDB primary is reachable.
func (c *client) Ping(ctx context.Context) error {
	return c.mongo.Ping(ctx, readpref.Primary())
}

// Append confirms the run's stream, reserves one position, and inserts the
// immutable event in the same transaction. Exact activity retries return the
// existing position.
func (c *client) Append(ctx context.Context, e *runlog.Event) (runlog.AppendResult, error) {
	if e == nil {
		return runlog.AppendResult{}, errors.New("event is required")
	}
	if e.RunID == "" {
		return runlog.AppendResult{}, errors.New("run id is required")
	}
	if e.EventKey == "" {
		return runlog.AppendResult{}, errors.New("event key is required")
	}
	if e.Type == "" {
		return runlog.AppendResult{}, errors.New("event type is required")
	}
	if e.Timestamp.IsZero() {
		return runlog.AppendResult{}, errors.New("timestamp is required")
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	doc := eventDocument{
		Stream:    streamKey(e.RunID, e.SessionID),
		EventKey:  e.EventKey,
		RunID:     e.RunID,
		AgentID:   string(e.AgentID),
		SessionID: e.SessionID,
		TurnID:    e.TurnID,
		Type:      string(e.Type),
		Payload:   append([]byte(nil), e.Payload...),
		Timestamp: e.Timestamp.UTC(),
	}
	err := c.transactions.WithTransaction(ctx, func(txCtx context.Context) error {
		if err := c.bindRunStream(txCtx, e.RunID, doc.Stream); err != nil {
			return err
		}
		sequence, err := c.nextSequence(txCtx, doc.Stream)
		if err != nil {
			return err
		}
		doc.Sequence = sequence
		res, err := c.coll.InsertOne(txCtx, doc)
		if err != nil {
			return err
		}
		if _, ok := res.InsertedID.(bson.ObjectID); !ok {
			return fmt.Errorf("unexpected inserted id type %T", res.InsertedID)
		}
		return nil
	})
	if err != nil {
		if mongodriver.IsDuplicateKeyError(err) {
			existing, lookupErr := c.lookupEventByKey(ctx, e.RunID, e.EventKey)
			if lookupErr != nil {
				if errors.Is(lookupErr, mongodriver.ErrNoDocuments) {
					return runlog.AppendResult{}, err
				}
				return runlog.AppendResult{}, lookupErr
			}
			if !sameEventDocument(existing, doc) {
				return runlog.AppendResult{}, fmt.Errorf("event key %q conflicts with existing event body", e.EventKey)
			}
			e.ID = strconv.FormatInt(existing.Sequence, 10)
			return runlog.AppendResult{ID: e.ID, Inserted: false}, nil
		}
		return runlog.AppendResult{}, err
	}
	e.ID = strconv.FormatInt(doc.Sequence, 10)
	return runlog.AppendResult{ID: e.ID, Inserted: true}, nil
}

// List returns one oldest-first page for a run using a sequence cursor.
func (c *client) List(ctx context.Context, runID string, cursor string, limit int) (page runlog.Page, err error) {
	if runID == "" {
		return runlog.Page{}, errors.New("run id is required")
	}
	if limit <= 0 {
		return runlog.Page{}, errors.New("limit must be > 0")
	}
	return c.listWithFilter(ctx, bson.M{"run_id": runID}, cursor, limit)
}

// ListSession returns one oldest-first page across all runs in a session.
func (c *client) ListSession(ctx context.Context, sessionID string, cursor string, limit int) (page runlog.Page, err error) {
	if sessionID == "" {
		return runlog.Page{}, errors.New("session id is required")
	}
	if limit <= 0 {
		return runlog.Page{}, errors.New("limit must be > 0")
	}
	return c.listWithFilter(ctx, bson.M{"session_id": sessionID}, cursor, limit)
}

// withTimeout applies the configured per-operation deadline.
func (c *client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

// listWithFilter applies one cursor-scoped forward scan over the event collection.
func (c *client) listWithFilter(ctx context.Context, filter bson.M, cursor string, limit int) (page runlog.Page, err error) {
	if cursor != "" {
		sequence, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil {
			return runlog.Page{}, fmt.Errorf("invalid cursor %q: %w", cursor, err)
		}
		if sequence <= 0 {
			return runlog.Page{}, fmt.Errorf("invalid cursor %q: must identify a stored event", cursor)
		}
		filter["sequence"] = bson.M{"$gt": sequence}
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	cur, err := c.coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "sequence", Value: 1}}).
		SetLimit(int64(limit+1)),
	)
	if err != nil {
		return runlog.Page{}, err
	}
	defer func() {
		if cerr := cur.Close(ctx); err == nil && cerr != nil {
			err = cerr
		}
	}()

	var events []*runlog.Event
	for cur.Next(ctx) {
		var doc eventDocument
		if err := cur.Decode(&doc); err != nil {
			return runlog.Page{}, err
		}
		events = append(events, &runlog.Event{
			ID:        strconv.FormatInt(doc.Sequence, 10),
			EventKey:  doc.EventKey,
			RunID:     doc.RunID,
			AgentID:   agent.Ident(doc.AgentID),
			SessionID: doc.SessionID,
			TurnID:    doc.TurnID,
			Type:      runlog.Type(doc.Type),
			Payload:   append([]byte(nil), doc.Payload...),
			Timestamp: doc.Timestamp,
		})
	}
	if err := cur.Err(); err != nil {
		return runlog.Page{}, err
	}

	var next string
	if len(events) > limit {
		next = events[limit-1].ID
		events = events[:limit]
	}
	return runlog.Page{
		Events:     events,
		NextCursor: next,
	}, nil
}

// ensureIndexes creates the query and uniqueness indexes required by
// sequence-only replay.
func ensureIndexes(ctx context.Context, coll collection) error {
	cursorIndex := mongodriver.IndexModel{
		Keys: bson.D{
			{Key: "run_id", Value: 1},
			{Key: "sequence", Value: 1},
		},
	}
	if _, err := coll.Indexes().CreateOne(ctx, cursorIndex); err != nil {
		return err
	}
	sessionCursorIndex := mongodriver.IndexModel{
		Keys: bson.D{
			{Key: "session_id", Value: 1},
			{Key: "sequence", Value: 1},
		},
	}
	if _, err := coll.Indexes().CreateOne(ctx, sessionCursorIndex); err != nil {
		return err
	}
	streamSequenceIndex := mongodriver.IndexModel{
		Keys: bson.D{
			{Key: "stream", Value: 1},
			{Key: "sequence", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}
	if _, err := coll.Indexes().CreateOne(ctx, streamSequenceIndex); err != nil {
		return err
	}
	identityIndex := mongodriver.IndexModel{
		Keys: bson.D{
			{Key: "run_id", Value: 1},
			{Key: "event_key", Value: 1},
		},
		Options: options.Index().SetUnique(true),
	}
	_, err := coll.Indexes().CreateOne(ctx, identityIndex)
	return err
}

// newClientWithCollections assembles the client from storage operations. Tests
// supply in-memory implementations that preserve the same transaction contract.
func newClientWithCollections(
	mongoClient *mongodriver.Client,
	coll collection,
	sequences collection,
	bindings collection,
	transactions transactionRunner,
	timeout time.Duration,
) (*client, error) {
	if coll == nil {
		return nil, errors.New("collection is required")
	}
	if sequences == nil {
		return nil, errors.New("sequence collection is required")
	}
	if bindings == nil {
		return nil, errors.New("run binding collection is required")
	}
	if transactions == nil {
		return nil, errors.New("transaction runner is required")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &client{
		mongo:        mongoClient,
		coll:         coll,
		sequences:    sequences,
		bindings:     bindings,
		transactions: transactions,
		timeout:      timeout,
	}, nil
}

func (c mongoCollection) InsertOne(ctx context.Context, document any, opts ...options.Lister[options.InsertOneOptions]) (*mongodriver.InsertOneResult, error) {
	return c.coll.InsertOne(ctx, document, opts...)
}

func (c mongoCollection) Find(ctx context.Context, filter any, opts ...options.Lister[options.FindOptions]) (cursor, error) {
	cur, err := c.coll.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	return mongoCursor{cur: cur}, nil
}

func (c mongoCollection) FindOne(ctx context.Context, filter any, opts ...options.Lister[options.FindOneOptions]) singleResult {
	return c.coll.FindOne(ctx, filter, opts...)
}

func (c mongoCollection) FindOneAndUpdate(ctx context.Context, filter, update any, opts ...options.Lister[options.FindOneAndUpdateOptions]) singleResult {
	return c.coll.FindOneAndUpdate(ctx, filter, update, opts...)
}

func (c mongoCollection) Indexes() indexView {
	return mongoIndexView{view: c.coll.Indexes()}
}

// WithTransaction runs binding confirmation, sequence allocation, and event
// insertion in one Mongo transaction so a visible event always owns exactly
// one stream position.
func (r mongoTransactionRunner) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	session, err := r.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(txCtx context.Context) (any, error) {
		return nil, fn(txCtx)
	})
	return err
}

func (c mongoCursor) Next(ctx context.Context) bool {
	return c.cur.Next(ctx)
}

func (c mongoCursor) Decode(val any) error {
	return c.cur.Decode(val)
}

func (c mongoCursor) Err() error {
	return c.cur.Err()
}

func (c mongoCursor) Close(ctx context.Context) error {
	return c.cur.Close(ctx)
}

func (v mongoIndexView) CreateOne(ctx context.Context, model mongodriver.IndexModel, opts ...options.Lister[options.CreateIndexesOptions]) (string, error) {
	return v.view.CreateOne(ctx, model, opts...)
}

// lookupEventByKey loads the first successful append after a unique-key error
// so Append can distinguish an exact retry from a conflicting event body.
func (c *client) lookupEventByKey(ctx context.Context, runID string, eventKey string) (eventDocument, error) {
	var doc eventDocument
	err := c.coll.FindOne(ctx, bson.M{
		"run_id":    runID,
		"event_key": eventKey,
	}).Decode(&doc)
	if err != nil {
		return eventDocument{}, err
	}
	return doc, nil
}

// nextSequence advances the counter for one session or one sessionless run.
// The caller's transaction also inserts the event that receives this position.
func (c *client) nextSequence(ctx context.Context, stream string) (int64, error) {
	var counter sequenceDocument
	err := c.sequences.FindOneAndUpdate(
		ctx,
		bson.M{"_id": stream},
		bson.M{"$inc": bson.M{"sequence": int64(1)}},
		options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After),
	).Decode(&counter)
	if err != nil {
		return 0, err
	}
	if counter.Sequence <= 0 {
		return 0, fmt.Errorf("stream %q returned invalid sequence %d", stream, counter.Sequence)
	}
	return counter.Sequence, nil
}

// bindRunStream creates the first run-to-stream binding or confirms the
// existing binding. The caller's transaction also allocates the sequence and
// inserts the event, so a rolled-back first append leaves no binding behind.
func (c *client) bindRunStream(ctx context.Context, runID, stream string) error {
	var binding runBindingDocument
	err := c.bindings.FindOneAndUpdate(
		ctx,
		bson.M{"_id": runID},
		bson.M{"$setOnInsert": bson.M{"stream": stream}},
		options.FindOneAndUpdate().
			SetUpsert(true).
			SetReturnDocument(options.After),
	).Decode(&binding)
	if err != nil {
		return fmt.Errorf("bind run %q to ordering stream %q: %w", runID, stream, err)
	}
	if binding.RunID != runID || binding.Stream != stream {
		return fmt.Errorf(
			"run %q is bound to ordering stream %q, cannot append to %q",
			runID,
			binding.Stream,
			stream,
		)
	}
	return nil
}

// requireSchema rejects databases that have not completed the ordering
// migration. The migration writes this sentinel only after all event documents,
// run bindings, counters, and indexes are ready for sequence-only reads.
func requireSchema(ctx context.Context, schemas collection) error {
	var sentinel schemaDocument
	err := schemas.FindOne(ctx, bson.M{"_id": schemaSentinelID}).Decode(&sentinel)
	if errors.Is(err, mongodriver.ErrNoDocuments) {
		return fmt.Errorf("runlog Mongo schema migration %d is required", schemaVersion)
	}
	if err != nil {
		return fmt.Errorf("load runlog Mongo schema sentinel: %w", err)
	}
	if sentinel.Name != schemaSentinelID || sentinel.Version != schemaVersion {
		return fmt.Errorf(
			"runlog Mongo schema version %d is required, found %d",
			schemaVersion,
			sentinel.Version,
		)
	}
	return nil
}

// requireReadyStorage accepts startup only after migration has installed the
// strict event validator and every required replay index, then written the
// current schema sentinel.
func requireReadyStorage(ctx context.Context, schemas collection, storage storageContractReader) error {
	if err := requireSchema(ctx, schemas); err != nil {
		return err
	}
	if err := storage.requireEventValidation(ctx, validationStrict); err != nil {
		return fmt.Errorf("verify runlog Mongo event validator: %w", err)
	}
	if err := storage.requireEventIndexes(ctx); err != nil {
		return fmt.Errorf("verify runlog Mongo event indexes: %w", err)
	}
	return nil
}

// streamKey selects the only ordering stream that may assign an event's
// sequence. Session-backed runs share one stream; one-shot runs use their run.
func streamKey(runID, sessionID string) string {
	if sessionID != "" {
		return "session:" + sessionID
	}
	return "run:" + runID
}

// sameEventDocument compares the immutable caller-owned event body. Sequence,
// ObjectID, and retry-attempt timestamp are assigned by the first successful
// append and therefore do not participate in retry equality.
func sameEventDocument(existing eventDocument, candidate eventDocument) bool {
	return existing.EventKey == candidate.EventKey &&
		existing.RunID == candidate.RunID &&
		existing.AgentID == candidate.AgentID &&
		existing.SessionID == candidate.SessionID &&
		existing.TurnID == candidate.TurnID &&
		existing.Type == candidate.Type &&
		bytes.Equal(existing.Payload, candidate.Payload)
}
