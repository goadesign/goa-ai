package mongo

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/v2/bson"

	mongodriver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/runlog"
)

func TestClientAppendAssignsID(t *testing.T) {
	t.Parallel()

	oid := mustOID(t, "000000000000000000000001")
	coll := &fakeCollection{
		insertedID: oid,
	}
	c := &client{coll: coll}

	e := &runlog.Event{
		EventKey:  "evt-1",
		RunID:     "run-1",
		AgentID:   "agent-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      hooks.RunStarted,
		Payload:   []byte(`{"ok":true}`),
		Timestamp: time.Unix(1, 0).UTC(),
	}
	res, err := c.Append(context.Background(), e)
	require.NoError(t, err)
	require.True(t, res.Inserted)
	assert.Equal(t, oid.Hex(), e.ID)
}

func TestClientListNextCursor(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		eventCount int
		limit      int
		wantNext   string
	}
	cases := []testCase{
		{
			name:       "fewer_than_limit",
			eventCount: 2,
			limit:      3,
			wantNext:   "",
		},
		{
			name:       "exactly_limit_no_more",
			eventCount: 3,
			limit:      3,
			wantNext:   "",
		},
		{
			name:       "more_than_limit_has_next",
			eventCount: 4,
			limit:      3,
			wantNext:   "000000000000000000000003",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runID := "run-1"
			coll := &fakeCollection{
				findDocs: fakeEventDocuments(runID, tc.eventCount),
			}
			c := &client{coll: coll}

			page, err := c.List(context.Background(), runID, "", tc.limit)
			require.NoError(t, err)
			assert.Len(t, page.Events, min(tc.eventCount, tc.limit))
			assert.Equal(t, tc.wantNext, page.NextCursor)

			if tc.wantNext == "" {
				return
			}

			next, err := c.List(context.Background(), runID, page.NextCursor, tc.limit)
			require.NoError(t, err)
			assert.Len(t, next.Events, tc.eventCount-tc.limit)
			assert.Empty(t, next.NextCursor)
		})
	}
}

func TestClientListSessionNextCursor(t *testing.T) {
	t.Parallel()

	type testCase struct {
		name       string
		eventCount int
		limit      int
		wantNext   string
	}
	cases := []testCase{
		{
			name:       "fewer_than_limit",
			eventCount: 2,
			limit:      3,
			wantNext:   "",
		},
		{
			name:       "exactly_limit_no_more",
			eventCount: 3,
			limit:      3,
			wantNext:   "",
		},
		{
			name:       "more_than_limit_has_next",
			eventCount: 4,
			limit:      3,
			wantNext:   "000000000000000000000003",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sessionID := "session-1"
			coll := &fakeCollection{
				findDocs: fakeSessionEventDocuments(sessionID, tc.eventCount),
			}
			c := &client{coll: coll}

			page, err := c.ListSession(context.Background(), sessionID, "", tc.limit)
			require.NoError(t, err)
			assert.Len(t, page.Events, min(tc.eventCount, tc.limit))
			assert.Equal(t, tc.wantNext, page.NextCursor)

			if tc.wantNext == "" {
				return
			}

			next, err := c.ListSession(context.Background(), sessionID, page.NextCursor, tc.limit)
			require.NoError(t, err)
			assert.Len(t, next.Events, tc.eventCount-tc.limit)
			assert.Empty(t, next.NextCursor)
		})
	}
}

func TestClientAppendReturnsExistingIDForDuplicateEventKey(t *testing.T) {
	t.Parallel()

	oid := mustOID(t, "000000000000000000000001")
	coll := &fakeCollection{
		insertedID: oid,
	}
	c := &client{coll: coll}

	e := &runlog.Event{
		RunID:     "run-1",
		AgentID:   "agent-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      hooks.RunStarted,
		Payload:   []byte(`{"ok":true}`),
		Timestamp: time.Unix(1, 0).UTC(),
		EventKey:  "evt-1",
	}
	first, err := c.Append(context.Background(), e)
	require.NoError(t, err)
	require.True(t, first.Inserted)

	coll.insertErr = mongodriver.WriteException{
		WriteErrors: []mongodriver.WriteError{
			{Code: 11000, Message: "duplicate key"},
		},
	}
	coll.findOneDoc = eventDocument{
		ID:        oid,
		RunID:     "run-1",
		AgentID:   "agent-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      string(hooks.RunStarted),
		Payload:   []byte(`{"ok":true}`),
		Timestamp: time.Unix(1, 0).UTC(),
		EventKey:  "evt-1",
	}

	dup := &runlog.Event{
		RunID:     "run-1",
		AgentID:   "agent-1",
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      hooks.RunStarted,
		Payload:   []byte(`{"ok":true}`),
		Timestamp: time.Unix(1, 0).UTC(),
		EventKey:  "evt-1",
	}
	second, err := c.Append(context.Background(), dup)
	require.NoError(t, err)
	require.False(t, second.Inserted)
	require.Equal(t, oid.Hex(), second.ID)
	require.Equal(t, oid.Hex(), dup.ID)
}

