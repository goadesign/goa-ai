// These tests verify that canonical run-log replay produces compact run
// snapshots and rejects malformed store results.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
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
			EventKey: in.EventKey,
			RunID:    runID,
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent(agentID))},

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
	t4 := time.Unix(14, 0).UTC()

	labels := map[string]string{"household_id": "house-42"}
	events := []*runlog.Event{
		mk(t0, hooks.NewRunStartedEvent(runID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))}, sessionID, "", "", labels)),
		mk(t1, hooks.NewRunPhaseChangedEvent(runID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))}, sessionID, run.PhasePlanning)),
		mk(t2, hooks.NewToolCallScheduledEvent(runID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))}, sessionID, tools.Ident("svc.tools.search"), "call-1", []byte(`{"q":"x"}`), "q", "", 0)),
		mk(t3, hooks.NewToolResultReceivedEvent(runID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))}, sessionID, runID, tools.Ident("svc.tools.search"), "call-1", "", nil, nil, nil, "", nil, 250*time.Millisecond, nil, testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "boom"))),
		mk(t4, hooks.NewRunCompletedEvent(runID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agentID))}, sessionID, "failed", run.PhaseFailed, labels, errors.New("run failed"), nil)),
	}

	snap, err := newRunSnapshot(events, "", nil)
	require.NoError(t, err)

	require.Equal(t, runID, snap.RunID)
	require.Equal(t, sessionID, snap.SessionID)
	require.Equal(t, turnID, snap.TurnID)
	require.Equal(t, labels, snap.Labels)
	require.Equal(t, run.StatusFailed, snap.Status)
	require.Equal(t, run.PhaseFailed, snap.Phase)
	require.Len(t, snap.ToolCalls, 1)
	require.Equal(t, "call-1", snap.ToolCalls[0].ToolCallID)
	require.Equal(t, "boom", snap.ToolCalls[0].ErrorSummary)
	require.Equal(t, 250*time.Millisecond, snap.ToolCalls[0].Duration)
}

func TestGetRunSnapshotReadsThroughStore(t *testing.T) {
	t.Parallel()

	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent("svc.agent"))}, RunID: "run-1", SessionID: "sess-1", Status: session.RunStatusRunning,
	})
	_, err := store.AppendRunRecord(context.Background(), &runlog.Event{
		EventKey: "evt-1",
		RunID:    "run-1",
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent(agent.Ident("svc.agent")))},

		SessionID: "sess-1",
		TurnID:    "turn-1",
		Type:      hooks.RunPhaseChanged,
		Payload:   []byte(`{"phase":"planning"}`),
		Timestamp: time.Unix(1, 0).UTC(),
	})
	require.NoError(t, err)

	rt := &Runtime{Store: store}

	_, err = rt.GetRunSnapshot(context.Background(), "run-1")
	require.NoError(t, err)
}

func TestNewRunSnapshotRejectsNilStoredEvent(t *testing.T) {
	t.Parallel()

	_, err := newRunSnapshot([]*runlog.Event{nil}, "", nil)

	require.EqualError(t, err, "snapshot run log contains nil event at index 0")
}

func TestNewRunSnapshotRejectsInvalidRunCompletedHookPayload(t *testing.T) {
	t.Parallel()

	_, err := newRunSnapshot([]*runlog.Event{{
		ID:        "event-1",
		EventKey:  "event-1",
		RunID:     "run-1",
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agent.Ident("svc.agent")))},
		SessionID: "sess-1",
		Type:      hooks.RunCompleted,
		Payload:   []byte(`{"status":"failed","phase":"failed"}`),
		Timestamp: time.Unix(1, 0).UTC(),
	}}, "", nil)

	require.ErrorContains(t, err, "failed run completion requires failure payload")
}

