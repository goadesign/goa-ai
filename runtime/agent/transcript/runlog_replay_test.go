package transcript_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	agentrun "goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestBuildMessagesFromRunLogReplaysCanonicalTranscriptOrder(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTranscriptTestStore(t, ctx)

	appendTranscriptDelta(t, ctx, store, "turn-1", []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "Summarize sales"}},
	}})
	appendTranscriptDelta(t, ctx, store, "turn-1", []*model.Message{{
		Role: model.ConversationRoleAssistant,
		Parts: []model.Part{
			model.ThinkingPart{Text: "Need the sales data first.", Signature: "sig-1", Index: 0, Final: true},
			model.TextPart{Text: "Need the sales data first."},
			model.ToolUsePart{
				ID:    "call_1",
				Name:  "reports.summarize",
				Input: rawjson.Message(`{"query":"sales"}`),
			},
		},
	}})
	appendTranscriptDelta(t, ctx, store, "turn-1", []*model.Message{{
		Role: model.ConversationRoleUser,
		Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call_1",
			Content:   map[string]any{"status": "ok", "rows": 3},
		}},
	}})

	messages, err := transcript.BuildMessagesFromRunLog(ctx, store, "run-1")
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
		Content:   map[string]any{"rows": json.Number("3"), "status": "ok"},
		IsError:   false,
	}, messages[2].Parts[0])
}

func TestBuildMessagesFromRunLogReplaysSeededAndAppendedTranscriptMessages(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTranscriptTestStore(t, ctx)

	appendTranscriptMessages(t, ctx, store, "run-1", "turn-1", transcript.RunLogMessagesSeeded, []*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "hello"}},
	}})
	appendTranscriptMessages(t, ctx, store, "run-1", "turn-1", transcript.RunLogMessagesAppended, []*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "world"}},
	}})

	messages, err := transcript.BuildMessagesFromRunLog(ctx, store, "run-1")
	require.NoError(t, err)
	require.Len(t, messages, 2)
	require.Equal(t, model.ConversationRoleUser, messages[0].Role)
	require.Equal(t, model.ConversationRoleAssistant, messages[1].Role)
}

func TestBuildMessagesFromRunLogAllowsPublishedEmptyHistory(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := newTranscriptTestStore(t, ctx)

	messages, err := transcript.BuildMessagesFromRunLog(ctx, store, "run-1")
	require.NoError(t, err)
	require.Empty(t, messages)
}

func appendTranscriptDelta(t *testing.T, ctx context.Context, store storage.Store, turnID string, messages []*model.Message) {
	t.Helper()
	appendTranscriptMessages(t, ctx, store, "run-1", turnID, transcript.RunLogMessagesAppended, messages)
}

func appendTranscriptMessages(t *testing.T, ctx context.Context, store storage.Store, runID, turnID string, typ runlog.Type, messages []*model.Message) {
	t.Helper()

	payload, err := transcript.EncodeRunLogDelta(messages)
	require.NoError(t, err)

	_, err = store.AppendRunRecord(ctx, &runlog.Event{
		EventKey:  "event-" + turnID + "-" + time.Now().UTC().Format(time.RFC3339Nano),
		RunID:     runID,
		AgentID:   agent.Ident("agent-1"),
		SessionID: "session-1",
		TurnID:    turnID,
		Type:      typ,
		Payload:   payload,
		Timestamp: time.Now().UTC().Truncate(time.Millisecond),
	})
	require.NoError(t, err)
}

// newTranscriptTestStore creates the run whose transcript records the tests
// append directly.
func newTranscriptTestStore(t *testing.T, ctx context.Context) *storageinmem.Store {
	t.Helper()
	store := storageinmem.New()
	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err := store.CreateSession(ctx, "session-1", now)
	require.NoError(t, err)
	_, err = store.BeginRunSeed(ctx, storage.SeedDeclaration{
		AgentID: "agent-1", RunID: "run-1", SessionID: "session-1", CommandID: "run-1", AttemptID: "run-1", Kind: storage.SeedLiteral,
	})
	require.NoError(t, err)
	prepared := []byte(`{}`)
	end, err := store.AppendRunSeed(ctx, storage.SeedAppend{
		RunID: "run-1", AttemptID: "run-1",
		Record: storage.SeedRecord{Key: "prepared", PreviousID: storage.EmptySeedEndID, Prepared: prepared},
	})
	require.NoError(t, err)
	require.NoError(t, store.PublishRunSeed(ctx, storage.SeedPublication{
		RunID: "run-1", AttemptID: "run-1", SeedEndID: storage.EmptySeedEndID, EndID: end, PreparedBytes: int64(len(prepared)),
	}))
	started := transcriptLifecycleRecord(t, hooks.NewRunStartedEvent(
		"run-1", "agent-1", "session-1", "", "", nil,
	), "run-started", now)
	canceled := transcriptLifecycleRecord(t, hooks.NewRunCompletedEvent(
		"run-1",
		"agent-1",
		"session-1",
		"canceled",
		agentrun.PhaseCanceled,
		nil,
		context.Canceled,
		&agentrun.Cancellation{Reason: agentrun.CancellationReasonSessionEnded},
	), "run-canceled", now)
	_, err = store.StartRootRun(ctx, storage.RootRunStart{RequestDigest: [32]byte{1},
		Run:      session.RunStart{AgentID: "agent-1", RunID: "run-1", SessionID: "session-1", SeedEndID: storage.EmptySeedEndID, StartedAt: now},
		Started:  started,
		Canceled: canceled,
	})
	require.NoError(t, err)
	return store
}

// transcriptLifecycleRecord encodes a typed lifecycle event for transcript
// store setup.
func transcriptLifecycleRecord(t *testing.T, event hooks.Event, key string, at time.Time) *runlog.Event {
	t.Helper()
	input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{EventKey: key, TimestampMS: at.UnixMilli()})
	require.NoError(t, err)
	return &runlog.Event{
		EventKey:  input.EventKey,
		RunID:     input.RunID,
		AgentID:   input.AgentID,
		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   input.Payload,
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	}
}
