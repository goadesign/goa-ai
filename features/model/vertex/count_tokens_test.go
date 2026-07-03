package vertex

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestCountTokens(t *testing.T) {
	stub := &stubGenerativeClient{countResp: &genai.CountTokensResponse{TotalTokens: 42}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	count, err := cl.CountTokens(context.Background(), &model.Request{
		ModelClass: model.ModelClassSmall,
		Messages: []*model.Message{
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
			{Role: model.ConversationRoleAssistant, Parts: []model.Part{
				model.ThinkingPart{Text: "secret reasoning", Final: true},
				model.TextPart{Text: "answer"},
			}},
		},
	})
	require.NoError(t, err)
	assert.Equal(t, 42, count.InputTokens)
	assert.True(t, count.Exact)
	assert.Equal(t, model.ModelClassSmall, count.ModelClass)
	// Thinking parts must not be encoded into the counted contents.
	for _, content := range stub.lastContents {
		for _, part := range content.Parts {
			assert.False(t, part.Thought, "thinking part leaked into token counting")
		}
	}
}

var _ model.TokenCounter = (*Client)(nil)
