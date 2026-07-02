package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type historyCountingClient struct {
	counted      *model.Request
	countedAll   []*model.Request
	summarized   *model.Request
	summaryText  string
	emptySummary bool
	countErr     error
	tokenCounted bool
	inexactCount bool
	// toolTokens charges this many tokens per tool definition present on the
	// counted request, so tests can prove which counts include the catalog.
	toolTokens int
}

func (c *historyCountingClient) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	c.summarized = req
	text := c.summaryText
	if text == "" && !c.emptySummary {
		text = "older summary"
	}
	return &model.Response{
		Content: []model.Message{
			{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: text}},
			},
		},
	}, nil
}

func (c *historyCountingClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, model.ErrStreamingUnsupported
}

func (c *historyCountingClient) CountTokens(_ context.Context, req *model.Request) (model.TokenCount, error) {
	c.tokenCounted = true
	c.counted = req
	c.countedAll = append(c.countedAll, req)
	if c.countErr != nil {
		return model.TokenCount{}, c.countErr
	}
	return model.TokenCount{
		ModelClass:  req.ModelClass,
		InputTokens: len(req.Messages)*10 + len(req.Tools)*c.toolTokens,
		Exact:       !c.inexactCount,
	}, nil
}

func TestCompressPropagatesTokenCountError(t *testing.T) {
	client := &historyCountingClient{countErr: errors.New("count failed")}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtMaxInputTokens: 10,
		KeepMaxTurns:             1,
	})

	out, err := policy(context.Background(), []*model.Message{
		userMsg("question"),
		assistantTextMsg("answer"),
	}, nil)
	require.ErrorContains(t, err, "count failed")
	require.Len(t, out, 2)
}

func TestCompressRequiresTokenCounterForTokenBudgets(t *testing.T) {
	client := struct {
		model.Client
	}{Client: &historyCountingClient{}}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtMaxInputTokens: 10,
		KeepMaxTurns:             1,
	})

	out, err := policy(context.Background(), []*model.Message{
		userMsg("question"),
		assistantTextMsg("answer"),
	}, nil)
	require.ErrorContains(t, err, "history compression token counter is required")
	require.Len(t, out, 2)
}

func TestCompressRequiresExactTokenCounts(t *testing.T) {
	client := &historyCountingClient{inexactCount: true}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtMaxInputTokens: 10,
		KeepMaxTurns:             1,
	})

	out, err := policy(context.Background(), []*model.Message{
		userMsg("question"),
		assistantTextMsg("answer"),
	}, nil)
	require.ErrorContains(t, err, "history compression requires exact token counts")
	require.Len(t, out, 2)
}

func TestCompressRequiresHistoryModel(t *testing.T) {
	policy := Compress(nil, HistoryCompressionConfig{
		CompressAtTurns: 2,
		KeepMaxTurns:    1,
	})
	out, err := policy(context.Background(), []*model.Message{
		userMsg("question"),
		assistantTextMsg("answer"),
	}, nil)
	require.ErrorContains(t, err, "history compression model is required")
	assert.Equal(t, []*model.Message{
		userMsg("question"),
		assistantTextMsg("answer"),
	}, out)
}

func TestCompressRejectsEmptySummary(t *testing.T) {
	client := &historyCountingClient{emptySummary: true}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtTurns: 2,
		KeepMaxTurns:    1,
	})
	msgs := []*model.Message{
		userMsg("question 1"),
		assistantTextMsg("answer 1"),
		userMsg("question 2"),
		assistantTextMsg("answer 2"),
	}

	out, err := policy(context.Background(), msgs, nil)
	require.ErrorContains(t, err, "history compression model returned empty summary")
	assert.Same(t, msgs[0], out[0])
}

func TestCompress_TokenBudgetTriggersAndKeepsWholeRecentTurns(t *testing.T) {
	client := &historyCountingClient{}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtMaxInputTokens: 80,
		KeepMaxInputTokens:       50,
	})
	msgs := []*model.Message{
		systemMsg("system"),
		userMsg("question 1"),
		assistantTextMsg("answer 1"),
		userMsg("question 2"),
		assistantTextMsg("answer 2"),
		userMsg("question 3"),
		assistantToolUseMsg("call-1", "lookup"),
		toolResultMsg("call-1", map[string]any{"next_cursor": "cursor-token"}),
		assistantTextMsg("answer 3"),
	}

	toolDefs := []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Looks up cursor-backed data.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object","properties":{"id":{"type":"string"}}}`)),
		},
	}

	out, err := policy(context.Background(), msgs, toolDefs)
	require.NoError(t, err)

	require.True(t, client.tokenCounted)
	require.NotEmpty(t, client.countedAll)
	assert.Equal(t, model.ModelClassSmall, client.countedAll[0].ModelClass)
	// The compression trigger counts the full provider-visible request
	// including the tool catalog; exact-tail retention counts turn content
	// only (see TestCompress_KeepBudgetExcludesToolCatalog).
	assert.Equal(t, toolDefs, client.countedAll[0].Tools)
	require.NotNil(t, client.summarized)
	require.Len(t, client.summarized.Messages, 1)
	assert.Contains(t, textPart(t, client.summarized.Messages[0]), "question 1")
	assert.Contains(t, textPart(t, client.summarized.Messages[0]), "question 2")
	require.Len(t, out, 6)
	assert.Equal(t, model.ConversationRoleSystem, out[0].Role)
	assert.Equal(t, model.ConversationRoleSystem, out[1].Role)
	assert.Equal(t, "question 3", textPart(t, out[2]))
	assertToolUse(t, out[3], "call-1", "lookup")
	assertToolResult(t, out[4], "call-1", map[string]any{"next_cursor": "cursor-token"})
	assert.Equal(t, "answer 3", textPart(t, out[5]))
}

