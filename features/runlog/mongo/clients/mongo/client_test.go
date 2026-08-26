package mongo

import (
	"context"
	"fmt"
	"sort"
	"sync"
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
	c := newTestClient(coll)

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
	assert.Equal(t, "1", e.ID)
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
			wantNext:   "3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			runID := "run-1"
			coll := &fakeCollection{
				findDocs: fakeEventDocuments(runID, tc.eventCount),
			}
			c := newTestClient(coll)

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
			wantNext:   "3",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			sessionID := "session-1"
			coll := &fakeCollection{
				findDocs: fakeSessionEventDocuments(sessionID, tc.eventCount),
			}
			c := newTestClient(coll)

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
	c := newTestClient(coll)

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
		Stream:    "session:session-1",
		Sequence:  1,
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
	require.Equal(t, "1", second.ID)
	require.Equal(t, "1", dup.ID)
}

func TestClientListUsesSequenceWhenObjectIDsAreOutOfOrder(t *testing.T) {
	t.Parallel()

	coll := &fakeCollection{
		findDocs: []eventDocument{
			fakeEventDocument("run-1", "evt-3", 3, mustOID(t, "000000000000000000000001")),
			fakeEventDocument("run-1", "evt-1", 1, mustOID(t, "000000000000000000000003")),
			fakeEventDocument("run-1", "evt-2", 2, mustOID(t, "000000000000000000000002")),
		},
	}
	client := newTestClient(coll)

	page, err := client.List(context.Background(), "run-1", "", 10)

	require.NoError(t, err)
	require.Len(t, page.Events, 3)
	assert.Equal(t, []string{"evt-1", "evt-2", "evt-3"}, []string{
		page.Events[0].EventKey,
		page.Events[1].EventKey,
		page.Events[2].EventKey,
	})
	assert.Equal(t, []string{"1", "2", "3"}, []string{
		page.Events[0].ID,
		page.Events[1].ID,
		page.Events[2].ID,
	})
}

func TestClientAppendSerializesConcurrentSessionEvents(t *testing.T) {
	t.Parallel()

	const count = 32
	ids := make([]bson.ObjectID, count)
	for i := range count {
		ids[i] = bson.ObjectID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, byte(count - i)}
	}
	coll := &fakeCollection{insertedIDs: ids}
	client := newTestClient(coll)
	results := make(chan runlog.AppendResult, count)
	errs := make(chan error, count)
	var group sync.WaitGroup
	for i := range count {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			result, err := client.Append(context.Background(), &runlog.Event{
				EventKey:  fmt.Sprintf("evt-%d", index),
				RunID:     fmt.Sprintf("run-%d", index%2),
				SessionID: "session-1",
				Type:      hooks.RunStarted,
				Payload:   []byte(`{}`),
				Timestamp: time.Unix(int64(index+1), 0).UTC(),
			})
			results <- result
			errs <- err
		}(i)
	}
	group.Wait()
	close(results)
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	seen := make(map[string]struct{}, count)
	for result := range results {
		require.True(t, result.Inserted)
		seen[result.ID] = struct{}{}
	}
	require.Len(t, seen, count)
	for sequence := 1; sequence <= count; sequence++ {
		_, ok := seen[fmt.Sprintf("%d", sequence)]
		assert.True(t, ok, "missing sequence %d", sequence)
	}

	page, err := client.ListSession(context.Background(), "session-1", "", count)
	require.NoError(t, err)
	require.Len(t, page.Events, count)
	for i, event := range page.Events {
		assert.Equal(t, fmt.Sprintf("%d", i+1), event.ID)
	}
}

