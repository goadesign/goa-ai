package transcript

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
)

func TestBuildMessagesFromRunLogReplaysCanonicalTranscriptOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := runloginmem.New()

	appendTranscriptDelta(t, ctx, store, "run-1", "turn-1", []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "Summarize sales"}},
	}})
	appendTranscriptDelta(t, ctx, store, "run-1", "turn-1", []*model.Message{{
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
	appendTranscriptDelta(t, ctx, store, "run-1", "turn-1", []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call_1",
			Content:   map[string]any{"status": "ok", "rows": 3},
		}},
	}})

	messages, err := BuildMessagesFromRunLog(ctx, store, "run-1")
	require.NoError(t, err)
	require.Len(t, messages, 3)

	require.Equal(t, model.ConversationRoleUser, messages[0].Role)
	require.Equal(t, []model.Part{model.TextPart{Text: "Summarize sales"}}, messages[0].Parts)

	require.Equal(t, model.ConversationRoleAssistant, messages[1].Role)
	require.Len(t, messages[1].Parts, 3)
	require.IsType(t, model.ThinkingPart{}, messages[1].Parts[0])
	require.IsType(t, model.TextPart{}, messages[1].Parts[1])
	require.IsType(t, model.ToolUsePart{}, messages[1].Parts[2])

	require.Equal(t, model.ConversationRoleUser, messages[2].Role)
	require.Len(t, messages[2].Parts, 1)
	require.Equal(t, model.ToolResultPart{
		ToolUseID: "call_1",
		Content:   map[string]any{"rows": float64(3), "status": "ok"},
		IsError:   false,
	}, messages[2].Parts[0])
}

func TestBuildMessagesFromRunLogReplaysSeededAndAppendedTranscriptMessages(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := runloginmem.New()

	appendTranscriptMessages(t, ctx, store, "run-1", "turn-1", RunLogMessagesSeeded, []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "hello"}},
	}})
	appendTranscriptMessages(t, ctx, store, "run-1", "turn-1", RunLogMessagesAppended, []*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "world"}},
	}})

	messages, err := BuildMessagesFromRunLog(ctx, store, "run-1")
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, model.ConversationRoleUser, messages[0].Role)
	require.Equal(t, model.ConversationRoleAssistant, messages[1].Role)
}

func TestBuildMessagesFromRunLogRequiresTranscriptDeltaEvents(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := runloginmem.New()
	_, err := store.Append(ctx, &runlog.Event{
		EventKey:  "run_started",
		RunID:     "run-1",
		AgentID:   agent.Ident("agent-1"),
		SessionID: "session-1",
		TurnID:    "turn-1",
		Type:      hooks.RunStarted,
		Payload:   []byte(`{}`),
		Timestamp: time.Unix(0, 0).UTC(),
	})
	require.NoError(t, err)

	_, err = BuildMessagesFromRunLog(ctx, store, "run-1")
	require.ErrorContains(t, err, "has no transcript message events")
}

func appendTranscriptDelta(t *testing.T, ctx context.Context, store runlog.Store, runID, turnID string, messages []*model.Message) {
	t.Helper()
	appendTranscriptMessages(t, ctx, store, runID, turnID, RunLogMessagesAppended, messages)
}

func appendTranscriptMessages(t *testing.T, ctx context.Context, store runlog.Store, runID, turnID string, typ runlog.Type, messages []*model.Message) {
	t.Helper()

	payload, err := EncodeRunLogDelta(messages)
	require.NoError(t, err)

	_, err = store.Append(ctx, &runlog.Event{
		EventKey:  "event-" + turnID + "-" + time.Now().UTC().Format(time.RFC3339Nano),
		RunID:     runID,
		AgentID:   agent.Ident("agent-1"),
		SessionID: "session-1",
		TurnID:    turnID,
		Type:      typ,
		Payload:   payload,
		Timestamp: time.Now().UTC(),
	})
	require.NoError(t, err)
}