// TestCompress_KeepBudgetExcludesToolCatalog verifies KeepMaxInputTokens
// budgets the retained turn content only. The advertised tool catalog is fixed
// request overhead that compression can never reclaim; charging it against the
// exact-tail budget would make retention (and the newest-turn fit check)
// depend on catalog size instead of turn size.
func TestCompress_KeepBudgetExcludesToolCatalog(t *testing.T) {
	client := &historyCountingClient{toolTokens: 1000}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtMaxInputTokens: 100,
		KeepMaxInputTokens:       45,
	})
	msgs := []*model.Message{
		systemMsg("system"),
		userMsg("question 1"),
		assistantTextMsg("answer 1"),
		userMsg("question 2"),
		assistantTextMsg("answer 2"),
		userMsg("question 3"),
		assistantTextMsg("answer 3"),
	}
	toolDefs := []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Looks up data.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		},
	}

	out, err := policy(context.Background(), msgs, toolDefs)
	require.NoError(t, err)

	// The compression trigger counts the full provider-visible request,
	// catalog included; the exact-tail retention counts turn content only.
	require.NotEmpty(t, client.countedAll)
	assert.Equal(t, toolDefs, client.countedAll[0].Tools)
	for _, req := range client.countedAll[1:] {
		assert.Empty(t, req.Tools)
	}

	// Turn content alone fits two turns in the 45-token keep budget
	// (2 messages = 20, 4 messages = 40, 6 messages = 60).
	require.Len(t, out, 6)
	assert.Equal(t, model.ConversationRoleSystem, out[0].Role)
	assert.Equal(t, model.ConversationRoleSystem, out[1].Role)
	assert.Contains(t, textPart(t, out[1]), "[Conversation Summary]")
	assert.Equal(t, "question 2", textPart(t, out[2]))
	assert.Equal(t, "answer 2", textPart(t, out[3]))
	assert.Equal(t, "question 3", textPart(t, out[4]))
	assert.Equal(t, "answer 3", textPart(t, out[5]))
}

func systemMsg(text string) *model.Message {
	return &model.Message{
		Role:  model.ConversationRoleSystem,
		Parts: []model.Part{model.TextPart{Text: text}},
	}
}

func userMsg(text string) *model.Message {
	return &model.Message{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: text}},
	}
}

func assistantTextMsg(text string) *model.Message {
	return &model.Message{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: text}},
	}
}

func assistantToolUseMsg(id, name string) *model.Message {
	return &model.Message{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ToolUsePart{
				ID:    id,
				Name:  name,
				Input: map[string]any{"id": "abc"},
			},
		},
	}
}

func toolResultMsg(id string, content any) *model.Message {
	return &model.Message{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{
			model.ToolResultPart{
				ToolUseID: id,
				Content:   content,
			},
		},
	}
}

func textPart(t *testing.T, msg *model.Message) string {
	t.Helper()
	require.Len(t, msg.Parts, 1)
	part, ok := msg.Parts[0].(model.TextPart)
	require.True(t, ok)
	return part.Text
}

func assertToolUse(t *testing.T, msg *model.Message, id, name string) {
	t.Helper()
	require.Len(t, msg.Parts, 1)
	part, ok := msg.Parts[0].(model.ToolUsePart)
	require.True(t, ok)
	assert.Equal(t, id, part.ID)
	assert.Equal(t, name, part.Name)
}

func assertToolResult(t *testing.T, msg *model.Message, id string, content any) {
	t.Helper()
	require.Len(t, msg.Parts, 1)
	part, ok := msg.Parts[0].(model.ToolResultPart)
	require.True(t, ok)
	assert.Equal(t, id, part.ToolUseID)
	assert.Equal(t, content, part.Content)
}
