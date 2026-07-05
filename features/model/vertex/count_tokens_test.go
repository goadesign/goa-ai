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

// TestCountTokensIncludesSystemInstructionAndTools verifies that CountTokens
// passes the request's system instruction and tool declarations to Vertex's
// native counter (a nil config undercounts requests that rely on a system
// prompt and/or tool schemas, both of which consume tokens).
func TestCountTokensIncludesSystemInstructionAndTools(t *testing.T) {
	stub := &stubGenerativeClient{countResp: &genai.CountTokensResponse{TotalTokens: 99}}
	cl, err := New(stub, Options{DefaultModel: "gemini-2.5-pro"})
	require.NoError(t, err)
	def := toolDef(t, "feed/find_duplicates", `{"type":"object"}`)
	_, err = cl.CountTokens(context.Background(), &model.Request{
		Messages: []*model.Message{
			{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "be terse"}}},
			{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "hi"}}},
		},
		Tools: []*model.ToolDefinition{def},
	})
	require.NoError(t, err)
	require.NotNil(t, stub.lastCountConfig)
	require.NotNil(t, stub.lastCountConfig.SystemInstruction)
	assert.Equal(t, "be terse", stub.lastCountConfig.SystemInstruction.Parts[0].Text)
	require.Len(t, stub.lastCountConfig.Tools, 1)
}

var _ model.TokenCounter = (*Client)(nil)
