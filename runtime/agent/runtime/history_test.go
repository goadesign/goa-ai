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
		// The keep budget is anchored on the newest tail: adding turn 2 to the
		// newest turn costs 20 tokens, which exceeds this budget, so only the
		// newest turn stays exact.
		KeepMaxInputTokens: 10,
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
// budgets retained history relative to the newest tail, so the advertised tool
// catalog (and the system prompt) cancel out of the comparison. The catalog is
// fixed request overhead that compression can never reclaim; charging it
// against the exact-tail budget would make retention depend on catalog size
// instead of turn size. Every count must be a full planner-request shape
// (system + turns + tools): providers such as Bedrock reject token counting
// for tool-bearing transcripts without the tool config.
func TestCompress_KeepBudgetExcludesToolCatalog(t *testing.T) {
	client := &historyCountingClient{toolTokens: 1000}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtMaxInputTokens: 1050,
		KeepMaxInputTokens:       25,
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

	// Every counting request is a full planner-request shape: catalog
	// attached and system prompt included.
	require.NotEmpty(t, client.countedAll)
	for _, req := range client.countedAll {
		assert.Equal(t, toolDefs, req.Tools)
		require.NotEmpty(t, req.Messages)
		assert.Equal(t, model.ConversationRoleSystem, req.Messages[0].Role)
	}

	// The 1000-token catalog cancels out of the anchored comparison: the
	// newest tail counts 1030 (system + 2 messages + catalog), adding turn 2
	// costs 20 more (fits the 25 budget), adding turn 1 costs 40 more
	// (exceeds it). Turns 2 and 3 stay exact; turn 1 is summarized.
	require.Len(t, out, 6)
	assert.Equal(t, model.ConversationRoleSystem, out[0].Role)
	assert.Equal(t, model.ConversationRoleSystem, out[1].Role)
	assert.Contains(t, textPart(t, out[1]), "[Conversation Summary]")
	assert.Equal(t, "question 2", textPart(t, out[2]))
	assert.Equal(t, "answer 2", textPart(t, out[3]))
	assert.Equal(t, "question 3", textPart(t, out[4]))
	assert.Equal(t, "answer 3", textPart(t, out[5]))
}

// TestCompress_ErrsWhenNewestTurnCannotFitCompressTrigger verifies the run
// fails loudly when even maximal compression — keeping only the newest turn —
// cannot produce a planner request under CompressAtMaxInputTokens. This is the
// truly doomed request; anything larger than the compress trigger would just
// re-trigger compression forever.
func TestCompress_ErrsWhenNewestTurnCannotFitCompressTrigger(t *testing.T) {
	client := &historyCountingClient{toolTokens: 1000}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtMaxInputTokens: 1025,
		KeepMaxInputTokens:       25,
	})
	msgs := []*model.Message{
		systemMsg("system"),
		userMsg("question 1"),
		assistantTextMsg("answer 1"),
		userMsg("question 2"),
		assistantTextMsg("answer 2"),
	}
	toolDefs := []*model.ToolDefinition{
		{
			Name:        "lookup",
			Description: "Looks up data.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		},
	}

	// Total counts 1050 (5 messages + catalog) which trips the 1025 trigger,
	// and the newest tail alone counts 1030 which still exceeds it.
	out, err := policy(context.Background(), msgs, toolDefs)
	require.ErrorContains(t, err, "newest history turn cannot fit within CompressAtMaxInputTokens (1030 > 1025)")
	require.Len(t, out, 5)
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
				Input: rawjson.Message(`{"id":"abc"}`),
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

// TestCompressCountsToolBearingHistoryWithUnavailableDefinition pins the
// counting contract for transcripts that carry unknown-tool recovery: every
// token-count request synthesized from history must include the runtime-owned
// tool_unavailable definition when historical tool_use names are absent from
// the advertised tool list, mirroring the guarantee the configured model
// client provides for Complete and Stream. Providers such as Bedrock reject
// tool-bearing transcripts whose tool_use names are missing from the request
// tool configuration.
func TestCompressCountsToolBearingHistoryWithUnavailableDefinition(t *testing.T) {
	client := &historyCountingClient{}
	policy := Compress(client, HistoryCompressionConfig{
		CompressAtMaxInputTokens: 30,
		KeepMaxInputTokens:       40,
	})

	_, err := policy(context.Background(), []*model.Message{
		userMsg("question"),
		assistantToolUseMsg("t1", "runtime.tool_unavailable"),
		toolResultMsg("t1", "unavailable"),
		assistantTextMsg("answer"),
		userMsg("follow-up"),
		assistantTextMsg("done"),
	}, []*model.ToolDefinition{{Name: "known.tool"}})
	require.NoError(t, err)
	require.NotEmpty(t, client.countedAll)
	for _, req := range client.countedAll {
		names := make(map[string]bool, len(req.Tools))
		for _, def := range req.Tools {
			names[def.Name] = true
		}
		referencesUnavailable := false
		for _, msg := range req.Messages {
			for _, part := range msg.Parts {
				if use, ok := part.(model.ToolUsePart); ok && use.Name == "runtime.tool_unavailable" {
					referencesUnavailable = true
				}
			}
		}
		if referencesUnavailable {
			require.True(t, names["runtime.tool_unavailable"],
				"count request with recovered tool_use history must carry the tool_unavailable definition, got tools %v", names)
		}
	}
}
