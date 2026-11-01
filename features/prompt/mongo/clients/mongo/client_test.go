// These tests exercise prompt override queries with an in-memory collection fake.
package mongo

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
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

func TestReset(t *testing.T) {
	t.Parallel()

	client := mustNewTestClient()
	ctx := context.Background()
	id := prompt.Ident("example.agent.system")

	err := client.Reset(ctx, "", prompt.Scope{})
	require.EqualError(t, err, "prompt id is required")

	scopeGlobal := prompt.Scope{}
	scopeAccount := prompt.Scope{
		Labels: map[string]string{"account": "acme"},
	}

	require.NoError(t, client.Set(ctx, id, scopeGlobal, "global template", nil))
	require.NoError(t, client.Set(ctx, id, scopeAccount, "account template", nil))

	override, err := client.Resolve(ctx, id, scopeAccount)
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Equal(t, "account template", override.Template)

	require.NoError(t, client.Reset(ctx, id, scopeAccount))

	override, err = client.Resolve(ctx, id, scopeAccount)
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Equal(t, "global template", override.Template)

	require.NoError(t, client.Reset(ctx, id, scopeGlobal))

	override, err = client.Resolve(ctx, id, scopeAccount)
	require.NoError(t, err)
	require.Nil(t, override)
}

// TestResetPreservesOtherScopes verifies that label strings remain data when
// MongoDB compares scopes, including strings that name document fields.
func TestResetPreservesOtherScopes(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		labels      map[string]string
		otherLabels map[string]string
	}{
		{
			name:        "document field in value",
			labels:      map[string]string{"account": "$prompt_id"},
			otherLabels: map[string]string{"account": "literal.prompt"},
		},
		{
			name:        "document field in key",
			labels:      map[string]string{"$prompt_id": "acme"},
			otherLabels: map[string]string{"literal.prompt": "acme"},
		},
		{
			name:        "dotted key and document field value",
			labels:      map[string]string{"account.id": "$scope_session"},
			otherLabels: map[string]string{"account.id": "session"},
		},
		{
			name:        "system variable strings",
			labels:      map[string]string{"$$ROOT": "$$CURRENT"},
			otherLabels: map[string]string{"account": "acme"},
		},
		{
			name:        "dotted key and empty values",
			labels:      map[string]string{"account.id": "123", "env": ""},
			otherLabels: map[string]string{"account.id": "123", "region": ""},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			client := mustNewTestClient()
			ctx := t.Context()
			id := prompt.Ident("literal.prompt")
			scope := prompt.Scope{SessionID: "session", Labels: tc.labels}

			require.NoError(t, client.Set(ctx, id, scope, "target first", nil))
			require.NoError(t, client.Set(ctx, id, scope, "target second", nil))
			require.NoError(t, client.Set(ctx, id, prompt.Scope{
				SessionID: "session",
				Labels:    tc.otherLabels,
			}, "other labels", nil))
			require.NoError(t, client.Set(ctx, id, prompt.Scope{}, "global", nil))
			require.NoError(t, client.Set(ctx, id, prompt.Scope{
				SessionID: "other session",
				Labels:    tc.labels,
			}, "other session", nil))
			require.NoError(t, client.Set(ctx, "other.prompt", scope, "other prompt", nil))

			before, err := client.List(ctx)
			require.NoError(t, err)
			var expected []*prompt.Override
			for _, override := range before {
				if override.Template != "target first" && override.Template != "target second" {
					expected = append(expected, override)
				}
			}

			require.NoError(t, client.Reset(ctx, id, scope))
			after, err := client.List(ctx)
			require.NoError(t, err)
			assert.ElementsMatch(t, expected, after)
		})
	}
}

func TestResolveEmptyStringLabel(t *testing.T) {
	t.Parallel()

	client := mustNewTestClient()
	ctx := context.Background()
	id := prompt.Ident("example.empty.label")

	scopeEmptyAccount := prompt.Scope{
		Labels: map[string]string{"account": ""},
	}
	scopeOther := prompt.Scope{
		Labels: map[string]string{"account": "other"},
	}

	require.NoError(t, client.Set(ctx, id, scopeEmptyAccount, "empty account", nil))
	require.NoError(t, client.Set(ctx, id, scopeOther, "other account", nil))

	// Request missing "account": must NOT match empty-string label.
	override, err := client.Resolve(ctx, id, prompt.Scope{
		Labels: map[string]string{"region": "us"},
	})
	require.NoError(t, err)
	require.Nil(t, override)

	// Request with empty labels: must NOT match.
	override, err = client.Resolve(ctx, id, prompt.Scope{})
	require.NoError(t, err)
	require.Nil(t, override)

	// Request with "account": "": MUST match.
	override, err = client.Resolve(ctx, id, scopeEmptyAccount)
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Equal(t, "empty account", override.Template)

	// Unrelated override is preserved.
	override, err = client.Resolve(ctx, id, scopeOther)
	require.NoError(t, err)
	require.NotNil(t, override)
	require.Equal(t, "other account", override.Template)
}

