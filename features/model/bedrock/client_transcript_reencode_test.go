package bedrock

import (
	"context"
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
	conv, _, err := encodeMessages(ctx, msgs, nil)
	if err != nil {
		t.Fatalf("encodeMessages error: %v", err)
	}
	if len(conv) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(conv))
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
