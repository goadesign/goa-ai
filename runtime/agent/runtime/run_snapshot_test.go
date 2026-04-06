package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	runloginmem "goa.design/goa-ai/runtime/agent/runlog/inmem"
	"goa.design/goa-ai/runtime/agent/toolerrors"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/agent/transcript"
)

func TestNewRunSnapshotDerivesToolStateAndCompletion(t *testing.T) {
	t.Parallel()

	var (
		runID     = "run-1"
		agentID   = agent.Ident("svc.agent")
		sessionID = "sess-1"
		turnID    = "turn-1"
	)

	mk := func(at time.Time, evt hooks.Event) *runlog.Event {
		in, err := hooks.EncodeToRecordInput(evt, hooks.EncodeOptions{
			TurnID:      turnID,
			EventKey:    fmt.Sprintf("evt-%d", at.Unix()),
			TimestampMS: at.UnixMilli(),
		})
		require.NoError(t, err)
		return &runlog.Event{
			EventKey:  in.EventKey,
			RunID:     runID,
			AgentID:   agentID,
			SessionID: sessionID,
			TurnID:    turnID,
			Type:      in.Type,
			Payload:   in.Payload,
			Timestamp: at.UTC(),
		}
	}

	t0 := time.Unix(10, 0).UTC()
	t1 := time.Unix(11, 0).UTC()
	t2 := time.Unix(12, 0).UTC()
	t3 := time.Unix(13, 0).UTC()

	events := []*runlog.Event{
		mk(t0, hooks.NewRunPhaseChangedEvent(runID, agentID, sessionID, run.PhasePlanning)),
		mk(t1, hooks.NewToolCallScheduledEvent(runID, agentID, sessionID, tools.Ident("svc.tools.search"), "call-1", []byte(`{"q":"x"}`), "q", "", 0)),
		mk(t2, hooks.NewToolResultReceivedEvent(runID, agentID, sessionID, tools.Ident("svc.tools.search"), "call-1", "", nil, 0, false, "", nil, "", nil, 250*time.Millisecond, nil, nil, toolerrors.New("boom"))),
		mk(t3, hooks.NewRunCompletedEvent(runID, agentID, sessionID, "failed", run.PhaseFailed, errors.New("run failed"), nil)),
	}

	snap, err := newRunSnapshot(events)
	require.NoError(t, err)

	require.Equal(t, runID, snap.RunID)
	require.Equal(t, sessionID, snap.SessionID)
	require.Equal(t, turnID, snap.TurnID)
	require.Equal(t, run.StatusFailed, snap.Status)
	require.Equal(t, run.PhaseFailed, snap.Phase)
	require.Len(t, snap.ToolCalls, 1)
	require.Equal(t, "call-1", snap.ToolCalls[0].ToolCallID)
	require.Equal(t, "boom", snap.ToolCalls[0].ErrorSummary)
	require.Equal(t, 250*time.Millisecond, snap.ToolCalls[0].Duration)
}

func TestGetRunSnapshotReadsThroughStore(t *testing.T) {
	t.Parallel()

	rl := runloginmem.New()
	_, err := rl.Append(context.Background(), &runlog.Event{
		EventKey:  "evt-1",
		RunID:     "run-1",
		AgentID:   agent.Ident("svc.agent"),
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Type:      hooks.RunPhaseChanged,
		Payload:   []byte(`{"phase":"planning"}`),
		Timestamp: time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)

	rt := &Runtime{
		RunEventStore: rl,
	}

	_, err = rt.GetRunSnapshot(context.Background(), "run-1")
	require.NoError(t, err)
}

func TestNewRunSnapshotIncludesCanonicalTranscript(t *testing.T) {
	t.Parallel()

	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "hello"}},
	}})
	require.NoError(t, err)

	snap, err := newRunSnapshot([]*runlog.Event{{
		EventKey:  "evt-transcript",
		RunID:     "run-1",
		AgentID:   agent.Ident("svc.agent"),
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Type:      transcript.RunLogMessagesAppended,
		Payload:   payload,
		Timestamp: time.Unix(10, 0).UTC(),
	}})
	require.NoError(t, err)
	require.Len(t, snap.Transcript, 1)
	require.Equal(t, model.ConversationRoleUser, snap.Transcript[0].Role)
	require.Equal(t, []model.Part{model.TextPart{Text: "hello"}}, snap.Transcript[0].Parts)
}

func TestNewRunSnapshotUsesTranscriptAssistantMessage(t *testing.T) {
	t.Parallel()

	payload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "final hello"}},
	}})
	require.NoError(t, err)

	snap, err := newRunSnapshot([]*runlog.Event{{
		EventKey:  "evt-transcript-assistant",
		RunID:     "run-1",
		AgentID:   agent.Ident("svc.agent"),
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Type:      transcript.RunLogMessagesAppended,
		Payload:   payload,
		Timestamp: time.Unix(20, 0).UTC(),
	}})
	require.NoError(t, err)
	require.Equal(t, "final hello", snap.LastAssistantMessage)
}
