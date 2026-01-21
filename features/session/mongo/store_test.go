package mongo

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	mockmongo "goa.design/goa-ai/features/session/mongo/clients/mongo/mocks"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestNewStoreRequiresClient(t *testing.T) {
	_, err := NewStore(nil)
	require.EqualError(t, err, "client is required")
}

func TestCreateSessionDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	now := time.Now().UTC()
	expected := session.Session{
		ID:        "sess-1",
		Status:    session.StatusActive,
		CreatedAt: now,
		EndedAt:   nil,
	}
	mockClient.AddCreateSession(func(ctx context.Context, id string, createdAt time.Time) (session.Session, error) {
		require.Equal(t, "sess-1", id)
		require.Equal(t, now, createdAt)
		return expected, nil
	})
	store, err := NewStore(mockClient)
	require.NoError(t, err)

	sess, err := store.CreateSession(context.Background(), "sess-1", now)
	require.NoError(t, err)
	require.Equal(t, expected, sess)
	require.False(t, mockClient.HasMore())
}

func TestLoadSessionDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	now := time.Now().UTC()
	expected := session.Session{
		ID:        "sess-1",
		Status:    session.StatusActive,
		CreatedAt: now,
	}
	mockClient.AddLoadSession(func(ctx context.Context, id string) (session.Session, error) {
		require.Equal(t, "sess-1", id)
		return expected, nil
	})
	store, err := NewStore(mockClient)
	require.NoError(t, err)

	actual, err := store.LoadSession(context.Background(), "sess-1")
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.False(t, mockClient.HasMore())
}

func TestEndSessionDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	now := time.Now().UTC()
	end := now.Add(time.Minute)
	expected := session.Session{
		ID:        "sess-1",
		Status:    session.StatusEnded,
		CreatedAt: now,
		EndedAt:   &end,
	}
	mockClient.AddEndSession(func(ctx context.Context, id string, endedAt time.Time) (session.Session, error) {
		require.Equal(t, "sess-1", id)
		require.Equal(t, end, endedAt)
		return expected, nil
	})
	store, err := NewStore(mockClient)
	require.NoError(t, err)

	actual, err := store.EndSession(context.Background(), "sess-1", end)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.False(t, mockClient.HasMore())
}

func TestUpsertRunDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	run := session.RunMeta{
		RunID:     "run-1",
		AgentID:   "agent",
		SessionID: "sess-1",
		Status:    session.RunStatusRunning,
	}
	mockClient.AddUpsertRun(func(ctx context.Context, r session.RunMeta) error {
		require.Equal(t, run, r)
		return nil
	})
	store, err := NewStore(mockClient)
	require.NoError(t, err)

	require.NoError(t, store.UpsertRun(context.Background(), run))
	require.False(t, mockClient.HasMore())
}

func TestLoadRunDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	expected := session.RunMeta{RunID: "run-1", AgentID: "agent", SessionID: "sess-1"}
	mockClient.AddLoadRun(func(ctx context.Context, runID string) (session.RunMeta, error) {
		require.Equal(t, "run-1", runID)
		return expected, nil
	})
	store, err := NewStore(mockClient)
	require.NoError(t, err)

	actual, err := store.LoadRun(context.Background(), "run-1")
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.False(t, mockClient.HasMore())
}

func TestListRunsBySessionDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	expected := []session.RunMeta{
		{RunID: "run-1", AgentID: "agent", SessionID: "sess-1", Status: session.RunStatusRunning},
		{RunID: "run-2", AgentID: "agent", SessionID: "sess-1", Status: session.RunStatusPending},
	}
	statuses := []session.RunStatus{session.RunStatusRunning, session.RunStatusPending}
	mockClient.AddListRunsBySession(func(ctx context.Context, sessionID string, st []session.RunStatus) ([]session.RunMeta, error) {
		require.Equal(t, "sess-1", sessionID)
		require.Equal(t, statuses, st)
		return expected, nil
	})
	store, err := NewStore(mockClient)
	require.NoError(t, err)

	actual, err := store.ListRunsBySession(context.Background(), "sess-1", statuses)
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.False(t, mockClient.HasMore())
}
