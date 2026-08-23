// Package runtime_test verifies public history compression rejects forged
// implementations of the opaque model client before any inference operation.
package runtime_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	agentruntime "goa.design/goa-ai/runtime/agent/runtime"
)

type (
	historyValidationProvider struct {
		calls    int
		countErr error
	}

	forgedHistoryClient struct {
		model.Client
	}
)

func (p *historyValidationProvider) Complete(context.Context, *model.Request) (*model.Response, error) {
	p.calls++
	return &model.Response{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: "summary"}},
		}},
		StopReason: "stop",
	}, nil
}

func (p *historyValidationProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	p.calls++
	return nil, model.ErrStreamingUnsupported
}

func (p *historyValidationProvider) CountTokens(context.Context, *model.Request) (model.TokenCount, error) {
	p.calls++
	return model.TokenCount{InputTokens: 1, Exact: true}, p.countErr
}

func TestCompressRejectsForgedEmbeddedClientBeforeInference(t *testing.T) {
	provider := &historyValidationProvider{}
	client, err := model.NewClient(provider)
	require.NoError(t, err)
	forged := &forgedHistoryClient{Client: client}
	policy := agentruntime.Compress(forged, agentruntime.HistoryCompressionConfig{
		CompressAtTurns: 2,
		KeepMaxTurns:    1,
	})
	messages := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "first"}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "answer"}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "second"}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "answer"}}},
	}

	got, err := policy(t.Context(), messages, nil)

	require.ErrorContains(t, err, "model client is not an intact validated client")
	require.Equal(t, messages, got)
	require.Zero(t, provider.calls)
}
