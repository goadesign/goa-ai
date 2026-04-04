package runtime

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/converter"
	openaifeature "goa.design/goa-ai/features/model/openai"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
)

type queryableEngine struct {
	engine.Engine
}

func (e *queryableEngine) QueryWorkflow(ctx context.Context, workflowID, queryType string, args ...any) (converter.EncodedValue, error) {
	return nil, nil
}

func TestNewOpenAIModelClientRequiresAPIKey(t *testing.T) {
	rt := &Runtime{}

	client, err := rt.NewOpenAIModelClient(OpenAIConfig{DefaultModel: "gpt-5"})
	require.Error(t, err)
	require.Nil(t, client)
	require.Contains(t, err.Error(), "api key is required")
}

func TestNewOpenAIModelClientWiresLedgerSourceWhenEngineSupportsQueries(t *testing.T) {
	rt := &Runtime{
		Engine: &queryableEngine{Engine: engineinmem.New()},
	}

	client, err := rt.NewOpenAIModelClient(OpenAIConfig{
		APIKey:       "sk-test",
		DefaultModel: "gpt-5",
	})
	require.NoError(t, err)

	openaiClient, ok := client.(*openaifeature.Client)
	require.True(t, ok)
	value := reflect.ValueOf(openaiClient).Elem()
	require.Equal(t, "gpt-5", value.FieldByName("defaultModel").String())
	require.False(t, value.FieldByName("ledger").IsNil())
}

func TestNewOpenAIModelClientLeavesLedgerNilWithoutWorkflowQueries(t *testing.T) {
	rt := &Runtime{
		Engine: engineinmem.New(),
	}

	client, err := rt.NewOpenAIModelClient(OpenAIConfig{
		APIKey:       "sk-test",
		DefaultModel: "gpt-5",
	})
	require.NoError(t, err)

	openaiClient, ok := client.(*openaifeature.Client)
	require.True(t, ok)
	require.True(t, reflect.ValueOf(openaiClient).Elem().FieldByName("ledger").IsNil())
}