func TestClientAppendExactRetryIgnoresAttemptTimestamp(t *testing.T) {
	t.Parallel()

	coll := &fakeCollection{}
	client := newTestClient(coll)
	first := testRunlogEvent("evt-1", []byte(`{"ok":true}`))
	firstResult, err := client.Append(context.Background(), first)
	require.NoError(t, err)
	require.True(t, firstResult.Inserted)

	retry := testRunlogEvent("evt-1", []byte(`{"ok":true}`))
	retry.Timestamp = retry.Timestamp.Add(time.Minute)
	retryResult, err := client.Append(context.Background(), retry)
	require.NoError(t, err)
	require.False(t, retryResult.Inserted)
	assert.Equal(t, "1", retryResult.ID)

	next := testRunlogEvent("evt-2", []byte(`{}`))
	nextResult, err := client.Append(context.Background(), next)
	require.NoError(t, err)
	assert.Equal(t, "2", nextResult.ID)
}

func TestClientAppendRejectsConflictingRetry(t *testing.T) {
	t.Parallel()

	client := newTestClient(&fakeCollection{})
	_, err := client.Append(context.Background(), testRunlogEvent("evt-1", []byte(`{"ok":true}`)))
	require.NoError(t, err)

	_, err = client.Append(context.Background(), testRunlogEvent("evt-1", []byte(`{"ok":false}`)))
	require.EqualError(t, err, `event key "evt-1" conflicts with existing event body`)
}

func TestRequireSchemaRejectsAbsentAndWrongSentinel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		result  singleResult
		wantErr string
	}{
		{
			name:    "absent",
			result:  fakeSingleResult{err: mongodriver.ErrNoDocuments},
			wantErr: "runlog Mongo schema migration 1 is required",
		},
		{
			name:    "wrong_version",
			result:  fakeSingleResult{schema: schemaDocument{Name: schemaSentinelID, Version: 2}},
			wantErr: "runlog Mongo schema version 1 is required, found 2",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := requireSchema(context.Background(), &fakeCollection{findOneResult: test.result})
			require.EqualError(t, err, test.wantErr)
		})
	}
}

func fakeEventDocument(runID, eventKey string, sequence int64, id bson.ObjectID) eventDocument {
	return eventDocument{
		ID:        id,
		Stream:    "session:session-1",
		Sequence:  sequence,
		EventKey:  eventKey,
		RunID:     runID,
		AgentID:   "agent-1",
		SessionID: "session-1",
		Type:      string(hooks.RunStarted),
		Payload:   []byte(`{}`),
		Timestamp: time.Unix(sequence, 0).UTC(),
	}
}

func testRunlogEvent(eventKey string, payload []byte) *runlog.Event {
	return &runlog.Event{
		EventKey:  eventKey,
		RunID:     "run-1",
		AgentID:   "agent-1",
		SessionID: "session-1",
		Type:      hooks.RunStarted,
		Payload:   payload,
		Timestamp: time.Unix(1, 0).UTC(),
	}
}

