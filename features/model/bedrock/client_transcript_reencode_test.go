package bedrock

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

// Ensures encodeMessages preserves transcript order and places reasoning before tool_use
// inside an assistant message, and encodes user tool_result referencing the prior ID.
func TestEncodeMessages_ReencodeTranscriptOrder(t *testing.T) {
	ctx := context.Background()
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ThinkingPart{Text: "thinking", Signature: "sig"},
				model.ToolUsePart{ID: "tu1", Name: "search_assets", Input: map[string]any{"q": "pump"}},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{ToolUseID: "tu1", Content: map[string]any{"ok": true}},
			},
		},
	}
	// Provide the canonical → sanitized name map for tools referenced in messages.
	nameMap := map[string]string{
		"search_assets": "search_assets",
	}
	conv, system, err := encodeMessages(ctx, msgs, nameMap, false, nil)
	if err != nil {
		t.Fatalf("encodeMessages error: %v", err)
	}
	if len(conv) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(conv))
	}
	if len(system) != 0 {
		t.Fatalf("expected no system blocks, got %d", len(system))
	}
	// Assistant message must start with reasoning content before tool_use.
	asst := conv[0]
	if asst.Role != brtypes.ConversationRoleAssistant {
		t.Fatalf("first role = %s, want assistant", asst.Role)
	}
	if len(asst.Content) < 2 {
		t.Fatalf("assistant content length = %d, want >= 2", len(asst.Content))
	}
	if _, ok := asst.Content[0].(*brtypes.ContentBlockMemberReasoningContent); !ok {
		t.Fatalf("assistant first block is not reasoning content")
	}
	if _, ok := asst.Content[1].(*brtypes.ContentBlockMemberToolUse); !ok {
		t.Fatalf("assistant second block is not tool_use")
	}
	// User message must contain tool_result referencing tu1.
	user := conv[1]
	if user.Role != brtypes.ConversationRoleUser {
		t.Fatalf("second role = %s, want user", user.Role)
	}
	if len(user.Content) == 0 {
		t.Fatalf("user content is empty")
	}
	trb, ok := user.Content[0].(*brtypes.ContentBlockMemberToolResult)
	if !ok || trb == nil || trb.Value.ToolUseId == nil || *trb.Value.ToolUseId != "tu1" {
		t.Fatalf("user tool_result does not reference tu1")
	}
}

func TestClientPrepareRequestLowersRunlogReplayedTranscript(t *testing.T) {
	messages := replayedBedrockToolLoopMessages(t)
	client := &Client{
		defaultModel: "test-model",
		maxTok:       32,
		temp:         0.0,
		think:        defaultThinkingBudget,
	}

	parts, err := client.prepareRequest(context.Background(), &model.Request{
		Messages: messages,
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			InputSchema: map[string]any{"type": "object"},
		}},
	})
	require.NoError(t, err)
	require.Len(t, parts.messages, 3)

	require.Equal(t, brtypes.ConversationRoleUser, parts.messages[0].Role)
	require.Equal(t, brtypes.ConversationRoleAssistant, parts.messages[1].Role)
	require.Equal(t, brtypes.ConversationRoleUser, parts.messages[2].Role)

	var toolUse *brtypes.ContentBlockMemberToolUse
	for _, block := range parts.messages[1].Content {
		if v, ok := block.(*brtypes.ContentBlockMemberToolUse); ok {
			toolUse = v
			break
		}
	}
	require.NotNil(t, toolUse)
	require.NotNil(t, toolUse.Value.ToolUseId)
	require.Equal(t, "call_1", *toolUse.Value.ToolUseId)

	toolResult, ok := parts.messages[2].Content[0].(*brtypes.ContentBlockMemberToolResult)
	require.True(t, ok)
	require.NotNil(t, toolResult.Value.ToolUseId)
	require.Equal(t, "call_1", *toolResult.Value.ToolUseId)
}

func TestClientPrepareRequestFailsOnMissingThinkingInToolLoop(t *testing.T) {
	client := &Client{
		defaultModel: "anthropic.claude-3-7-sonnet",
		maxTok:       32,
		temp:         0.0,
		think:        defaultThinkingBudget,
	}

	_, err := client.prepareRequest(context.Background(), &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: "Need the sales data first."},
					model.ToolUsePart{
						ID:    "call_1",
						Name:  "analytics.analyze",
						Input: map[string]any{"query": "sales"},
					},
				},
			},
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.ToolResultPart{
						ToolUseID: "call_1",
						Content:   map[string]any{"status": "ok"},
					},
				},
			},
		},
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			InputSchema: map[string]any{"type": "object"},
		}},
		Thinking: &model.ThinkingOptions{
			Enable: true,
		},
	})
	require.ErrorContains(t, err, "must start with thinking")
}

