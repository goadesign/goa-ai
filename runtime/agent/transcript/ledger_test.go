package transcript

import (
	"testing"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestLedger_BuildAndValidate(t *testing.T) {
	l := NewLedger()
	// Structured thinking first
	l.AppendThinking(ThinkingPart{Text: "let me think", Signature: "sig", Index: 0, Final: true})
	// Assistant text
	l.AppendText("calling tool")
	// Declare tool use
	l.DeclareToolUse("tu1", "search_assets", map[string]any{"q": "pump"})
	// Append user tool result
	l.AppendToolResult("tu1", map[string]any{"ok": true}, false)

	msgs := l.BuildMessages()
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != model.ConversationRoleAssistant {
		t.Fatalf("first role = %s, want assistant", msgs[0].Role)
	}
	if len(msgs[0].Parts) < 2 {
		t.Fatalf("assistant parts too short")
	}
	if _, ok := msgs[0].Parts[0].(model.ThinkingPart); !ok {
		t.Fatalf("assistant does not start with thinking")
	}
	if _, ok := msgs[0].Parts[1].(model.TextPart); !ok {
		t.Fatalf("assistant second part should be text")
	}
	if msgs[1].Role != model.ConversationRoleUser {
		t.Fatalf("second role = %s, want user", msgs[1].Role)
	}
	if err := ValidateBedrock(msgs, true); err != nil {
		t.Fatalf("validate failed: %v", err)
	}
}
