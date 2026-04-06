package mongo

import (
	"context"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"
	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestEnsureIndexes(t *testing.T) {
	t.Parallel()

	fc := newFakeCollection()
	err := ensureIndexes(context.Background(), fc)
	require.NoError(t, err)
	require.Len(t, fc.indexes, 2)
}

func TestSetAndResolveByPrecedence(t *testing.T) {
	t.Parallel()

	client := mustNewTestClient()
	ctx := context.Background()
	id := prompt.Ident("example.agent.system")

	require.NoError(t, client.Set(ctx, id, prompt.Scope{}, "global", nil))
	require.NoError(t, client.Set(ctx, id, prompt.Scope{
		Labels: map[string]string{
			"account": "acme",
		},
	}, "account", nil))
	require.NoError(t, client.Set(ctx, id, prompt.Scope{
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	}, "region", nil))
	require.NoError(t, client.Set(ctx, id, prompt.Scope{
		SessionID: "sess_1",
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	}, "session", nil))

	override, err := client.Resolve(ctx, id, prompt.Scope{
		SessionID: "sess_1",
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Equal(t, "session", override.Template)
}

func TestResolveFallsBackAcrossScopes(t *testing.T) {
	t.Parallel()

	client := mustNewTestClient()
	ctx := context.Background()
	id := prompt.Ident("example.agent.system")
	require.NoError(t, client.Set(ctx, id, prompt.Scope{}, "global", nil))
	require.NoError(t, client.Set(ctx, id, prompt.Scope{
		Labels: map[string]string{
			"account": "acme",
		},
	}, "account", nil))
	require.NoError(t, client.Set(ctx, id, prompt.Scope{
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	}, "region", nil))

	override, err := client.Resolve(ctx, id, prompt.Scope{
		SessionID: "missing_session",
		Labels: map[string]string{
			"account": "acme",
			"region":  "west",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Equal(t, "region", override.Template)

	override, err = client.Resolve(ctx, id, prompt.Scope{
		SessionID: "missing_session",
		Labels: map[string]string{
			"account": "acme",
			"region":  "missing_region",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Equal(t, "account", override.Template)

	override, err = client.Resolve(ctx, id, prompt.Scope{
		SessionID: "missing_session",
		Labels: map[string]string{
			"account": "missing_account",
		},
	})
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Equal(t, "global", override.Template)
}

func TestResolveReturnsNilWhenMissing(t *testing.T) {
	t.Parallel()

	client := mustNewTestClient()
	override, err := client.Resolve(context.Background(), "missing", prompt.Scope{})
	require.NoError(t, err)
	require.Nil(t, override)
}

func TestHistoryAndListNewestFirst(t *testing.T) {
	t.Parallel()

	client := mustNewTestClient()
	ctx := context.Background()
	id := prompt.Ident("example.agent.system")
	require.NoError(t, client.Set(ctx, id, prompt.Scope{}, "first", nil))
	time.Sleep(time.Millisecond)
	require.NoError(t, client.Set(ctx, id, prompt.Scope{}, "second", nil))

	history, err := client.History(ctx, id)
	require.NoError(t, err)
	require.Len(t, history, 2)
	require.Equal(t, "second", history[0].Template)
	require.Equal(t, "first", history[1].Template)

	list, err := client.List(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	require.Equal(t, "second", list[0].Template)
}

func TestSetValidation(t *testing.T) {
	t.Parallel()

	client := mustNewTestClient()
	ctx := context.Background()
	err := client.Set(ctx, "", prompt.Scope{}, "template", nil)
	require.EqualError(t, err, "prompt id is required")
	err = client.Set(ctx, "example.agent.system", prompt.Scope{}, "", nil)
	require.EqualError(t, err, "template is required")
}

func TestSetWritesDocumentDefaults(t *testing.T) {
	t.Parallel()

	client := mustNewTestClient()
	ctx := context.Background()
	template := "hello {{ .Name }}"
	err := client.Set(ctx, "example.agent.system", prompt.Scope{}, template, nil)
	require.NoError(t, err)

	fc := client.coll.(*fakeCollection)
	require.Len(t, fc.docs, 1)
	doc := fc.docs[0]
	require.Equal(t, "example.agent.system", doc.PromptID)
	require.Empty(t, doc.ScopeSession)
	require.Nil(t, doc.ScopeLabels)
	require.Equal(t, 0, doc.ScopeLabelCount)
	require.Equal(t, prompt.VersionFromTemplate(template), doc.Version)
	require.False(t, doc.CreatedAt.IsZero())
	require.Nil(t, doc.Metadata)
}

func mustNewTestClient() *client {
	fc := newFakeCollection()
	c, err := newClientWithCollection(nil, fc, time.Second)
	if err != nil {
		panic(err)
	}
	return c
}

type fakeCollection struct {
	mu      sync.Mutex
	indexes []mongodriver.IndexModel
	docs    []overrideDocument
}

func newFakeCollection() *fakeCollection {
	return &fakeCollection{
		docs: make([]overrideDocument, 0),
	}
}

func (c *fakeCollection) FindOne(ctx context.Context, filter any, opts ...options.Lister[options.FindOneOptions]) singleResult {
	c.mu.Lock()
	defer c.mu.Unlock()

	matches := c.match(filter)
	if len(matches) == 0 {
		return fakeSingleResult{err: mongodriver.ErrNoDocuments}
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].CreatedAt.After(matches[j].CreatedAt)
	})
	return fakeSingleResult{doc: &matches[0]}
}

func (c *fakeCollection) Find(ctx context.Context, filter any, opts ...options.Lister[options.FindOptions]) (cursor, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	matches := c.match(filter)
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].CreatedAt.After(matches[j].CreatedAt)
	})
	return &fakeCursor{docs: matches, idx: -1}, nil
}

func (c *fakeCollection) InsertOne(ctx context.Context, document any, opts ...options.Lister[options.InsertOneOptions]) (*mongodriver.InsertOneResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	doc, ok := document.(overrideDocument)
	if !ok {
		return nil, errors.New("unexpected insert document type")
	}
	c.docs = append(c.docs, doc)
	return &mongodriver.InsertOneResult{InsertedID: "id"}, nil
}

func (c *fakeCollection) Indexes() indexView {
	return fakeIndexView{parent: c}
}

func (c *fakeCollection) match(filter any) []overrideDocument {
	f, _ := filter.(bson.M)
	out := make([]overrideDocument, 0)
	for _, doc := range c.docs {
		if !matchesFilter(doc, f) {
			continue
		}
		out = append(out, doc)
	}
	return out
}

func matchesFilter(doc overrideDocument, filter bson.M) bool {
	for key, val := range filter {
		switch key {
		case "prompt_id":
			if doc.PromptID != val {
				return false
			}
		case "scope_session":
			if doc.ScopeSession != val {
				return false
			}
		default:
			return false
		}
	}
	return true
}

type fakeSingleResult struct {
	doc *overrideDocument
	err error
}

func (r fakeSingleResult) Decode(val any) error {
	if r.err != nil {
		return r.err
	}
	out, ok := val.(*overrideDocument)
	if !ok {
		return errors.New("unexpected decode target")
	}
	*out = *r.doc
	return nil
}

type fakeCursor struct {
	docs []overrideDocument
	idx  int
}

func (c *fakeCursor) Close(ctx context.Context) error {
	return nil
}

func (c *fakeCursor) Decode(val any) error {
	if c.idx < 0 || c.idx >= len(c.docs) {
		return errors.New("no current document")
	}
	out, ok := val.(*overrideDocument)
	if !ok {
		return errors.New("unexpected decode target")
	}
	*out = c.docs[c.idx]
	return nil
}

func (c *fakeCursor) Err() error {
	return nil
}

func (c *fakeCursor) Next(ctx context.Context) bool {
	next := c.idx + 1
	if next >= len(c.docs) {
		return false
	}
	c.idx = next
	return true
}

type fakeIndexView struct {
	parent *fakeCollection
}

func (v fakeIndexView) CreateOne(ctx context.Context, model mongodriver.IndexModel,
	opts ...options.Lister[options.CreateIndexesOptions]) (string, error) {
	v.parent.mu.Lock()
	defer v.parent.mu.Unlock()
	v.parent.indexes = append(v.parent.indexes, model)
	return "idx", nil
}
