// Package mongo implements the low-level MongoDB client used by the run log store.
package mongo

//go:generate cmg gen .

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"goa.design/clue/health"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
)

type (
	// Client exposes Mongo-backed operations for the run event log.
	Client interface {
		health.Pinger

		Append(ctx context.Context, e *runlog.Event) error
		List(ctx context.Context, runID string, cursor string, limit int) (runlog.Page, error)
	}

	// Options configures the Mongo client implementation.
	Options struct {
		Client     *mongodriver.Client
		Database   string
		Collection string
		Timeout    time.Duration
	}

	client struct {
		mongo   *mongodriver.Client
		coll    collection
		timeout time.Duration
	}

	eventDocument struct {
		ID        primitive.ObjectID `bson:"_id,omitempty"`
		RunID     string             `bson:"run_id"`
		AgentID   string             `bson:"agent_id"`
		SessionID string             `bson:"session_id"`
		TurnID    string             `bson:"turn_id"`
		Type      string             `bson:"type"`
		Payload   []byte             `bson:"payload"`
		Timestamp time.Time          `bson:"timestamp"`
	}
)

const (
	defaultCollection = "agent_run_events"
	defaultTimeout    = 5 * time.Second
	clientName        = "runlog-mongo"
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

	mcoll := opts.Client.Database(opts.Database).Collection(collection)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	wrapper := mongoCollection{coll: mcoll}
	if err := ensureIndexes(ctx, wrapper); err != nil {
		return nil, err
	}
	return newClientWithCollection(opts.Client, wrapper, timeout)
}

func (c *client) Name() string {
	return clientName
}

func (c *client) Ping(ctx context.Context) error {
	return c.mongo.Ping(ctx, readpref.Primary())
}

func (c *client) Append(ctx context.Context, e *runlog.Event) error {
	if e == nil {
		return errors.New("event is required")
	}
	if e.RunID == "" {
		return errors.New("run id is required")
	}
	if e.Type == "" {
		return errors.New("event type is required")
	}
	if e.Timestamp.IsZero() {
		return errors.New("timestamp is required")
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	doc := eventDocument{
		RunID:     e.RunID,
		AgentID:   string(e.AgentID),
		SessionID: e.SessionID,
		TurnID:    e.TurnID,
		Type:      string(e.Type),
		Payload:   append([]byte(nil), e.Payload...),
		Timestamp: e.Timestamp.UTC(),
	}
	res, err := c.coll.InsertOne(ctx, doc)
	if err != nil {
		return err
	}
	oid, ok := res.InsertedID.(primitive.ObjectID)
	if !ok {
		return fmt.Errorf("unexpected inserted id type %T", res.InsertedID)
	}
	e.ID = oid.Hex()
	return nil
}

func (c *client) List(ctx context.Context, runID string, cursor string, limit int) (page runlog.Page, err error) {
	if runID == "" {
		return runlog.Page{}, errors.New("run id is required")
	}
	if limit <= 0 {
		return runlog.Page{}, errors.New("limit must be > 0")
	}

	filter := bson.M{"run_id": runID}
	if cursor != "" {
		oid, err := primitive.ObjectIDFromHex(cursor)
		if err != nil {
			return runlog.Page{}, fmt.Errorf("invalid cursor %q: %w", cursor, err)
		}
		filter["_id"] = bson.M{"$gt": oid}
	}

	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	cur, err := c.coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "_id", Value: 1}}).
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
			ID:        doc.ID.Hex(),
			RunID:     doc.RunID,
			AgentID:   agent.Ident(doc.AgentID),
			SessionID: doc.SessionID,
			TurnID:    doc.TurnID,
			Type:      hooks.EventType(doc.Type),
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

func (c *client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if c.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

func ensureIndexes(ctx context.Context, coll collection) error {
	index := mongodriver.IndexModel{
		Keys: bson.D{
			{Key: "run_id", Value: 1},
			{Key: "_id", Value: 1},
		},
	}
	_, err := coll.Indexes().CreateOne(ctx, index)
	return err
}

func newClientWithCollection(mongoClient *mongodriver.Client, coll collection, timeout time.Duration) (*client, error) {
	if coll == nil {
		return nil, errors.New("collection is required")
	}
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	return &client{
		mongo:   mongoClient,
		coll:    coll,
		timeout: timeout,
	}, nil
}

type collection interface {
	InsertOne(ctx context.Context, document any, opts ...*options.InsertOneOptions) (*mongodriver.InsertOneResult, error)
	Find(ctx context.Context, filter any, opts ...*options.FindOptions) (cursor, error)
	Indexes() indexView
}

type indexView interface {
	CreateOne(ctx context.Context, model mongodriver.IndexModel, opts ...*options.CreateIndexesOptions) (string, error)
}

type cursor interface {
	Next(ctx context.Context) bool
	Decode(val any) error
	Err() error
	Close(ctx context.Context) error
}

type mongoCollection struct {
	coll *mongodriver.Collection
}

func (c mongoCollection) InsertOne(ctx context.Context, document any, opts ...*options.InsertOneOptions) (*mongodriver.InsertOneResult, error) {
	return c.coll.InsertOne(ctx, document, opts...)
}

func (c mongoCollection) Find(ctx context.Context, filter any, opts ...*options.FindOptions) (cursor, error) {
	cur, err := c.coll.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	return mongoCursor{cur: cur}, nil
}

func (c mongoCollection) Indexes() indexView {
	return mongoIndexView{view: c.coll.Indexes()}
}

type mongoCursor struct {
	cur *mongodriver.Cursor
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

type mongoIndexView struct {
	view mongodriver.IndexView
}

func (v mongoIndexView) CreateOne(ctx context.Context, model mongodriver.IndexModel, opts ...*options.CreateIndexesOptions) (string, error) {
	return v.view.CreateOne(ctx, model, opts...)
}
