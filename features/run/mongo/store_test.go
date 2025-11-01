package mongo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	clientsmongo "goa.design/goa-ai/features/run/mongo/clients/mongo"
	mockmongo "goa.design/goa-ai/features/run/mongo/clients/mongo/mocks"
	"goa.design/goa-ai/runtime/agents/run"
)

func TestNewStoreRequiresClient(t *testing.T) {
	_, err := NewStore(Options{})
	require.EqualError(t, err, "client is required")
}

func TestUpsertDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	rec := run.Record{RunID: "run", AgentID: "agent"}
	mockClient.AddUpsertRun(func(ctx context.Context, r run.Record) error {
		require.Equal(t, rec, r)
		return nil
	})
	store, err := NewStore(Options{Client: mockClient})
	require.NoError(t, err)

	require.NoError(t, store.Upsert(context.Background(), rec))
	require.False(t, mockClient.HasMore())
}

func TestLoadDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	expected := run.Record{RunID: "run", AgentID: "agent"}
	mockClient.AddLoadRun(func(ctx context.Context, runID string) (run.Record, error) {
		require.Equal(t, "run", runID)
		return expected, nil
	})
	store, err := NewStore(Options{Client: mockClient})
	require.NoError(t, err)

	actual, err := store.Load(context.Background(), "run")
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.False(t, mockClient.HasMore())
}

func TestNewStoreFromMongoValidatesOptions(t *testing.T) {
	_, err := NewStoreFromMongo(clientsmongo.Options{})
	require.EqualError(t, err, "mongo client is required")
}
