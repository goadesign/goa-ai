package reminder

import (
	"testing"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestEngineAddAndSnapshot(t *testing.T) {
	e := NewEngine()
	const runID = "run-1"

	e.AddReminder(runID, Reminder{
		ID:       "r1",
		Text:     "first",
		Priority: TierGuidance,
	})
	e.AddReminder(runID, Reminder{
		ID:       "r2",
		Text:     "second",
		Priority: TierSafety,
	})

	rems := e.Snapshot(runID)
	if len(rems) != 2 {
		t.Fatalf("expected 2 reminders, got %d", len(rems))
	}
	if rems[0].ID != "r2" || rems[1].ID != "r1" {
		t.Errorf("expected safety reminder first, got %q then %q", rems[0].ID, rems[1].ID)
	}
}

func TestEngineRateLimitingAndCaps(t *testing.T) {
	e := NewEngine()
	const runID = "run-2"

	e.AddReminder(runID, Reminder{
		ID:              "limited",
		Text:            "limited",
		Priority:        TierGuidance,
		MaxPerRun:       1,
		MinTurnsBetween: 2,
	})

	// First turn: reminder should emit.
	rems := e.Snapshot(runID)
	if len(rems) != 1 {
		t.Fatalf("expected 1 reminder on first turn, got %d", len(rems))
	}

	// Second turn: rate limit prevents emission.
	rems = e.Snapshot(runID)
	if len(rems) != 0 {
		t.Fatalf("expected 0 reminders on second turn, got %d", len(rems))
	}

	// Further turns should still respect MaxPerRun.
	_ = e.Snapshot(runID)
	rems = e.Snapshot(runID)
	if len(rems) != 0 {
		t.Fatalf("expected 0 reminders after cap, got %d", len(rems))
	}
}

func TestInjectMessages(t *testing.T) {
	msgs := []*model.Message{
		{
			Role: model.ConversationRoleSystem,
			Parts: []model.Part{
				model.TextPart{Text: "preamble"},
			},
		},
		{
			Role: model.ConversationRoleUser,
			Parts: []model.Part{
				model.TextPart{Text: "user question"},
			},
		},
	}
	rems := []Reminder{
		{
			ID:       "r1",
			Text:     "run-start",
			Priority: TierSafety,
			Attachment: Attachment{
				Kind: AttachmentRunStart,
			},
		},
		{
			ID:       "r2",
			Text:     "per-turn",
			Priority: TierGuidance,
			Attachment: Attachment{
				Kind: AttachmentUserTurn,
			},
		},
	}

	out := InjectMessages(msgs, rems)
	if len(out) != 3 {
		t.Fatalf("expected 3 messages after injection, got %d", len(out))
	}
	if out[0].Role != model.ConversationRoleSystem {
		t.Fatalf("expected first message to be system, got %q", out[0].Role)
	}
	if out[1].Role != model.ConversationRoleSystem {
		t.Fatalf("expected second message to be injected system, got %q", out[1].Role)
	}
	if out[2].Role != model.ConversationRoleUser {
		t.Fatalf("expected third message to be user, got %q", out[2].Role)
	}
}



