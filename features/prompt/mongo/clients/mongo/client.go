// Package mongo implements the low-level MongoDB client used by the prompt
// override store.
package mongo

//go:generate cmg gen .

import (
	"context"
	"errors"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"

	"goa.design/clue/health"

	"goa.design/goa-ai/runtime/agent/prompt"
)

type (
	// Client exposes Mongo-backed operations for prompt overrides.
	Client interface {
		health.Pinger

		Resolve(ctx context.Context, promptID prompt.Ident, scope prompt.Scope) (*prompt.Override, error)
		Set(ctx context.Context, promptID prompt.Ident, scope prompt.Scope, template string, metadata map[string]string) error
		Reset(ctx context.Context, promptID prompt.Ident, scope prompt.Scope) error
		History(ctx context.Context, promptID prompt.Ident) ([]*prompt.Override, error)
		List(ctx context.Context) ([]*prompt.Override, error)
	}

	// Options configures the Mongo client implementation.
	Options struct {
		// Client is the connected MongoDB client handle used to execute operations.
		Client *mongodriver.Client
		// Database is the database containing prompt override collections.
		Database string
		// Collection is the collection storing override documents. Defaults to "prompt_overrides".
		Collection string
		// Timeout bounds individual MongoDB command executions. Defaults to 5s.
		Timeout time.Duration
	}

	client struct {
		mongo   *mongodriver.Client
		coll    collection
		timeout time.Duration
	}

	overrideDocument struct {
		PromptID        string            `bson:"prompt_id"`
		ScopeSession    string            `bson:"scope_session"`
		ScopeLabels     map[string]string `bson:"scope_labels,omitempty"`
		ScopeLabelCount int               `bson:"scope_label_count"`
		Template        string            `bson:"template"`
		Version         string            `bson:"version"`
		CreatedAt       time.Time         `bson:"created_at"`
		Metadata        map[string]string `bson:"metadata,omitempty"`
	}
)

const (
	defaultCollection = "prompt_overrides"
	defaultTimeout    = 5 * time.Second
	clientName        = "prompt-mongo"
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
	return newClientWithCollection(opts.Client, wrapper, timeout), nil
}

func (c *client) Name() string {
	return clientName
}