func fakeEventDocuments(runID string, n int) []eventDocument {
	docs := make([]eventDocument, 0, n)
	for i := 1; i <= n; i++ {
		oid := bson.ObjectID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, byte(i)}
		docs = append(docs, eventDocument{
			ID:        oid,
			RunID:     runID,
			AgentID:   "agent-1",
			SessionID: "session-1",
			TurnID:    "turn-1",
			Type:      string(hooks.RunStarted),
			Payload:   []byte(`{}`),
			Timestamp: time.Unix(int64(i), 0).UTC(),
		})
	}
	return docs
}

func fakeSessionEventDocuments(sessionID string, n int) []eventDocument {
	docs := make([]eventDocument, 0, n)
	for i := 1; i <= n; i++ {
		oid := bson.ObjectID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, byte(i)}
		docs = append(docs, eventDocument{
			ID:        oid,
			EventKey:  fmt.Sprintf("evt-%d", i),
			RunID:     fmt.Sprintf("run-%d", i),
			AgentID:   "agent-1",
			SessionID: sessionID,
			TurnID:    "turn-1",
			Type:      string(hooks.RunStarted),
			Payload:   []byte(`{}`),
			Timestamp: time.Unix(int64(i), 0).UTC(),
		})
	}
	return docs
}

func mustOID(t *testing.T, hex string) bson.ObjectID {
	t.Helper()

	oid, err := bson.ObjectIDFromHex(hex)
	if !assert.NoError(t, err) {
		t.FailNow()
	}
	return oid
}

type fakeCollection struct {
	insertedID bson.ObjectID
	findDocs   []eventDocument
	findOneDoc eventDocument
	insertErr  error
}

func (c *fakeCollection) InsertOne(context.Context, any, ...options.Lister[options.InsertOneOptions]) (*mongodriver.InsertOneResult, error) {
	if c.insertErr != nil {
		return nil, c.insertErr
	}
	return &mongodriver.InsertOneResult{InsertedID: c.insertedID}, nil
}

func (c *fakeCollection) FindOne(_ context.Context, _ any, _ ...options.Lister[options.FindOneOptions]) singleResult {
	return fakeSingleResult{doc: c.findOneDoc}
}

func (c *fakeCollection) Find(_ context.Context, filter any, opts ...options.Lister[options.FindOptions]) (cursor, error) {
	f, ok := filter.(bson.M)
	if !ok {
		return &fakeCursor{}, nil
	}

	runID, _ := f["run_id"].(string)
	sessionID, _ := f["session_id"].(string)
	var after bson.ObjectID
	if id, ok := f["_id"].(bson.M); ok {
		if gt, ok := id["$gt"].(bson.ObjectID); ok {
			after = gt
		}
	}

	filtered := make([]eventDocument, 0, len(c.findDocs))
	for _, doc := range c.findDocs {
		if runID != "" && doc.RunID != runID {
			continue
		}
		if sessionID != "" && doc.SessionID != sessionID {
			continue
		}
		if !after.IsZero() && bytes.Compare(doc.ID[:], after[:]) <= 0 {
			continue
		}
		filtered = append(filtered, doc)
	}

	var limit int64
	if len(opts) > 0 && opts[0] != nil {
		findOpts := new(options.FindOptions)
		for _, apply := range opts[0].List() {
			if err := apply(findOpts); err != nil {
				panic(err)
			}
		}
		if findOpts.Limit != nil {
			limit = *findOpts.Limit
		}
	}
	if limit > 0 && int64(len(filtered)) > limit {
		filtered = filtered[:limit]
	}

	return &fakeCursor{docs: filtered}, nil
}

func (c *fakeCollection) Indexes() indexView {
	return fakeIndexView{}
}

type fakeIndexView struct{}

func (fakeIndexView) CreateOne(context.Context, mongodriver.IndexModel, ...options.Lister[options.CreateIndexesOptions]) (string, error) {
	return "", nil
}

type fakeCursor struct {
	docs []eventDocument
	pos  int
	err  error
}

func (c *fakeCursor) Next(context.Context) bool {
	if c.err != nil {
		return false
	}
	if c.pos >= len(c.docs) {
		return false
	}
	c.pos++
	return true
}

func (c *fakeCursor) Decode(val any) error {
	if c.err != nil {
		return c.err
	}
	if c.pos == 0 || c.pos > len(c.docs) {
		return nil
	}
	p, ok := val.(*eventDocument)
	if !ok {
		return nil
	}
	*p = c.docs[c.pos-1]
	return nil
}

func (c *fakeCursor) Err() error {
	return c.err
}

func (c *fakeCursor) Close(context.Context) error {
	return nil
}

type fakeSingleResult struct {
	doc eventDocument
	err error
}

func (r fakeSingleResult) Decode(val any) error {
	if r.err != nil {
		return r.err
	}
	p, ok := val.(*eventDocument)
	if !ok {
		return nil
	}
	*p = r.doc
	return nil
}
