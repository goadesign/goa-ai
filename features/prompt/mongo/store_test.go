package mongo

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	clientsmongo "goa.design/goa-ai/features/prompt/mongo/clients/mongo"
	mockmongo "goa.design/goa-ai/features/prompt/mongo/clients/mongo/mocks"
	"goa.design/goa-ai/runtime/agent/prompt"
)

func TestNewStoreRequiresClient(t *testing.T) {
	t.Parallel()

	_, err := NewStore(nil)
	require.EqualError(t, err, "client is required")
}

func TestResolveDelegatesToClient(t *testing.T) {
	t.Parallel()

	mockClient := mockmongo.NewClient(t)
	expected := &prompt.Override{
		PromptID: "example.agent.system",
		Template: "template",
		Version:  "sha256:abc",
	}
	mockClient.AddResolve(func(ctx context.Context, promptID prompt.Ident, scope prompt.Scope) (*prompt.Override, error) {
		require.Equal(t, prompt.Ident("example.agent.system"), promptID)
		require.Equal(t, "acme", scope.Labels["account"])
		return expected, nil
	})

	store, err := NewStore(mockClient)
	require.NoError(t, err)

	actual, err := store.Resolve(context.Background(), "example.agent.system", prompt.Scope{
		Labels: map[string]string{
			"account": "acme",
		},
	})
	require.NoError(t, err)
	require.Equal(t, expected, actual)
	require.False(t, mockClient.HasMore())
}

func TestSetDelegatesToClient(t *testing.T) {
	t.Parallel()

	mockClient := mockmongo.NewClient(t)
	mockClient.AddSet(func(ctx context.Context, promptID prompt.Ident, scope prompt.Scope, template string, metadata map[string]string) error {
		require.Equal(t, prompt.Ident("example.agent.system"), promptID)
		require.Equal(t, "acme", scope.Labels["account"])
		require.Equal(t, "template", template)
		require.Equal(t, "exp_1", metadata["experiment_id"])
		return nil
	})

	store, err := NewStore(mockClient)
	require.NoError(t, err)

	err = store.Set(context.Background(), "example.agent.system", prompt.Scope{
		Labels: map[string]string{
			"account": "acme",
		},
	}, "template", map[string]string{
		"experiment_id": "exp_1",
	})
	require.NoError(t, err)
	require.False(t, mockClient.HasMore())
}

func TestHistoryDelegatesToClient(t *testing.T) {
	t.Parallel()

	mockClient := mockmongo.NewClient(t)
	expected := []*prompt.Override{
		{PromptID: "example.agent.system", Template: "v2"},
	}
	mockClient.AddHistory(func(ctx context.Context, promptID prompt.Ident) ([]*prompt.Override, error) {
		require.Equal(t, prompt.Ident("example.agent.system"), promptID)
		return expected, nil
	})

	store, err := NewStore(mockClient)
	require.NoError(t, err)

	history, err := store.History(context.Background(), "example.agent.system")
	require.NoError(t, err)
	require.Equal(t, expected, history)
	require.False(t, mockClient.HasMore())
}

func TestListDelegatesToClient(t *testing.T) {
	t.Parallel()

	mockClient := mockmongo.NewClient(t)
	expected := []*prompt.Override{
		{PromptID: "example.agent.system", Template: "v2"},
	}
	mockClient.AddList(func(ctx context.Context) ([]*prompt.Override, error) {
		return expected, nil
	})

	store, err := NewStore(mockClient)
	require.NoError(t, err)

	list, err := store.List(context.Background())
	require.NoError(t, err)
	require.Equal(t, expected, list)
	require.False(t, mockClient.HasMore())
}

func TestNewClientValidatesOptions(t *testing.T) {
	t.Parallel()

	_, err := clientsmongo.New(clientsmongo.Options{})
	require.EqualError(t, err, "mongo client is required")
}