func (c *client) Ping(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if c.mongo == nil {
		return errors.New("mongo client is not configured")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()
	return c.mongo.Ping(ctx, readpref.Primary())
}

func (c *client) Resolve(ctx context.Context, promptID prompt.Ident, scope prompt.Scope) (*prompt.Override, error) {
	if promptID == "" {
		return nil, errors.New("prompt id is required")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	findOpts := options.Find().SetSort(bson.D{
		{Key: "scope_session", Value: -1},
		{Key: "scope_label_count", Value: -1},
		{Key: "created_at", Value: -1},
	})
	cur, err := c.coll.Find(ctx, bson.M{"prompt_id": promptID.String()}, findOpts)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = cur.Close(ctx)
	}()

	candidates, err := decodeOverrides(ctx, cur)
	if err != nil {
		return nil, err
	}

	var best *prompt.Override
	for _, candidate := range candidates {
		if !prompt.ScopeMatches(candidate.Scope, scope) {
			continue
		}
		if prompt.OverridePrecedes(candidate, best) {
			best = candidate
		}
	}
	return best, nil
}

func (c *client) Set(ctx context.Context, promptID prompt.Ident, scope prompt.Scope, template string, metadata map[string]string) error {
	if promptID == "" {
		return errors.New("prompt id is required")
	}
	if template == "" {
		return errors.New("template is required")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	doc := overrideDocument{
		PromptID:        promptID.String(),
		ScopeSession:    scope.SessionID,
		ScopeLabels:     cloneMetadata(scope.Labels),
		ScopeLabelCount: len(scope.Labels),
		Template:        template,
		Version:         prompt.VersionFromTemplate(template),
		CreatedAt:       time.Now().UTC(),
		Metadata:        cloneMetadata(metadata),
	}
	_, err := c.coll.InsertOne(ctx, doc)
	return err
}

func (c *client) Reset(ctx context.Context, promptID prompt.Ident, scope prompt.Scope) error {
	if promptID == "" {
		return errors.New("prompt id is required")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	expectedArray := make(bson.A, 0, len(scope.Labels))
	keys := make([]string, 0, len(scope.Labels))
	for k := range scope.Labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		expectedArray = append(expectedArray, bson.D{{Key: "k", Value: k}, {Key: "v", Value: scope.Labels[k]}})
	}

	filter := bson.M{
		"prompt_id":         promptID.String(),
		"scope_session":     scope.SessionID,
		"scope_label_count": len(scope.Labels),
		"$expr": bson.M{
			"$setEquals": bson.A{
				bson.M{"$objectToArray": bson.M{"$ifNull": bson.A{"$scope_labels", bson.M{}}}},
				expectedArray,
			},
		},
	}
	_, err := c.coll.DeleteMany(ctx, filter)
	return err
}

func (c *client) History(ctx context.Context, promptID prompt.Ident) ([]*prompt.Override, error) {
	if promptID == "" {
		return nil, errors.New("prompt id is required")
	}
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	cur, err := c.coll.Find(ctx, bson.M{"prompt_id": promptID.String()}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = cur.Close(ctx)
	}()
	return decodeOverrides(ctx, cur)
}

func (c *client) List(ctx context.Context) ([]*prompt.Override, error) {
	ctx, cancel := c.withTimeout(ctx)
	defer cancel()

	cur, err := c.coll.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = cur.Close(ctx)
	}()
	return decodeOverrides(ctx, cur)
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

func decodeOverrides(ctx context.Context, cur cursor) ([]*prompt.Override, error) {
	overrides := make([]*prompt.Override, 0)
	for cur.Next(ctx) {
		var doc overrideDocument
		if err := cur.Decode(&doc); err != nil {
			return nil, err
		}
		overrides = append(overrides, toOverride(doc))
	}
	if err := cur.Err(); err != nil {
		return nil, err
	}
	return overrides, nil
}

func toOverride(doc overrideDocument) *prompt.Override {
	return &prompt.Override{
		PromptID: prompt.Ident(doc.PromptID),
		Scope: prompt.Scope{
			SessionID: doc.ScopeSession,
			Labels:    cloneMetadata(doc.ScopeLabels),
		},
		Template:  doc.Template,
		Version:   doc.Version,
		CreatedAt: doc.CreatedAt,
		Metadata:  cloneMetadata(doc.Metadata),
	}
}

func cloneMetadata(src map[string]string) map[string]string {
	if src == nil {
		return nil
	}
	cloned := make(map[string]string, len(src))
	for k, v := range src {
		cloned[k] = v
	}
	return cloned
}

type (
	mongoCollection struct {
		coll *mongodriver.Collection
	}

	mongoSingleResult struct {
		res *mongodriver.SingleResult
	}

	mongoCursor struct {
		cur *mongodriver.Cursor
	}

	mongoIndexView struct {
		view mongodriver.IndexView
	}
)

func newClientWithCollection(mclient *mongodriver.Client, coll collection, timeout time.Duration) *client {
	return &client{
		mongo:   mclient,
		coll:    coll,
		timeout: timeout,
	}
}

func (c mongoCollection) FindOne(ctx context.Context, filter any, opts ...options.Lister[options.FindOneOptions]) singleResult {
	return mongoSingleResult{res: c.coll.FindOne(ctx, filter, opts...)}
}

func (c mongoCollection) Find(ctx context.Context, filter any, opts ...options.Lister[options.FindOptions]) (cursor, error) {
	cur, err := c.coll.Find(ctx, filter, opts...)
	if err != nil {
		return nil, err
	}
	return mongoCursor{cur: cur}, nil
}

func (c mongoCollection) InsertOne(ctx context.Context, document any, opts ...options.Lister[options.InsertOneOptions]) (*mongodriver.InsertOneResult, error) {
	return c.coll.InsertOne(ctx, document, opts...)
}

func (c mongoCollection) DeleteMany(ctx context.Context, filter any, opts ...options.Lister[options.DeleteManyOptions]) (*mongodriver.DeleteResult, error) {
	return c.coll.DeleteMany(ctx, filter, opts...)
}

func (c mongoCollection) Indexes() indexView {
	return mongoIndexView{view: c.coll.Indexes()}
}

func (r mongoSingleResult) Decode(val any) error {
	return r.res.Decode(val)
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

func (v mongoIndexView) CreateMany(ctx context.Context, models []mongodriver.IndexModel, opts ...options.Lister[options.CreateIndexesOptions]) ([]string, error) {
	return v.view.CreateMany(ctx, models, opts...)
}

type (
	collection interface {
		FindOne(ctx context.Context, filter any, opts ...options.Lister[options.FindOneOptions]) singleResult
		Find(ctx context.Context, filter any, opts ...options.Lister[options.FindOptions]) (cursor, error)
		InsertOne(ctx context.Context, document any, opts ...options.Lister[options.InsertOneOptions]) (*mongodriver.InsertOneResult, error)
		DeleteMany(ctx context.Context, filter any, opts ...options.Lister[options.DeleteManyOptions]) (*mongodriver.DeleteResult, error)
		Indexes() indexView
	}

	singleResult interface {
		Decode(val any) error
	}

	cursor interface {
		Next(ctx context.Context) bool
		Decode(val any) error
		Err() error
		Close(ctx context.Context) error
	}

	indexView interface {
		CreateMany(ctx context.Context, models []mongodriver.IndexModel, opts ...options.Lister[options.CreateIndexesOptions]) ([]string, error)
	}
)

func ensureIndexes(ctx context.Context, coll collection) error {
	models := []mongodriver.IndexModel{
		{
			Keys: bson.D{
				{Key: "prompt_id", Value: 1},
				{Key: "created_at", Value: -1},
			},
		},
		{
			Keys: bson.D{
				{Key: "prompt_id", Value: 1},
				{Key: "scope_session", Value: -1},
				{Key: "scope_label_count", Value: -1},
				{Key: "created_at", Value: -1},
			},
		},
	}
	_, err := coll.Indexes().CreateMany(ctx, models)
	return err
}