// Ensures encodeMessages fails fast when a tool_use references a tool that is not in the
// current tool configuration. This catches transcript contamination (e.g., ledger key
// collision between agent runs) or missing tool definitions.
func TestEncodeMessages_FailsOnUnknownToolUse(t *testing.T) {
	ctx := context.Background()
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "tu1",
					Name:  "ada.unknown_tool",
					Input: map[string]any{"arg": "value"},
				},
			},
		},
	}
	// Provide a nameMap that does NOT include the tool referenced in messages.
	nameMap := map[string]string{
		"atlas.read.some_other_tool": "some_other_tool",
	}
	_, _, err := encodeMessages(ctx, msgs, nameMap, false, nil)
	if err == nil {
		t.Fatal("expected error for unknown tool_use, got nil")
	}
	if !strings.Contains(err.Error(), "ada.unknown_tool") {
		t.Errorf("error should mention the unknown tool name, got: %v", err)
	}
	if !strings.Contains(err.Error(), "not in the current tool configuration") {
		t.Errorf("error should mention tool configuration mismatch, got: %v", err)
	}
}

func replayedBedrockToolLoopMessages(t *testing.T) []*model.Message {
	t.Helper()

	ctx := context.Background()
	store := runloginmem.New()
	appendBedrockReplayDelta(t, ctx, store, []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "Summarize sales"}},
	}})
	appendBedrockReplayDelta(t, ctx, store, []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ThinkingPart{Text: "Need the sales data first.", Signature: "sig-1", Index: 0, Final: true},
			model.TextPart{Text: "Need the sales data first."},
			model.ToolUsePart{
				ID:    "call_1",
				Name:  "analytics.analyze",
				Input: map[string]any{"query": "sales"},
			},
		},
	}})
	appendBedrockReplayDelta(t, ctx, store, []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call_1",
			Content:   map[string]any{"status": "ok", "rows": 3},
		}},
	}})

	messages, err := transcript.BuildMessagesFromRunLog(ctx, store, "run-1")
	require.NoError(t, err)
	return messages
}

func appendBedrockReplayDelta(t *testing.T, ctx context.Context, store runlog.Store, messages []*model.Message) {
	t.Helper()

	payload, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)

	_, err = store.Append(ctx, &runlog.Event{
		EventKey:  time.Now().UTC().Format(time.RFC3339Nano),
		RunID:     "run-1",
		AgentID:   agent.Ident("agent-1"),
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      transcript.RunLogMessagesAppended,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, err)
}

func TestEncodeMessages_RewritesUnknownToolUseToToolUnavailable(t *testing.T) {
	ctx := context.Background()
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "tu1",
					Name:  "atlas_read_count_events",
					Input: map[string]any{"from": "2026-02-06T00:00:00Z"},
				},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.ToolResultPart{
					ToolUseID: "tu1",
					Content:   map[string]any{"error": "unknown tool"},
					IsError:   true,
				},
			},
		},
	}
	nameMap := map[string]string{
		tools.ToolUnavailable.String(): SanitizeToolName(tools.ToolUnavailable.String()),
	}
	conv, _, err := encodeMessages(ctx, msgs, nameMap, false, nil)
	if err != nil {
		t.Fatalf("encodeMessages error: %v", err)
	}
	if len(conv) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(conv))
	}
	asst := conv[0]
	if asst.Role != brtypes.ConversationRoleAssistant {
		t.Fatalf("first role = %s, want assistant", asst.Role)
	}
	var toolUse *brtypes.ContentBlockMemberToolUse
	for _, b := range asst.Content {
		if v, ok := b.(*brtypes.ContentBlockMemberToolUse); ok {
			toolUse = v
			break
		}
	}
	if toolUse == nil || toolUse.Value.Name == nil {
		t.Fatalf("missing tool_use block name")
	}
	wantName := SanitizeToolName(tools.ToolUnavailable.String())
	if got := *toolUse.Value.Name; got != wantName {
		t.Fatalf("tool_use name = %q, want %q", got, wantName)
	}
	if toolUse.Value.Input == nil {
		t.Fatalf("missing tool_use input")
	}
	raw, err := toolUse.Value.Input.MarshalSmithyDocument()
	if err != nil {
		t.Fatalf("marshal tool_use input: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode tool_use input: %v", err)
	}
	if got := decoded["requested_tool"]; got != "atlas_read_count_events" {
		t.Fatalf("requested_tool = %#v, want %q", got, "atlas_read_count_events")
	}
}

func TestEncodeMessages_AppendsSystemCacheCheckpoint(t *testing.T) {
	ctx := context.Background()
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleSystem,
			Parts: []model.Part{
				model.TextPart{Text: "you are a helpful assistant"},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.TextPart{Text: "hello"},
			},
		},
	}
	conv, system, err := encodeMessages(ctx, msgs, map[string]string{}, true, nil)
	if err != nil {
		t.Fatalf("encodeMessages error: %v", err)
	}
	if len(conv) != 1 {
		t.Fatalf("expected 1 non-system message, got %d", len(conv))
	}
	if len(system) != 2 {
		t.Fatalf("expected 2 system blocks (text + cache point), got %d", len(system))
	}
	if _, ok := system[0].(*brtypes.SystemContentBlockMemberText); !ok {
		t.Fatalf("first system block is not text")
	}
	if _, ok := system[1].(*brtypes.SystemContentBlockMemberCachePoint); !ok {
		t.Fatalf("second system block is not cache checkpoint")
	}
}
