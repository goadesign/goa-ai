package mongo

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	mongodriver "go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"goa.design/goa-ai/agents/runtime/run"
)

func TestEnsureIndexes(t *testing.T) {
	fc := newFakeCollection()
	err := ensureIndexes(context.Background(), fc)
	require.NoError(t, err)
	require.True(t, fc.indexCreated)
}

func TestUpsertAndLoad(t *testing.T) {
	client := mustNewTestClient()
	rec := run.Record{
		RunID:     "run-1",
		AgentID:   "agent.chat",
		SessionID: "sess-1",
		Status:    run.StatusPending,
		Labels:    map[string]string{"org": "demo"},
		Metadata:  map[string]any{"reason": "test"},
	}
	err := client.UpsertRun(context.Background(), rec)
	require.NoError(t, err)

	stored, err := client.LoadRun(context.Background(), "run-1")
	require.NoError(t, err)
	require.Equal(t, rec.RunID, stored.RunID)
	require.Equal(t, rec.AgentID, stored.AgentID)
	require.Equal(t, rec.SessionID, stored.SessionID)
	require.Equal(t, rec.Status, stored.Status)
	require.Equal(t, "demo", stored.Labels["org"])

	rec.Status = run.StatusCompleted
	time.Sleep(10 * time.Millisecond)
	err = client.UpsertRun(context.Background(), rec)
	require.NoError(t, err)
	updated, err := client.LoadRun(context.Background(), "run-1")
	require.NoError(t, err)
	require.Equal(t, run.StatusCompleted, updated.Status)
	require.True(t, updated.UpdatedAt.After(updated.StartedAt) || updated.UpdatedAt.Equal(updated.StartedAt))
}

func TestUpsertValidation(t *testing.T) {
	client := mustNewTestClient()
	err := client.UpsertRun(context.Background(), run.Record{AgentID: "agent"})
	require.EqualError(t, err, "run id is required")
	err = client.UpsertRun(context.Background(), run.Record{RunID: "run"})
	require.EqualError(t, err, "agent id is required")
}

func TestLoadMissingReturnsZero(t *testing.T) {
	client := mustNewTestClient()
	rec, err := client.LoadRun(context.Background(), "missing")
	require.NoError(t, err)
	require.Equal(t, run.Record{}, rec)
}

func TestLoadRequiresID(t *testing.T) {
	client := mustNewTestClient()
	_, err := client.LoadRun(context.Background(), "")
	require.EqualError(t, err, "run id is required")
}

func mustNewTestClient() *client {
	fc := newFakeCollection()
	cl, err := newClientWithCollection(nil, fc, time.Second)
	if err != nil {
		panic(err)
	}
	return cl
}

type fakeCollection struct {
	mu           sync.Mutex
	indexCreated bool
	docs         map[string]runDocument
}

func newFakeCollection() *fakeCollection {
	return &fakeCollection{docs: make(map[string]runDocument)}
}

func (c *fakeCollection) FindOne(ctx context.Context, filter any, opts ...*options.FindOneOptions) singleResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	runID := filter.(bson.M)["run_id"].(string)
	doc, ok := c.docs[runID]
	if !ok {
		return fakeSingleResult{err: mongodriver.ErrNoDocuments}
	}
	copyDoc := doc
	return fakeSingleResult{doc: &copyDoc}
}

func (c *fakeCollection) UpdateOne(ctx context.Context, filter any, update any,
	opts ...*options.UpdateOptions) (*mongodriver.UpdateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	runID := filter.(bson.M)["run_id"].(string)
	doc, ok := c.docs[runID]
	if !ok {
		doc = runDocument{}
	}
	up := update.(bson.M)
	if set, ok := up["$set"].(runDocument); ok {
		doc = set
	}
	if soi, ok := up["$setOnInsert"].(bson.M); ok && doc.StartedAt.IsZero() {
		if ts, ok := soi["started_at"].(time.Time); ok {
			doc.StartedAt = ts
		}
	}
	c.docs[runID] = doc
	return &mongodriver.UpdateResult{MatchedCount: 1}, nil
}

func (c *fakeCollection) Indexes() indexView {
	return fakeIndexView{parent: &c.indexCreated}
}

type fakeIndexView struct {
	parent *bool
}

func (v fakeIndexView) CreateOne(ctx context.Context, model mongodriver.IndexModel,
	opts ...*options.CreateIndexesOptions) (string, error) {
	if len(model.Keys.(bson.D)) == 0 {
		return "", errors.New("missing keys")
	}
	*v.parent = true
	return "run_id_idx", nil
}

type fakeSingleResult struct {
	doc *runDocument
	err error
}

func (r fakeSingleResult) Decode(val any) error {
	if r.err != nil {
		return r.err
	}
	target, ok := val.(*runDocument)
	if !ok {
		return errors.New("unsupported target")
	}
	*target = *r.doc
	return nil
}
