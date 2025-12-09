package mongo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	clientsmongo "goa.design/goa-ai/features/session/mongo/clients/mongo"
	mockmongo "goa.design/goa-ai/features/session/mongo/clients/mongo/mocks"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestNewStoreRequiresClient(t *testing.T) {
	_, err := NewStore(nil)
	require.EqualError(t, err, "client is required")
}

func TestUpsertDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	run := session.Run{RunID: "run", AgentID: "agent"}
	mockClient.AddUpsertRun(func(ctx context.Context, r session.Run) error {
		require.Equal(t, run, r)
		return nil
	})
	store, err := NewStore(mockClient)
	require.NoError(t, err)

	require.NoError(t, store.Upsert(context.Background(), run))
	require.False(t, mockClient.HasMore())
}

func TestLoadDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	expected := session.Run{RunID: "run", AgentID: "agent"}
	mockClient.AddLoadRun(func(ctx context.Context, runID string) (session.Run, error) {
		require.Equal(t, "run", runID)
		return expected, nil
	})
	store, err := NewStore(mockClient)
	require.NoError(t, err)

	actual, err := store.Load(context.Background(), "run")
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.False(t, mockClient.HasMore())
}

func TestNewClientValidatesOptions(t *testing.T) {
	_, err := clientsmongo.New(clientsmongo.Options{})
	require.EqualError(t, err, "mongo client is required")
}
