// Package mongo hosts the MongoDB client used by the session store.
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

	"goa.design/goa-ai/agents/runtime/run"
)

const (
	defaultSessionsCollection = "agent_sessions"
	defaultOpTimeout          = 5 * time.Second
	sessionClientName         = "session-mongo"
)

// Client exposes Mongo-backed operations for session metadata.
type Client interface {
	health.Pinger

	UpsertRun(ctx context.Context, run run.Record) error
	LoadRun(ctx context.Context, runID string) (run.Record, error)
}

// Options configures the Mongo session client.
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

// New returns a Client backed by MongoDB.
func New(opts Options) (Client, error) {
	if opts.Client == nil {
		return nil, errors.New("mongo client is required")
	}
	if opts.Database == "" {
		return nil, errors.New("database name is required")
	}
	collection := opts.Collection
	if collection == "" {
		collection = defaultSessionsCollection
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultOpTimeout
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
	return sessionClientName
}

func (c *client) Ping(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	return c.mongo.Ping(ctx, readpref.Primary())
}

func (c *client) UpsertRun(ctx context.Context, run run.Record) error {
	if run.RunID == "" {
		return errors.New("run id is required")
	}
	if run.AgentID == "" {
		return errors.New("agent id is required")
	}
	now := time.Now().UTC()
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = now
	}
	doc := fromRun(run)
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	filter := bson.M{"run_id": run.RunID}
	update := bson.M{
		"$set": doc,
		"$setOnInsert": bson.M{
			"started_at": doc.StartedAt,
		},
	}
	_, err := c.coll.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (c *client) LoadRun(ctx context.Context, runID string) (run.Record, error) {
	if runID == "" {
		return run.Record{}, errors.New("run id is required")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	filter := bson.M{"run_id": runID}
	var doc runDocument
	if err := c.coll.FindOne(ctx, filter).Decode(&doc); err != nil {
		if errors.Is(err, mongodriver.ErrNoDocuments) {
			return run.Record{}, nil
		}
		return run.Record{}, err
	}
	return doc.toRun(), nil
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
	RunID     string            `bson:"run_id"`
	AgentID   string            `bson:"agent_id"`
	SessionID string            `bson:"session_id,omitempty"`
	TurnID    string            `bson:"turn_id,omitempty"`
	Status    run.Status        `bson:"status"`
	StartedAt time.Time         `bson:"started_at"`
	UpdatedAt time.Time         `bson:"updated_at"`
	Labels    map[string]string `bson:"labels,omitempty"`
	Metadata  map[string]any    `bson:"metadata,omitempty"`
}

func fromRun(run run.Record) runDocument {
	return runDocument{
		RunID:     run.RunID,
		AgentID:   run.AgentID,
		SessionID: run.SessionID,
		TurnID:    run.TurnID,
		Status:    run.Status,
		StartedAt: run.StartedAt.UTC(),
		UpdatedAt: run.UpdatedAt.UTC(),
		Labels:    cloneLabels(run.Labels),
		Metadata:  cloneMetadata(run.Metadata),
	}
}

func (doc runDocument) toRun() run.Record {
	return run.Record{
		RunID:     doc.RunID,
		AgentID:   doc.AgentID,
		SessionID: doc.SessionID,
		TurnID:    doc.TurnID,
		Status:    doc.Status,
		StartedAt: doc.StartedAt,
		UpdatedAt: doc.UpdatedAt,
		Labels:    cloneLabels(doc.Labels),
		Metadata:  cloneMetadata(doc.Metadata),
	}
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

func cloneMetadata(src map[string]any) map[string]any {
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
		Keys:    bson.D{{Key: "run_id", Value: 1}},
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
		timeout = defaultOpTimeout
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