func TestNewRunSnapshotEnforcesLocalToolOrder(t *testing.T) {
	t.Parallel()

	const (
		runID    = "run-1"
		session  = "session-1"
		callID   = "call-1"
		parentID = "parent-1"
	)
	toolName := tools.Ident("svc.tools.search")
	schedule := func(name tools.Ident, parent string) hooks.Event {
		return hooks.NewToolCallScheduledEvent(
			runID,
			genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))},
			session,
			name,
			callID,
			[]byte(`{"q":"x"}`),
			"",
			parent,
			0,
		)
	}
	result := func(name tools.Ident, parent string) hooks.Event {
		return hooks.NewToolResultReceivedEvent(
			runID,
			genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))},
			session,
			runID,
			name,
			callID,
			parent,
			nil,
			nil,
			nil, "",
			nil,
			time.Second,
			nil,
			testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "failed"),
		)
	}
	tests := []struct {
		name    string
		events  []hooks.Event
		wantErr string
	}{
		{
			name:    "duplicate schedule",
			events:  []hooks.Event{schedule(toolName, ""), schedule(toolName, "")},
			wantErr: `duplicate tool schedule for call "call-1"`,
		},
		{
			name: "update before schedule",
			events: []hooks.Event{
				hooks.NewToolCallUpdatedEvent(runID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}, session, callID, 1),
			},
			wantErr: `tool update requires active schedule for call "call-1"`,
		},
		{
			name:    "result before schedule",
			events:  []hooks.Event{result(toolName, "")},
			wantErr: "tool schedule is required",
		},
		{
			name:    "result name mismatch",
			events:  []hooks.Event{schedule(toolName, ""), result("svc.tools.other", "")},
			wantErr: `tool result name "svc.tools.other" does not match scheduled name "svc.tools.search"`,
		},
		{
			name: "result parent mismatch",
			events: []hooks.Event{
				schedule("svc.tools.parent", ""),
				hooks.NewToolCallScheduledEvent(
					runID,
					genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))},
					session,
					toolName,
					"child-1",
					[]byte(`{}`),
					"",
					callID,
					0,
				),
				hooks.NewToolResultReceivedEvent(
					runID,
					genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))},
					session,
					runID,
					toolName,
					"child-1",
					parentID,
					nil,
					nil,
					nil, "",
					nil,
					time.Second,
					nil,
					testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "failed"),
				),
			},
			wantErr: `tool result parent "parent-1" does not match scheduled parent "call-1"`,
		},
		{
			name: "update after completion",
			events: []hooks.Event{
				schedule(toolName, ""),
				result(toolName, ""),
				hooks.NewToolCallUpdatedEvent(runID, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}, session, callID, 1),
			},
			wantErr: `tool update follows completion for call "call-1"`,
		},
		{
			name:    "result after completion",
			events:  []hooks.Event{schedule(toolName, ""), result(toolName, ""), result(toolName, "")},
			wantErr: `tool result follows completion for call "call-1"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := make([]*runlog.Event, len(test.events))
			for index, event := range test.events {
				events[index] = snapshotRunlogEvent(t, event, index+1)
			}
			_, err := newRunSnapshot(events, "", nil)
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

func TestNewRunSnapshotValidatesToolResultPlacement(t *testing.T) {
	t.Parallel()

	schedule := func(runID string) hooks.Event {
		return hooks.NewToolCallScheduledEvent(
			runID,
			genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))},
			"session-1",
			"svc.tools.search",
			"call-1",
			nil,
			"",
			"",
			0,
		)
	}
	result := func(resultRunID, callRunID string) hooks.Event {
		return hooks.NewToolResultReceivedEvent(
			resultRunID,
			genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))},
			"session-1",
			callRunID,
			"svc.tools.search",
			"call-1",
			"",
			nil,
			nil,
			nil, "",
			nil,
			time.Second,
			nil,
			testToolFailure(planner.FailureInternal, planner.RecoveryFinish, "failed"),
		)
	}
	tests := []struct {
		name    string
		events  []hooks.Event
		wantErr string
	}{
		{
			name:   "same run with matching local schedule",
			events: []hooks.Event{schedule("call-run"), result("call-run", "call-run")},
		},
		{
			name:    "same run without local schedule",
			events:  []hooks.Event{result("call-run", "call-run")},
			wantErr: "tool schedule is required",
		},
		{
			name:    "cross run without source schedule",
			events:  []hooks.Event{result("result-run", "call-run")},
			wantErr: "tool schedule is required",
		},
		{
			name:    "cross run with local schedule",
			events:  []hooks.Event{schedule("result-run"), result("result-run", "call-run")},
			wantErr: "reuses a schedule from result run",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := make([]*runlog.Event, len(test.events))
			for index, event := range test.events {
				events[index] = snapshotRunlogEvent(t, event, index+1)
			}
			snapshot, err := newRunSnapshot(events, "", nil)
			if test.wantErr != "" {
				require.ErrorContains(t, err, test.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, snapshot.ToolCalls, 1)
			require.Equal(t, "call-1", snapshot.ToolCalls[0].ToolCallID)
			require.Equal(t, tools.Ident("svc.tools.search"), snapshot.ToolCalls[0].ToolName)
		})
	}
}

func TestNewRunSnapshotRetainsPendingRequestWhenSuspended(t *testing.T) {
	t.Parallel()

	await := hooks.NewAwaitClarificationEvent(
		"run-1",
		genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))},
		"sess-1",
		"question-1",
		"Which unit?",
		nil,
		"",
		nil,
	)
	suspended := hooks.NewRunSuspendedEvent(
		"run-1",
		genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))},
		"sess-1",
		"suspension-1",
		"v1",
		1,
		nil,
	)
	events := make([]*runlog.Event, 0, 2)
	for i, event := range []hooks.Event{await, suspended} {
		input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{
			TurnID:      "turn-1",
			EventKey:    fmt.Sprintf("evt-%d", i),
			TimestampMS: int64(i + 1),
		})
		require.NoError(t, err)
		events = append(events, &runlog.Event{
			EventKey: input.EventKey, RunID: input.RunID, Subject: input.Subject,

			SessionID: input.SessionID, TurnID: input.TurnID, Type: input.Type,
			Payload: input.Payload, Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
		})
	}

	snapshot, err := newRunSnapshot(events, "", nil)
	require.NoError(t, err)
	require.Equal(t, run.StatusSuspended, snapshot.Status)
	require.Equal(t, run.PhaseSuspended, snapshot.Phase)
	require.NotNil(t, snapshot.Await)
	require.Equal(t, "question-1", snapshot.Await.ID)
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
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agent.Ident("svc.agent")))},
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Type:      transcript.RunLogMessagesAppended,
		Payload:   payload,
		Timestamp: time.Unix(10, 0).UTC(),
	}}, "", nil)
	require.NoError(t, err)
	require.Len(t, snap.Transcript, 1)
	require.Equal(t, model.ConversationRoleUser, snap.Transcript[0].Role)
	require.Equal(t, []model.Part{model.TextPart{Text: "hello"}}, snap.Transcript[0].Parts)
}

func TestNewRunSnapshotReplaysSeededAndAppendedTranscriptMessages(t *testing.T) {
	t.Parallel()

	seedPayload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "hello"}},
	}})
	require.NoError(t, err)
	appendPayload, err := transcript.EncodeRunLogDelta([]*model.Message{{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "world"}},
	}})
	require.NoError(t, err)

	snap, err := newRunSnapshot([]*runlog.Event{
		{
			EventKey:  "evt-seed",
			RunID:     "run-1",
			Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agent.Ident("svc.agent")))},
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Type:      transcript.RunLogMessagesSeeded,
			Payload:   seedPayload,
			Timestamp: time.Unix(10, 0).UTC(),
		},
		{
			EventKey:  "evt-append",
			RunID:     "run-1",
			Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agent.Ident("svc.agent")))},
			SessionID: "sess-1",
			TurnID:    "turn-1",
			Type:      transcript.RunLogMessagesAppended,
			Payload:   appendPayload,
			Timestamp: time.Unix(20, 0).UTC(),
		},
	}, "", nil)
	require.NoError(t, err)
	require.Len(t, snap.Transcript, 2)
	require.Equal(t, model.ConversationRoleUser, snap.Transcript[0].Role)
	require.Equal(t, model.ConversationRoleAssistant, snap.Transcript[1].Role)
	require.Equal(t, "world", snap.LastAssistantMessage)
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
		Subject:   genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(agent.Ident("svc.agent")))},
		SessionID: "sess-1",
		TurnID:    "turn-1",
		Type:      transcript.RunLogMessagesAppended,
		Payload:   payload,
		Timestamp: time.Unix(20, 0).UTC(),
	}}, "", nil)
	require.NoError(t, err)
	require.Equal(t, "final hello", snap.LastAssistantMessage)
}

// snapshotRunlogEvent encodes one hook event into the oldest-first rows consumed
// by snapshot replay tests.
func snapshotRunlogEvent(t *testing.T, event hooks.Event, position int) *runlog.Event {
	t.Helper()
	input, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{
		TurnID:      "turn-1",
		EventKey:    fmt.Sprintf("event-%d", position),
		TimestampMS: int64(position),
	})
	require.NoError(t, err)
	return &runlog.Event{
		EventKey: input.EventKey,
		RunID:    input.RunID,
		Subject:  input.Subject,

		SessionID: input.SessionID,
		TurnID:    input.TurnID,
		Type:      input.Type,
		Payload:   input.Payload,
		Timestamp: time.UnixMilli(input.TimestampMS).UTC(),
	}
}

func TestNewRunSnapshotKeepsServiceSubjectWithoutMessages(t *testing.T) {
	t.Parallel()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{
		Registry: "primary", Tool: "records.lookup",
	})}
	events := []*runlog.Event{
		snapshotRunlogEvent(t, hooks.NewRunStartedEvent("service-run", subject, "session", "", "", nil), 1),
		snapshotRunlogEvent(t, hooks.NewToolCallScheduledEvent("service-run", subject, "session", "records.lookup", "call", nil, "", "", 0), 2),
		snapshotRunlogEvent(t, hooks.NewToolResultReceivedEvent("service-run", subject, "session", "service-run", "records.lookup", "call", "", nil, nil, nil, "", nil, 0, nil, nil), 3),
		snapshotRunlogEvent(t, hooks.NewRunCompletedEvent("service-run", subject, "session", "success", run.PhaseCompleted, nil, nil, nil), 4),
	}
	snapshot, err := newRunSnapshot(events, "", nil)
	require.NoError(t, err)
	require.Equal(t, subject, snapshot.Subject)
	require.Equal(t, run.StatusCompleted, snapshot.Status)
	require.Len(t, snapshot.ToolCalls, 1)
	require.Empty(t, snapshot.Transcript)
	require.Empty(t, snapshot.LastAssistantMessage)
	storedService, ok := events[0].Subject.Executor.AsServiceTool()
	require.True(t, ok)
	storedService.Registry = "changed-after-read"
	require.Equal(t, subject, snapshot.Subject)
}

func TestNewRunSnapshotRejectsInvalidOrChangedSubject(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		subject genrunidentity.RunSubject
	}{
		{name: "no branch"},
		{name: "empty Agent", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("")}},
		{name: "incomplete service", subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Tool: "records.lookup"})}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newRunSnapshot([]*runlog.Event{{RunID: "run", Subject: test.subject, Type: "ignored"}}, "", nil)
			require.ErrorContains(t, err, "snapshot run subject")
		})
	}
	first := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"})}
	other := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "other", Tool: "records.lookup"})}
	_, err := newRunSnapshot([]*runlog.Event{
		{RunID: "run", Subject: first, Type: "ignored"},
		{RunID: "run", Subject: other, Type: "ignored"},
	}, "", nil)
	require.ErrorContains(t, err, "snapshot event subject does not match")
}

func TestNewRunSnapshotRejectsServiceModelEvents(t *testing.T) {
	t.Parallel()
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "records.lookup"})}
	for _, eventType := range []runlog.Type{hooks.PolicyDecision, hooks.Usage, transcript.RunLogMessagesAppended} {
		t.Run(string(eventType), func(t *testing.T) {
			_, err := newRunSnapshot([]*runlog.Event{{RunID: "run", Subject: subject, Type: eventType, Payload: []byte(`[]`)}}, "", nil)
			require.ErrorContains(t, err, "requires an Agent subject")
		})
	}
}
