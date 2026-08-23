package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/model"
)

func TestNewOpenAIModelClientRequiresAPIKey(t *testing.T) {
	rt := &Runtime{}

	client, err := rt.NewOpenAIModelClient(OpenAIConfig{DefaultModel: "gpt-5"})
	require.Error(t, err)
	require.Nil(t, client)
	require.Contains(t, err.Error(), "api key is required")
}

func TestNewOpenAIModelClientBuildsStatelessClient(t *testing.T) {
	rt := &Runtime{}

	client, err := rt.NewOpenAIModelClient(OpenAIConfig{
		APIKey:       "sk-test",
		DefaultModel: "gpt-5",
	})
	require.NoError(t, err)

	require.NoError(t, model.ValidateClient(client))
}

func TestNewVertexGeminiModelClientValidates(t *testing.T) {
	rt := &Runtime{}

	client, err := rt.NewVertexGeminiModelClient(context.Background(), VertexConfig{Location: "global", DefaultModel: "gemini-2.5-pro"})
	require.Error(t, err)
	require.Nil(t, client)
	require.Contains(t, err.Error(), "project id is required")

	client, err = rt.NewVertexGeminiModelClient(context.Background(), VertexConfig{ProjectID: "p", DefaultModel: "gemini-2.5-pro"})
	require.Error(t, err)
	require.Nil(t, client)
	require.Contains(t, err.Error(), "location is required")
}

func TestNewVertexAnthropicModelClientValidates(t *testing.T) {
	rt := &Runtime{}

	client, err := rt.NewVertexAnthropicModelClient(context.Background(), VertexConfig{ProjectID: "p", Location: "global"})
	require.Error(t, err)
	require.Nil(t, client)
	require.Contains(t, err.Error(), "default model is required")
}