func TestResolveSessionZeroLabelsOutranksNonSessionManyLabels(t *testing.T) {
	t.Parallel()

	for _, count := range []int{999, 1000, 1001} {
		t.Run(fmt.Sprintf("%d_labels", count), func(t *testing.T) {
			client := mustNewTestClient()
			ctx := context.Background()
			id := prompt.Ident("example.session.precedence")

			labels := make(map[string]string, count)
			for i := 0; i < count; i++ {
				labels[fmt.Sprintf("k%04d", i)] = "v"
			}

			require.NoError(t, client.Set(ctx, id, prompt.Scope{Labels: labels}, "many_labels", nil))
			require.NoError(t, client.Set(ctx, id, prompt.Scope{SessionID: "sess_1"}, "session_zero", nil))

			override, err := client.Resolve(ctx, id, prompt.Scope{
				SessionID: "sess_1",
				Labels:    labels,
			})
			require.NoError(t, err)
			require.NotNil(t, override)
			require.Equal(t, "session_zero", override.Template)
		})
	}
}

func mustNewTestClient() *client {
	fc := newFakeCollection()
	return newClientWithCollection(nil, fc, time.Second)
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

func (c *fakeCollection) DeleteMany(ctx context.Context, filter any, opts ...options.Lister[options.DeleteManyOptions]) (*mongodriver.DeleteResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	f, _ := filter.(bson.M)
	remaining := make([]overrideDocument, 0, len(c.docs))
	deleted := int64(0)
	for _, doc := range c.docs {
		if matchesFilter(doc, f) {
			deleted++
			continue
		}
		remaining = append(remaining, doc)
	}
	c.docs = remaining
	return &mongodriver.DeleteResult{DeletedCount: deleted}, nil
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

// matchesFilter applies the query shapes used by this client, including literal
// values and the document field references exercised by reset preservation tests.
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
		case "scope_label_count":
			if doc.ScopeLabelCount != val {
				return false
			}
		case "$expr":
			exprMap, ok := val.(bson.M)
			if !ok {
				return false
			}
			setEqualsArgs, ok := exprMap["$setEquals"].(bson.A)
			if !ok || len(setEqualsArgs) != 2 {
				return false
			}
			var expectedArr bson.A
			literal := false
			switch operand := setEqualsArgs[1].(type) {
			case bson.A:
				expectedArr = operand
			case bson.M:
				expectedArr, ok = operand["$literal"].(bson.A)
				if !ok {
					return false
				}
				literal = true
			default:
				return false
			}
			expectedMap := make(map[string]string, len(expectedArr))
			for _, item := range expectedArr {
				if d, ok := item.(bson.D); ok {
					var k, v string
					for _, elem := range d {
						switch elem.Key {
						case "k":
							k, ok = resetExpressionString(doc, elem.Value, literal)
						case "v":
							v, ok = resetExpressionString(doc, elem.Value, literal)
						}
						if !ok {
							return false
						}
					}
					expectedMap[k] = v
				}
			}
			if len(doc.ScopeLabels) == 0 && len(expectedMap) == 0 {
				continue
			}
			if !maps.Equal(doc.ScopeLabels, expectedMap) {
				return false
			}
		default:
			// Dotted paths like "scope_labels.*" are rejected by design.
			return false
		}
	}
	return true
}

// resetExpressionString evaluates the literal strings and document field paths
// used by the reset tests. Other expression results are outside this fake's scope.
func resetExpressionString(doc overrideDocument, value any, literal bool) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	if literal || !strings.HasPrefix(text, "$") {
		return text, true
	}
	switch text {
	case "$prompt_id":
		return doc.PromptID, true
	case "$scope_session":
		return doc.ScopeSession, true
	default:
		return "", false
	}
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
