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

	"goa.design/goa-ai/runtime/agent/memory"
)

func TestEnsureIndexes(t *testing.T) {
	fc := newFakeCollection()
	err := ensureIndexes(context.Background(), fc)
	require.NoError(t, err)
	require.True(t, fc.indexCreated)
}

func TestLoadRunMissingReturnsEmptySnapshot(t *testing.T) {
	client := mustNewTestClient()
	snap, err := client.LoadRun(context.Background(), "agent", "run")
	require.NoError(t, err)
	require.Equal(t, "agent", snap.AgentID)
	require.Equal(t, "run", snap.RunID)
	require.Empty(t, snap.Events)
	require.NotNil(t, snap.Meta)
}

func TestAppendAndLoadRun(t *testing.T) {
	client := mustNewTestClient()
	ts := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	err := client.AppendEvents(context.Background(), "agent", "run", []memory.Event{
		{
			Type:      memory.EventToolCall,
			Timestamp: ts,
			Data:      map[string]any{"name": "foo"},
			Labels:    map[string]string{"kind": "call"},
		},
		{
			Type:   memory.EventAssistantMessage,
			Data:   map[string]any{"text": "done"},
			Labels: map[string]string{"kind": "assistant"},
		},
	})
	require.NoError(t, err)

	snap, err := client.LoadRun(context.Background(), "agent", "run")
	require.NoError(t, err)
	require.Len(t, snap.Events, 2)
	require.Equal(t, memory.EventToolCall, snap.Events[0].Type)
	require.Equal(t, ts, snap.Events[0].Timestamp)
	require.Equal(t, "foo", snap.Events[0].Data.(map[string]any)["name"])
	require.Equal(t, "call", snap.Events[0].Labels["kind"])
	require.Equal(t, memory.EventAssistantMessage, snap.Events[1].Type)
	require.NotZero(t, snap.Events[1].Timestamp)
	require.Equal(t, "assistant", snap.Events[1].Labels["kind"])
}

func TestAppendEventsRequiresIdentifiers(t *testing.T) {
	client := mustNewTestClient()
	err := client.AppendEvents(context.Background(), "", "run", []memory.Event{{Type: memory.EventPlannerNote}})
	require.EqualError(t, err, "agent id is required")
	err = client.AppendEvents(context.Background(), "agent", "", []memory.Event{{Type: memory.EventPlannerNote}})
	require.EqualError(t, err, "run id is required")
}

func TestLoadRunRequiresIdentifiers(t *testing.T) {
	client := mustNewTestClient()
	_, err := client.LoadRun(context.Background(), "", "run")
	require.EqualError(t, err, "agent id is required")
	_, err = client.LoadRun(context.Background(), "agent", "")
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

// fakeCollection is a lightweight in-memory collection that mimics the subset
// of MongoDB behavior exercised by the client.
type fakeCollection struct {
	mu           sync.Mutex
	indexCreated bool
	docs         map[string]*runDocument
}

func newFakeCollection() *fakeCollection {
	return &fakeCollection{docs: make(map[string]*runDocument)}
}

func (c *fakeCollection) FindOne(ctx context.Context, filter any, opts ...*options.FindOneOptions) singleResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := docKey(filter)
	doc, ok := c.docs[key]
	if !ok {
		return fakeSingleResult{err: mongodriver.ErrNoDocuments}
	}
	clone := *doc
	clone.Events = append([]eventDocument(nil), doc.Events...)
	return fakeSingleResult{doc: &clone}
}

func (c *fakeCollection) UpdateOne(ctx context.Context, filter any, update any,
	opts ...*options.UpdateOptions) (*mongodriver.UpdateResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	key := docKey(filter)
	doc, ok := c.docs[key]
	if !ok {
		doc = &runDocument{}
		c.docs[key] = doc
	}
	up, _ := update.(bson.M)
	if soi, ok := up["$setOnInsert"].(bson.M); ok && doc.AgentID == "" && doc.RunID == "" {
		if v, ok := soi["agent_id"].(string); ok {
			doc.AgentID = v
		}
		if v, ok := soi["run_id"].(string); ok {
			doc.RunID = v
		}
	}
	if set, ok := up["$set"].(bson.M); ok {
		if v, ok := set["updated_at"].(time.Time); ok {
			doc.UpdatedAt = v
		}
	}
	if push, ok := up["$push"].(bson.M); ok {
		if ev, ok := push["events"].(bson.M); ok {
			if each, ok := ev["$each"].([]eventDocument); ok {
				doc.Events = append(doc.Events, cloneEventDocs(each)...)
			}
		}
	}
	return &mongodriver.UpdateResult{MatchedCount: 1}, nil
}

func (c *fakeCollection) Indexes() indexView {
	return fakeIndexView{parent: c}
}

type fakeIndexView struct {
	parent *fakeCollection
}

func (v fakeIndexView) CreateOne(ctx context.Context, model mongodriver.IndexModel,
	opts ...*options.CreateIndexesOptions) (string, error) {
	if len(model.Keys.(bson.D)) == 0 {
		return "", errors.New("missing keys")
	}
	v.parent.mu.Lock()
	v.parent.indexCreated = true
	v.parent.mu.Unlock()
	return "idx_agent_run", nil
}

type fakeSingleResult struct {
	doc *runDocument
	err error
}

func (r fakeSingleResult) Decode(val any) error {
	if r.err != nil {
		return r.err
	}
	dest, ok := val.(*runDocument)
	if !ok {
		return errors.New("unsupported decode target")
	}
	*dest = *r.doc
	return nil
}

func docKey(filter any) string {
	bsonFilter, _ := filter.(bson.M)
	agent, _ := bsonFilter["agent_id"].(string)
	run, _ := bsonFilter["run_id"].(string)
	return agent + "|" + run
}

func cloneEventDocs(src []eventDocument) []eventDocument {
	if len(src) == 0 {
		return nil
	}
	dst := make([]eventDocument, len(src))
	for i, evt := range src {
		evt.Labels = cloneStringMap(evt.Labels)
		dst[i] = evt
	}
	return dst
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}
