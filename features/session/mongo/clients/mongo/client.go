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

	"goa.design/goa-ai/runtime/agent/session"
)

const (
	defaultSessionsCollection = "agent_sessions"
	defaultRunsCollection     = "agent_runs"
	defaultOpTimeout          = 5 * time.Second
	sessionClientName         = "session-mongo"
)

// Client exposes Mongo-backed operations for session metadata.
type Client interface {
	health.Pinger

	CreateSession(ctx context.Context, sessionID string, createdAt time.Time) (session.Session, error)
	LoadSession(ctx context.Context, sessionID string) (session.Session, error)
	EndSession(ctx context.Context, sessionID string, endedAt time.Time) (session.Session, error)

	UpsertRun(ctx context.Context, run session.RunMeta) error
	LoadRun(ctx context.Context, runID string) (session.RunMeta, error)
	ListRunsBySession(ctx context.Context, sessionID string, statuses []session.RunStatus) ([]session.RunMeta, error)
}

// Options configures the Mongo session client.
type Options struct {
	Client             *mongodriver.Client
	Database           string
	SessionsCollection string
	RunsCollection     string
	Timeout            time.Duration
}

type client struct {
	mongo    *mongodriver.Client
	sessions collection
	runs     collection
	timeout  time.Duration
}

// New returns a Client backed by MongoDB.
func New(opts Options) (Client, error) {
	if opts.Client == nil {
		return nil, errors.New("mongo client is required")
	}
	if opts.Database == "" {
		return nil, errors.New("database name is required")
	}
	sessionsCollection := opts.SessionsCollection
	if sessionsCollection == "" {
		sessionsCollection = defaultSessionsCollection
	}
	runsCollection := opts.RunsCollection
	if runsCollection == "" {
		runsCollection = defaultRunsCollection
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultOpTimeout
	}
	sessColl := opts.Client.Database(opts.Database).Collection(sessionsCollection)
	runColl := opts.Client.Database(opts.Database).Collection(runsCollection)
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	sessWrapper := mongoCollection{coll: sessColl}
	runWrapper := mongoCollection{coll: runColl}
	if err := ensureIndexes(ctx, sessWrapper, runWrapper); err != nil {
		return nil, err
	}
	return newClientWithCollections(opts.Client, sessWrapper, runWrapper, timeout)
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

func (c *client) CreateSession(ctx context.Context, sessionID string, createdAt time.Time) (session.Session, error) {
	if sessionID == "" {
		return session.Session{}, errors.New("session id is required")
	}
	if createdAt.IsZero() {
		return session.Session{}, errors.New("created_at is required")
	}

	existing, err := c.LoadSession(ctx, sessionID)
	if err == nil {
		if existing.Status == session.StatusEnded {
			return session.Session{}, session.ErrSessionEnded
		}
		return existing, nil
	}
	if !errors.Is(err, session.ErrSessionNotFound) {
		return session.Session{}, err
	}

	now := time.Now().UTC()
	createdAt = createdAt.UTC()
	ctxWithTimeout, cancel := c.withTimeout(ctx)
	defer cancel()
	filter := bson.M{"session_id": sessionID}
	update := bson.M{
		// Idempotent insert: CreateSession must never modify an existing session.
		//
		// MongoDB rejects updates that set the same path in multiple update
		// operators (e.g. created_at in both $set and $setOnInsert). Keeping this
		// as a pure $setOnInsert update avoids that class of bugs and makes
		// CreateSession safe under retries and races.
		"$setOnInsert": bson.M{
			"session_id": sessionID,
			"status":     session.StatusActive,
			"created_at": createdAt,
			"updated_at": now,
		},
	}
	if _, err := c.sessions.UpdateOne(ctxWithTimeout, filter, update, options.Update().SetUpsert(true)); err != nil {
		return session.Session{}, err
	}

	out, err := c.LoadSession(ctx, sessionID)
	if err != nil {
		return session.Session{}, err
	}
	if out.Status == session.StatusEnded {
		return session.Session{}, session.ErrSessionEnded
	}
	return out, nil
}

func (c *client) LoadSession(ctx context.Context, sessionID string) (session.Session, error) {
	if sessionID == "" {
		return session.Session{}, errors.New("session id is required")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	filter := bson.M{"session_id": sessionID}
	var doc sessionDocument
	if err := c.sessions.FindOne(ctx, filter).Decode(&doc); err != nil {
		if errors.Is(err, mongodriver.ErrNoDocuments) {
			return session.Session{}, session.ErrSessionNotFound
		}
		return session.Session{}, err
	}
	return doc.toSession(), nil
}

func (c *client) EndSession(ctx context.Context, sessionID string, endedAt time.Time) (session.Session, error) {
	if sessionID == "" {
		return session.Session{}, errors.New("session id is required")
	}
	if endedAt.IsZero() {
		return session.Session{}, errors.New("ended_at is required")
	}

	existing, err := c.LoadSession(ctx, sessionID)
	if err != nil {
		return session.Session{}, err
	}
	if existing.Status == session.StatusEnded {
		return existing, nil
	}

	now := time.Now().UTC()
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	filter := bson.M{"session_id": sessionID}
	update := bson.M{
		"$set": bson.M{
			"status":     session.StatusEnded,
			"ended_at":   endedAt.UTC(),
			"updated_at": now,
		},
	}
	if _, err := c.sessions.UpdateOne(ctx, filter, update); err != nil {
		return session.Session{}, err
	}
	return c.LoadSession(ctx, sessionID)
}

func (c *client) UpsertRun(ctx context.Context, run session.RunMeta) error {
	if run.RunID == "" {
		return errors.New("run id is required")
	}
	if run.AgentID == "" {
		return errors.New("agent id is required")
	}
	if run.SessionID == "" {
		return errors.New("session id is required")
	}
	now := time.Now().UTC()
	if run.StartedAt.IsZero() {
		run.StartedAt = now
	}
	run.UpdatedAt = now
	doc := fromRunMeta(run)
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	filter := bson.M{"run_id": run.RunID}
	update := bson.M{
		"$set": bson.M{
			"run_id":     doc.RunID,
			"agent_id":   doc.AgentID,
			"session_id": doc.SessionID,
			"status":     doc.Status,
			"updated_at": doc.UpdatedAt,
			"labels":     doc.Labels,
			"metadata":   doc.Metadata,
		},
		"$setOnInsert": bson.M{
			"started_at": doc.StartedAt,
		},
	}
	_, err := c.runs.UpdateOne(ctx, filter, update, options.Update().SetUpsert(true))
	return err
}

func (c *client) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	if runID == "" {
		return session.RunMeta{}, errors.New("run id is required")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	filter := bson.M{"run_id": runID}
	var doc runDocument
	if err := c.runs.FindOne(ctx, filter).Decode(&doc); err != nil {
		if errors.Is(err, mongodriver.ErrNoDocuments) {
			return session.RunMeta{}, session.ErrRunNotFound
		}
		return session.RunMeta{}, err
	}
	return doc.toRunMeta(), nil
}

func (c *client) ListRunsBySession(ctx context.Context, sessionID string, statuses []session.RunStatus) ([]session.RunMeta, error) {
	if sessionID == "" {
		return nil, errors.New("session id is required")
	}
	filter := bson.M{"session_id": sessionID}
	if len(statuses) > 0 {
		filter["status"] = bson.M{"$in": statuses}
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	cur, err := c.runs.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "started_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = cur.Close(ctx)
	}()
	var out []session.RunMeta
	for cur.Next(ctx) {
		var doc runDocument
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		out = append(out, doc.toRunMeta())
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return out, nil
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
	Status    session.RunStatus `bson:"status"`
	StartedAt time.Time         `bson:"started_at"`
	UpdatedAt time.Time         `bson:"updated_at"`
	Labels    map[string]string `bson:"labels,omitempty"`
	Metadata  map[string]any    `bson:"metadata,omitempty"`
}

type sessionDocument struct {
	SessionID string                `bson:"session_id"`
	Status    session.SessionStatus `bson:"status"`
	CreatedAt time.Time             `bson:"created_at"`
	EndedAt   *time.Time            `bson:"ended_at,omitempty"`
	UpdatedAt time.Time             `bson:"updated_at"`
}

func fromRunMeta(run session.RunMeta) runDocument {
	return runDocument{
		RunID:     run.RunID,
		AgentID:   run.AgentID,
		SessionID: run.SessionID,
		Status:    run.Status,
		StartedAt: run.StartedAt.UTC(),
		UpdatedAt: run.UpdatedAt.UTC(),
		Labels:    cloneLabels(run.Labels),
		Metadata:  cloneMetadata(run.Metadata),
	}
}

func (doc runDocument) toRunMeta() session.RunMeta {
	return session.RunMeta{
		RunID:     doc.RunID,
		AgentID:   doc.AgentID,
		SessionID: doc.SessionID,
		Status:    doc.Status,
		StartedAt: doc.StartedAt,
		UpdatedAt: doc.UpdatedAt,
		Labels:    cloneLabels(doc.Labels),
		Metadata:  cloneMetadata(doc.Metadata),
	}
}

func (doc sessionDocument) toSession() session.Session {
	var endedAt *time.Time
	if doc.EndedAt != nil {
		at := doc.EndedAt.UTC()
		endedAt = &at
	}
	return session.Session{
		ID:        doc.SessionID,
		Status:    doc.Status,
		CreatedAt: doc.CreatedAt.UTC(),
		EndedAt:   endedAt,
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

func ensureIndexes(ctx context.Context, sessionsColl, runsColl collection) error {
	sessionIndex := mongodriver.IndexModel{
		Keys:    bson.D{{Key: "session_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	}
	if _, err := sessionsColl.Indexes().CreateOne(ctx, sessionIndex); err != nil {
		return err
	}
	runIndex := mongodriver.IndexModel{
		Keys:    bson.D{{Key: "run_id", Value: 1}},
		Options: options.Index().SetUnique(true),
	}
	if _, err := runsColl.Indexes().CreateOne(ctx, runIndex); err != nil {
		return err
	}
	runSessionIndex := mongodriver.IndexModel{
		Keys: bson.D{{Key: "session_id", Value: 1}},
	}
	if _, err := runsColl.Indexes().CreateOne(ctx, runSessionIndex); err != nil {
		return err
	}
	runSessionStatusIndex := mongodriver.IndexModel{
		Keys: bson.D{
			{Key: "session_id", Value: 1},
			{Key: "status", Value: 1},
		},
	}
	if _, err := runsColl.Indexes().CreateOne(ctx, runSessionStatusIndex); err != nil {
		return err
	}
	return nil
}

func newClientWithCollections(mongoClient *mongodriver.Client, sessionsColl, runsColl collection, timeout time.Duration) (*client, error) {
	if sessionsColl == nil || runsColl == nil {
		return nil, errors.New("collections are required")
	}
	if timeout <= 0 {
		timeout = defaultOpTimeout
	}
	return &client{
		mongo:    mongoClient,
		sessions: sessionsColl,
		runs:     runsColl,
		timeout:  timeout,
	}, nil
}

type collection interface {
	FindOne(ctx context.Context, filter any, opts ...*options.FindOneOptions) singleResult
	Find(ctx context.Context, filter any, opts ...*options.FindOptions) (cursor, error)
	UpdateOne(ctx context.Context, filter any, update any,
		opts ...*options.UpdateOptions) (*mongodriver.UpdateResult, error)
	Indexes() indexView
}

type indexView interface {
	CreateOne(ctx context.Context, model mongodriver.IndexModel,
		opts ...*options.CreateIndexesOptions) (string, error)
}

type singleResult interface {
	Decode(val any) error
}

type cursor interface {
	Close(ctx context.Context) error
	Decode(val any) error
	Err() error
	Next(ctx context.Context) bool
}

type mongoCollection struct {
	coll *mongodriver.Collection
}

func (c mongoCollection) FindOne(ctx context.Context, filter any, opts ...*options.FindOneOptions) singleResult {
	return mongoSingleResult{res: c.coll.FindOne(ctx, filter, opts...)}
}

func (c mongoCollection) Find(ctx context.Context, filter any, opts ...*options.FindOptions) (cursor, error) {
	cur, err := c.coll.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	return mongoCursor{cur: cur}, nil
}

func (c mongoCollection) UpdateOne(ctx context.Context, filter any, update any,
	opts ...*options.UpdateOptions) (*mongodriver.UpdateResult, error) {
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

type mongoCursor struct {
	cur *mongodriver.Cursor
}

func (c mongoCursor) Close(ctx context.Context) error {
	return c.cur.Close(ctx)
}

func (c mongoCursor) Decode(val any) error {
	return c.cur.Decode(val)
}

func (c mongoCursor) Err() error {
	return c.cur.Err()
}

func (c mongoCursor) Next(ctx context.Context) bool {
	return c.cur.Next(ctx)
}

type mongoIndexView struct {
	view mongodriver.IndexView
}

func (v mongoIndexView) CreateOne(ctx context.Context, model mongodriver.IndexModel,
	opts ...*options.CreateIndexesOptions) (string, error) {
	return v.view.CreateOne(ctx, model, opts...)
}
