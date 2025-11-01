package search

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	"go.mongodb.org/mongo-driver/mongo/options"

	"goa.design/goa-ai/runtime/agents/run"
)

func TestSessionsBuildsFilter(t *testing.T) {
	now := time.Now()
	docs := []any{
		sessionDocument{
			ID:        primitive.NewObjectID(),
			RunID:     "run-1",
			SessionID: "sess-1",
			OrgID:     "org",
			AgentID:   "agent",
			Principal: principalDoc{ID: "user"},
			Status:    run.StatusRunning,
			CreatedAt: now.Add(-time.Hour),
			UpdatedAt: now,
		},
	}
	sess := &fakeCollection{docs: docs}
	events := &fakeCollection{}
	repo, err := NewSearchRepository(SearchOptions{Sessions: sess, Events: events})
	require.NoError(t, err)

	createdFrom := now.Add(-24 * time.Hour)
	query := SessionSearchQuery{
		OrgIDs:       []string{"org"},
		AgentIDs:     []string{"agent"},
		PrincipalIDs: []string{"user"},
		CreatedFrom:  &createdFrom,
		LastEventTo:  &now,
		Limit:        1,
		SortField:    SortByLastEvent,
		Descending:   true,
		Cursor:       &SessionCursor{Timestamp: now, ID: primitive.NewObjectID()},
	}

	res, err := repo.Sessions(context.Background(), query)
	require.NoError(t, err)
	require.Len(t, res.Sessions, 1)
	require.Equal(t, "run-1", res.Sessions[0].RunID)
	require.NotNil(t, res.NextCursor)

	filter := sess.filter.(bson.M)
	require.Equal(t, bson.M{"$in": []string{"org"}}, filter["org_id"])
	require.Equal(t, bson.M{"$exists": false}, filter["deleted_at"])
	require.NotNil(t, filter["$or"])
	opts := sess.options[0]
	require.Equal(t, int64(1), *opts.Limit)
}

func TestFailuresFilter(t *testing.T) {
	docs := []any{
		eventDocument{
			ID:         primitive.NewObjectID(),
			RunID:      "run-2",
			ToolName:   "tool",
			ResultCode: "error",
			OccurredAt: time.Now(),
		},
	}
	events := &fakeCollection{docs: docs}
	repo, err := NewSearchRepository(SearchOptions{Sessions: &fakeCollection{}, Events: events})
	require.NoError(t, err)

	from := time.Now().Add(-time.Hour)
	_, _, err = repo.Failures(context.Background(), FailureQuery{
		OrgIDs:      []string{"org"},
		ToolNames:   []string{"tool"},
		ResultCodes: []string{"error"},
		From:        &from,
		Limit:       1,
	})
	require.NoError(t, err)

	filter := events.filter.(bson.M)
	require.Equal(t, "tool_result", filter["type"])
	require.NotNil(t, filter["org_id"])
}

func TestNewSearchRepositoryRequiresCollections(t *testing.T) {
	_, err := NewSearchRepository(SearchOptions{})
	require.EqualError(t, err, "sessions collection is required")
}

type fakeCollection struct {
	filter  any
	options []*options.FindOptions
	docs    []any
}

func (f *fakeCollection) Find(ctx context.Context, filter any, opts ...*options.FindOptions) (cursor, error) {
	f.filter = filter
	f.options = opts
	return &fakeCursor{docs: f.docs}, nil
}

type fakeCursor struct {
	docs []any
	idx  int
}

func (c *fakeCursor) Next(ctx context.Context) bool {
	if c.idx >= len(c.docs) {
		return false
	}
	c.idx++
	return true
}

func (c *fakeCursor) Decode(val any) error {
	doc := c.docs[c.idx-1]
	switch v := val.(type) {
	case *sessionDocument:
		*v = doc.(sessionDocument)
	case *eventDocument:
		*v = doc.(eventDocument)
	default:
		return errors.New("unexpected decode target")
	}
	return nil
}

func (c *fakeCursor) Close(ctx context.Context) error { return nil }
