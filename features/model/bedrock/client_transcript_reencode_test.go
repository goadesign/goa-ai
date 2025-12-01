package bedrock

import (
	"context"
	"strings"
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"goa.design/goa-ai/runtime/agent/model"
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
	conv, system, err := encodeMessages(ctx, msgs, nameMap, false)
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
	_, _, err := encodeMessages(ctx, msgs, nameMap, false)
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
	conv, system, err := encodeMessages(ctx, msgs, map[string]string{}, true)
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
