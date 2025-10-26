package mongo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/agents/runtime/memory"
	clientsmongo "goa.design/goa-ai/features/memory/mongo/clients/mongo"
	mockmongo "goa.design/goa-ai/features/memory/mongo/clients/mongo/mocks"
)

func TestNewStoreRequiresClient(t *testing.T) {
	_, err := NewStore(Options{})
	require.EqualError(t, err, "client is required")
}

func TestLoadRunDelegatesToClient(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	expected := memory.Snapshot{AgentID: "agent", RunID: "run"}
	mockClient.AddLoadRun(func(ctx context.Context, agentID, runID string) (memory.Snapshot, error) {
		require.Equal(t, "agent", agentID)
		require.Equal(t, "run", runID)
		return expected, nil
	})

	store, err := NewStore(Options{Client: mockClient})
	require.NoError(t, err)

	actual, err := store.LoadRun(context.Background(), "agent", "run")
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.False(t, mockClient.HasMore())
}

func TestAppendEventsSkipsEmpty(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	store, err := NewStore(Options{Client: mockClient})
	require.NoError(t, err)

	err = store.AppendEvents(context.Background(), "agent", "run")
	require.NoError(t, err)
	require.False(t, mockClient.HasMore())
}

func TestAppendEventsDelegates(t *testing.T) {
	mockClient := mockmongo.NewClient(t)
	mockClient.AddAppendEvents(func(ctx context.Context, agentID, runID string, events []memory.Event) error {
		require.Equal(t, "agent", agentID)
		require.Equal(t, "run", runID)
		require.Len(t, events, 1)
		require.Equal(t, memory.EventToolCall, events[0].Type)
		return nil
	})
	store, err := NewStore(Options{Client: mockClient})
	require.NoError(t, err)

	err = store.AppendEvents(context.Background(), "agent", "run", memory.Event{Type: memory.EventToolCall})
	require.NoError(t, err)
	require.False(t, mockClient.HasMore())
}

func TestNewStoreFromMongoValidatesOptions(t *testing.T) {
	_, err := NewStoreFromMongo(clientsmongo.Options{})
	require.EqualError(t, err, "mongo client is required")
}
