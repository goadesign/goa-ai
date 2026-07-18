package bedrock

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"
	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/features/model/toolname"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestEncodeMessagesRejectsNonCanonicalThinking(t *testing.T) {
	tests := []struct {
		name    string
		part    model.ThinkingPart
		wantErr string
	}{
		{
			name: "signed plaintext",
			part: model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
		},
		{
			name: "redacted",
			part: model.ThinkingPart{Redacted: []byte("opaque"), Final: true},
		},
		{
			name:    "missing signature",
			part:    model.ThinkingPart{Text: "reasoning", Final: true},
			wantErr: "bedrock: thinking part must contain exactly signed plaintext or redacted content",
		},
		{
			name: "mixed variants",
			part: model.ThinkingPart{
				Text:      "reasoning",
				Signature: "sig",
				Redacted:  []byte("opaque"),
				Final:     true,
			},
			wantErr: "bedrock: thinking part must contain exactly signed plaintext or redacted content",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := encodeMessages(
				[]*model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{test.part},
				}},
				nil,
				false,
			)

			if test.wantErr != "" {
				require.EqualError(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestTranslateResponsePreservesReasoning(t *testing.T) {
	output := &bedrockruntime.ConverseOutput{
		StopReason: brtypes.StopReasonEndTurn,
		Output: &brtypes.ConverseOutputMemberMessage{Value: brtypes.Message{
			Role: brtypes.ConversationRoleAssistant,
			Content: []brtypes.ContentBlock{
				&brtypes.ContentBlockMemberReasoningContent{
					Value: &brtypes.ReasoningContentBlockMemberReasoningText{
						Value: brtypes.ReasoningTextBlock{
							Text:      aws.String("reasoning"),
							Signature: aws.String("sig"),
						},
					},
				},
				&brtypes.ContentBlockMemberText{Value: "answer"},
			},
		}},
	}

	resp, err := translateResponse(output, nil, "", "")

	require.NoError(t, err)
	require.Equal(t, []model.Part{
		model.ThinkingPart{Text: "reasoning", Signature: "sig", Final: true},
		model.TextPart{Text: "answer"},
	}, resp.Content[0].Parts)
}

// Ensures encodeMessages preserves transcript order and places reasoning before tool_use
// inside an assistant message, and encodes user tool_result referencing the prior ID.
func TestEncodeMessages_ReencodeTranscriptOrder(t *testing.T) {
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ThinkingPart{Text: "thinking", Signature: "sig"},
				model.ToolUsePart{ID: "tu1", Name: "search_assets", Input: rawjson.Message(`{"q":"pump"}`)},
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
	conv, system, err := encodeMessages(msgs, nameMap, false)
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

	parts, err := client.prepareRequest(&model.Request{
		Messages: messages,
		Tools: []*model.ToolDefinition{{
			Name:        "analytics.analyze",
			Description: "Run an analysis.",
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
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

func TestEncodeMessagesToolUseIDMappingIsBijective(t *testing.T) {
	messages := []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ToolUsePart{ID: "bad/id", Name: "analytics.analyze", Input: rawjson.Message(`{}`)},
			model.ToolUsePart{ID: "t1", Name: "analytics.analyze", Input: rawjson.Message(`{}`)},
		},
	}}

	encoded, _, err := encodeMessages(messages, map[string]string{"analytics.analyze": "analytics_analyze"}, false)
	require.NoError(t, err)
	require.Len(t, encoded, 1)
	require.Len(t, encoded[0].Content, 2)
	first := encoded[0].Content[0].(*brtypes.ContentBlockMemberToolUse)
	second := encoded[0].Content[1].(*brtypes.ContentBlockMemberToolUse)
	require.Equal(t, "t2", aws.ToString(first.Value.ToolUseId))
	require.Equal(t, "t1", aws.ToString(second.Value.ToolUseId))
}

func TestClientPrepareRequestFailsOnMissingThinkingInToolLoop(t *testing.T) {
	client := &Client{
		defaultModel: "anthropic.claude-3-7-sonnet",
		maxTok:       32,
		temp:         0.0,
		think:        defaultThinkingBudget,
	}

	_, err := client.prepareRequest(&model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleAssistant,
				Parts: []model.Part{
					model.TextPart{Text: "Need the sales data first."},
					model.ToolUsePart{
						ID:    "call_1",
						Name:  "analytics.analyze",
						Input: rawjson.Message(`{"query":"sales"}`),
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
			Input:       model.ToolInputFromSchema(rawjson.Message(`{"type":"object"}`)),
		}},
		Thinking: &model.ThinkingOptions{
			Enable: true,
		},
	})
	require.ErrorContains(t, err, "must start with thinking")
}

func TestEncodeMessagesReplaysHistoricalToolUseUnchanged(t *testing.T) {
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "tu1",
					Name:  "ada.unknown_tool",
					Input: rawjson.Message(`{"arg":"value"}`),
				},
			},
		},
	}
	nameMap := map[string]string{
		"atlas.read.some_other_tool": "some_other_tool",
	}
	conv, _, err := encodeMessages(msgs, nameMap, false)
	require.NoError(t, err)
	require.Len(t, conv, 1)
	use := conv[0].Content[0].(*brtypes.ContentBlockMemberToolUse)
	require.Equal(t, "ada.unknown_tool", aws.ToString(use.Value.Name))
	raw, err := use.Value.Input.MarshalSmithyDocument()
	require.NoError(t, err)
	require.JSONEq(t, `{"arg":"value"}`, string(raw))
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
				Input: rawjson.Message(`{"query":"sales"}`),
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

func TestEncodeMessagesDoesNotRewriteHistoricalToolUseToToolUnavailable(t *testing.T) {
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleAssistant,
			Parts: []model.Part{
				model.ToolUsePart{
					ID:    "tu1",
					Name:  "atlas_read_count_events",
					Input: rawjson.Message(`{"from":"2026-02-06T00:00:00Z"}`),
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
		tools.ToolUnavailable.String(): toolname.Sanitize(tools.ToolUnavailable.String()),
	}
	conv, _, err := encodeMessages(msgs, nameMap, false)
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
	wantName := "atlas_read_count_events"
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
	if got := decoded["from"]; got != "2026-02-06T00:00:00Z" {
		t.Fatalf("from = %#v, want %q", got, "2026-02-06T00:00:00Z")
	}
}

func TestEncodeMessages_AppendsSystemCacheCheckpoint(t *testing.T) {
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
	conv, system, err := encodeMessages(msgs, map[string]string{}, true)
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
