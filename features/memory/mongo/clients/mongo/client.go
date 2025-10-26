// Package mongo implements the low-level MongoDB client used by the memory store.
package mongo

//go:generate cmg gen .

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
	"go.mongodb.org/mongo-driver/mongo/readpref"

	"goa.design/clue/health"

	"goa.design/goa-ai/agents/runtime/memory"
)

const (
	defaultCollection = "agent_memory"
	defaultTimeout    = 5 * time.Second
	clientName        = "memory-mongo"
)

// Client exposes Mongo-backed operations for memory snapshots.
type Client interface {
	health.Pinger

	LoadRun(ctx context.Context, agentID, runID string) (memory.Snapshot, error)
	AppendEvents(ctx context.Context, agentID, runID string, events []memory.Event) error
}

// Options configures the Mongo client implementation.
type Options struct {
	Client     *mongodriver.Client
	Database   string
	Collection string
	Timeout    time.Duration
}

type client struct {
	mongo   *mongodriver.Client
	coll    collection
	timeout time.Duration
}

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
	if ctx == nil {
		ctx = context.Background()
	}
	return c.mongo.Ping(ctx, readpref.Primary())
}

func (c *client) LoadRun(ctx context.Context, agentID, runID string) (memory.Snapshot, error) {
	if agentID == "" {
		return memory.Snapshot{}, errors.New("agent id is required")
	}
	if runID == "" {
		return memory.Snapshot{}, errors.New("run id is required")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	filter := bson.M{"agent_id": agentID, "run_id": runID}
	var doc runDocument
	if err := c.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		if errors.Is(err, mongodriver.ErrNoDocuments) {
			return memory.Snapshot{
				AgentID: agentID,
				RunID:   runID,
				Meta:    make(map[string]any),
			}, nil
		}
		return memory.Snapshot{}, err
	}
	return memory.Snapshot{
		AgentID: agentID,
		RunID:   runID,
		Events:  fromEventDocuments(doc.Events),
		Meta:    cloneMeta(doc.Meta),
	}, nil
}

func (c *client) AppendEvents(ctx context.Context, agentID, runID string, events []memory.Event) error {
	if agentID == "" {
		return errors.New("agent id is required")
	}
	if runID == "" {
		return errors.New("run id is required")
	}
	if len(events) == 0 {
		return nil
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	now := time.Now().UTC()
	docs := toEventDocuments(events, now)
	filter := bson.M{"agent_id": agentID, "run_id": runID}
	update := bson.M{
		"$setOnInsert": bson.M{
			"agent_id": agentID,
			"run_id":   runID,
			"events":   []eventDocument{},
		},
		"$set": bson.M{
			"updated_at": now,
		},
		"$push": bson.M{
			"events": bson.M{
				"$each": docs,
			},
		},
	}
	_, err := c.coll.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (c *client) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.timeout <= 0 {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, c.timeout)
}

type runDocument struct {
	AgentID   string          `bson:"agent_id"`
	RunID     string          `bson:"run_id"`
	Events    []eventDocument `bson:"events"`
	Meta      map[string]any  `bson:"meta,omitempty"`
	UpdatedAt time.Time       `bson:"updated_at,omitempty"`
}

type eventDocument struct {
	Type      memory.EventType  `bson:"type"`
	Timestamp time.Time         `bson:"timestamp"`
	Data      any               `bson:"data,omitempty"`
	Labels    map[string]string `bson:"labels,omitempty"`
}

func toEventDocuments(events []memory.Event, fallback time.Time) []eventDocument {
	result := make([]eventDocument, len(events))
	for i, evt := range events {
		ts := evt.Timestamp
		if ts.IsZero() {
			ts = fallback
		}
		result[i] = eventDocument{
			Type:      evt.Type,
			Timestamp: ts.UTC(),
			Data:      evt.Data,
			Labels:    cloneLabels(evt.Labels),
		}
	}
	return result
}

func fromEventDocuments(events []eventDocument) []memory.Event {
	if len(events) == 0 {
		return nil
	}
	result := make([]memory.Event, len(events))
	for i, evt := range events {
		result[i] = memory.Event{
			Type:      evt.Type,
			Timestamp: evt.Timestamp,
			Data:      evt.Data,
			Labels:    cloneLabels(evt.Labels),
		}
	}
	return result
}

func cloneLabels(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func cloneMeta(src map[string]any) map[string]any {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]any, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func ensureIndexes(ctx context.Context, coll collection) error {
	index := mongodriver.IndexModel{
		Keys:    bson.D{{Key: "agent_id", Value: 1}, {Key: "run_id", Value: 1}},
		Options: options.Index().SetUnique(true),
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
	FindOne(ctx context.Context, filter any, opts ...*options.FindOneOptions) singleResult
	UpdateOne(ctx context.Context, filter any, update any, opts ...*options.UpdateOptions) (*mongodriver.UpdateResult, error)
	Indexes() indexView
}

type indexView interface {
	CreateOne(ctx context.Context, model mongodriver.IndexModel, opts ...*options.CreateIndexesOptions) (string, error)
}

type singleResult interface {
	Decode(val any) error
}

type mongoCollection struct {
	coll *mongodriver.Collection
}

func (c mongoCollection) FindOne(ctx context.Context, filter any, opts ...*options.FindOneOptions) singleResult {
	return mongoSingleResult{res: c.coll.FindOne(ctx, filter, opts...)}
}

func (c mongoCollection) UpdateOne(ctx context.Context, filter any, update any, opts ...*options.UpdateOptions) (*mongodriver.UpdateResult, error) {
	return c.coll.UpdateOne(ctx, filter, update, opts...)
}

func (c mongoCollection) Indexes() indexView {
	return mongoIndexView{view: c.coll.Indexes()}
}

type mongoSingleResult struct {
	res *mongodriver.SingleResult
}

func (r mongoSingleResult) Decode(val any) error {
	return r.res.Decode(val)
}

type mongoIndexView struct {
	view mongodriver.IndexView
}

func (v mongoIndexView) CreateOne(ctx context.Context, model mongodriver.IndexModel, opts ...*options.CreateIndexesOptions) (string, error) {
	return v.view.CreateOne(ctx, model, opts...)
}