func fakeEventDocuments(runID string, n int) []eventDocument {
	docs := make([]eventDocument, 0, n)
	for i := 1; i <= n; i++ {
		oid := bson.ObjectID{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, byte(i)}
		docs = append(docs, eventDocument{
			ID:        oid,
			Stream:    "session:session-1",
			Sequence:  int64(i),
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
			Stream:    "session:" + sessionID,
			Sequence:  int64(i),
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
	insertedID    bson.ObjectID
	insertedIDs   []bson.ObjectID
	findDocs      []eventDocument
	findOneDoc    eventDocument
	findOneResult singleResult
	insertErr     error
	sequences     map[string]int64
}

func (c *fakeCollection) InsertOne(_ context.Context, value any, _ ...options.Lister[options.InsertOneOptions]) (*mongodriver.InsertOneResult, error) {
	if c.insertErr != nil {
		return nil, c.insertErr
	}
	doc, ok := value.(eventDocument)
	if ok {
		for _, existing := range c.findDocs {
			if existing.RunID == doc.RunID && existing.EventKey == doc.EventKey {
				return nil, duplicateKeyError()
			}
			if existing.Stream == doc.Stream && existing.Sequence == doc.Sequence {
				return nil, duplicateKeyError()
			}
		}
		doc.ID = c.nextObjectID()
		c.findDocs = append(c.findDocs, doc)
		return &mongodriver.InsertOneResult{InsertedID: doc.ID}, nil
	}
	return &mongodriver.InsertOneResult{InsertedID: c.nextObjectID()}, nil
}

func (c *fakeCollection) FindOne(_ context.Context, filter any, _ ...options.Lister[options.FindOneOptions]) singleResult {
	if c.findOneResult != nil {
		return c.findOneResult
	}
	if c.findOneDoc.RunID != "" || c.findOneDoc.Stream != "" {
		return fakeSingleResult{doc: c.findOneDoc}
	}
	f, _ := filter.(bson.M)
	runID, _ := f["run_id"].(string)
	eventKey, _ := f["event_key"].(string)
	for _, doc := range c.findDocs {
		if doc.RunID == runID && doc.EventKey == eventKey {
			return fakeSingleResult{doc: doc}
		}
	}
	return fakeSingleResult{err: mongodriver.ErrNoDocuments}
}

func (c *fakeCollection) FindOneAndUpdate(_ context.Context, filter, _ any, _ ...options.Lister[options.FindOneAndUpdateOptions]) singleResult {
	f, _ := filter.(bson.M)
	stream, _ := f["_id"].(string)
	if c.sequences == nil {
		c.sequences = make(map[string]int64)
	}
	c.sequences[stream]++
	return fakeSingleResult{sequence: sequenceDocument{
		Stream:   stream,
		Sequence: c.sequences[stream],
	}}
}

func (c *fakeCollection) Find(_ context.Context, filter any, opts ...options.Lister[options.FindOptions]) (cursor, error) {
	f, ok := filter.(bson.M)
	if !ok {
		return &fakeCursor{}, nil
	}

	runID, _ := f["run_id"].(string)
	sessionID, _ := f["session_id"].(string)
	var after int64
	if value, ok := f["sequence"].(bson.M); ok {
		if gt, ok := value["$gt"].(int64); ok {
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
		if doc.Sequence <= after {
			continue
		}
		filtered = append(filtered, doc)
	}
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Sequence < filtered[j].Sequence
	})

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
	doc      eventDocument
	sequence sequenceDocument
	schema   schemaDocument
	err      error
}

func (r fakeSingleResult) Decode(val any) error {
	if r.err != nil {
		return r.err
	}
	p, ok := val.(*eventDocument)
	if ok {
		*p = r.doc
		return nil
	}
	counter, ok := val.(*sequenceDocument)
	if ok {
		*counter = r.sequence
		return nil
	}
	sentinel, ok := val.(*schemaDocument)
	if ok {
		*sentinel = r.schema
	}
	return nil
}

type fakeTransactionRunner struct {
	mu        sync.Mutex
	events    *fakeCollection
	sequences *fakeCollection
}

func (r *fakeTransactionRunner) WithTransaction(ctx context.Context, fn func(context.Context) error) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	eventCount := len(r.events.findDocs)
	sequenceSnapshot := make(map[string]int64, len(r.sequences.sequences))
	for stream, sequence := range r.sequences.sequences {
		sequenceSnapshot[stream] = sequence
	}
	err := fn(ctx)
	if err != nil {
		r.events.findDocs = r.events.findDocs[:eventCount]
		r.sequences.sequences = sequenceSnapshot
	}
	return err
}

func newTestClient(events *fakeCollection) *client {
	sequences := &fakeCollection{sequences: make(map[string]int64)}
	return &client{
		coll:         events,
		sequences:    sequences,
		transactions: &fakeTransactionRunner{events: events, sequences: sequences},
	}
}

func (c *fakeCollection) nextObjectID() bson.ObjectID {
	if len(c.insertedIDs) > 0 {
		id := c.insertedIDs[0]
		c.insertedIDs = c.insertedIDs[1:]
		return id
	}
	if !c.insertedID.IsZero() {
		return c.insertedID
	}
	return bson.NewObjectID()
}

func duplicateKeyError() error {
	return mongodriver.WriteException{
		WriteErrors: []mongodriver.WriteError{
			{Code: 11000, Message: "duplicate key"},
		},
	}
}
