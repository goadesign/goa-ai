// These tests exercise snapshots across exact saved continuation sources. They
// keep completed history separate from unfinished tool work and current state.
package runtime

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// snapshotHistoryStore returns typed saved records, including malformed
	// owner results for tests of the snapshot reader's storage boundary.
	snapshotHistoryStore struct {
		storage.Store
		runs           map[string]session.RunMeta
		seeds          map[string]storage.RunSeed
		events         map[string][]*runlog.Event
		repeatedCursor bool
	}
)

func TestNewRunSnapshotContinuesOnlyUnfinishedCalls(t *testing.T) {
	t.Parallel()
	schedule := func(owner, call string) hooks.Event {
		return hooks.NewToolCallScheduledEvent(owner, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "records.lookup", call, nil, "", "", 0)
	}
	result := func(owner, source, call string) hooks.Event {
		return hooks.NewToolResultReceivedEvent(owner, genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", source, "records.lookup", call, "", nil, nil, nil, "", nil, 0, nil, nil)
	}
	inherited := snapshotEvents(t,
		hooks.NewRunStartedEvent("original", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "", "", map[string]string{"generation": "old"}),
		schedule("original", "completed"), result("original", "original", "completed"),
		schedule("original", "pending-a"), schedule("original", "pending-b"),
		hooks.NewRunSuspendedEvent("original", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pause-one", "v1", 1, nil),
		hooks.NewRunStartedEvent("middle", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "", "original", nil),
		hooks.NewToolCallUpdatedEvent("middle", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pending-a", 2),
		result("middle", "original", "pending-b"),
		hooks.NewRunSuspendedEvent("middle", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pause-two", "v1", 1, nil),
	)
	current := snapshotEvents(t,
		hooks.NewRunStartedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "", "middle", map[string]string{"generation": "new"}),
		hooks.NewToolCallUpdatedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pending-a", 3),
		result("current", "original", "pending-a"),
		hooks.NewRunCompletedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "success", run.PhaseCompleted, nil, nil, nil),
	)
	snapshot, err := newRunSnapshot(current, "", inherited)
	require.NoError(t, err)
	assert.Equal(t, "current", snapshot.RunID)
	assert.Equal(t, run.StatusCompleted, snapshot.Status)
	assert.Equal(t, map[string]string{"generation": "new"}, snapshot.Labels)
	assert.Equal(t, current[0].Timestamp, snapshot.StartedAt)
	require.Len(t, snapshot.ToolCalls, 1)
	assert.Equal(t, "pending-a", snapshot.ToolCalls[0].ToolCallID)
	assert.Equal(t, inherited[3].Timestamp, snapshot.ToolCalls[0].ScheduledAt)
	assert.Equal(t, 3, snapshot.ToolCalls[0].ExpectedChildrenTotal)
	assert.False(t, snapshot.ToolCalls[0].CompletedAt.IsZero())

	for _, test := range []struct {
		name  string
		event hooks.Event
		error string
	}{
		{"completed update", hooks.NewToolCallUpdatedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "completed", 1), "follows completion"},
		{"completed in middle", result("current", "original", "pending-b"), "follows completion"},
		{"unrelated update", hooks.NewToolCallUpdatedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "other", 1), "requires active schedule"},
		{"wrong source", result("current", "middle", "pending-a"), "does not match scheduled run"},
		{"unrelated result", result("current", "unrelated", "other"), "tool schedule is required"},
		{"duplicate inherited schedule", schedule("current", "pending-a"), "duplicate tool schedule"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newRunSnapshot(snapshotEvents(t, test.event), "", inherited)
			assert.ErrorContains(t, err, test.error)
		})
	}
}

func TestSnapshotInheritedEventsIncludesExecutionAfterConversation(t *testing.T) {
	t.Parallel()
	store := snapshotHistoryFixture(t)
	rt := &Runtime{Store: store}
	inherited, err := rt.snapshotInheritedEvents(t.Context(), store.runs["current"])
	require.NoError(t, err)
	require.Len(t, inherited, 8)
	assert.Equal(t, "original", inherited[0].RunID)
	assert.Equal(t, "middle", inherited[5].RunID)
	assert.Equal(t, hooks.ToolCallUpdated, inherited[6].Type)
	// The saved conversation ends before these schedules and completions.
	// Execution replay must retain the pending call and reject the completed one.
	current := snapshotEvents(t, hooks.NewToolCallUpdatedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pending", 4))
	snapshot, err := newRunSnapshot(current, "", inherited)
	require.NoError(t, err)
	require.Len(t, snapshot.ToolCalls, 1)
	assert.Equal(t, 4, snapshot.ToolCalls[0].ExpectedChildrenTotal)
	_, err = newRunSnapshot(snapshotEvents(t, hooks.NewToolCallUpdatedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "completed", 4)), "", inherited)
	assert.ErrorContains(t, err, "follows completion")
}

func TestSnapshotInheritedEventsRejectsChangedSource(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		change func(*snapshotHistoryStore)
		error  string
	}{
		{"seed run", func(s *snapshotHistoryStore) {
			v := s.seeds["current"]
			v.Declaration.RunID = "other"
			s.seeds["current"] = v
		}, "owner does not match"},
		{"seed agent", func(s *snapshotHistoryStore) {
			v := s.seeds["current"]
			v.Declaration.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("other"))}
			s.seeds["current"] = v
		}, "owner does not match"},
		{"seed session", func(s *snapshotHistoryStore) {
			v := s.seeds["current"]
			v.Declaration.SessionID = "other"
			s.seeds["current"] = v
		}, "owner does not match"},
		{"source run", func(s *snapshotHistoryStore) { s.seeds["current"].Source.RunID = "other" }, "seed conflict"},
		{"source end", func(s *snapshotHistoryStore) { s.seeds["current"].Source.EndID = "other" }, "seed conflict"},
		{"source owner", func(s *snapshotHistoryStore) {
			v := s.runs["middle"]
			v.SessionID = "other"
			s.runs["middle"] = v
		}, "owner does not match"},
		{"missing position", func(s *snapshotHistoryStore) {
			v := s.seeds["current"]
			v.Declaration.SourceEndID = "missing"
			v.Source.EndID = "missing"
			s.seeds["current"] = v
		}, "invalid transcript position"},
		{"source cycle", func(s *snapshotHistoryStore) {
			v := s.seeds["middle"]
			v.Declaration.SourceRunID = "current"
			v.Declaration.PredecessorRunID = "current"
			v.Source.RunID = "current"
			s.seeds["middle"] = v
			s.events["current"] = snapshotEvents(t, hooks.NewToolCallUpdatedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pending", 4), hooks.NewToolCallUpdatedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pending", 5))
		}, "repeats run"},
		{"repeated cursor", func(s *snapshotHistoryStore) { s.repeatedCursor = true }, "made no progress"},
		{"running predecessor", func(s *snapshotHistoryStore) {
			v := s.runs["middle"]
			v.Status = session.RunStatusRunning
			s.runs["middle"] = v
		}, "is not suspended"},
		{"missing terminal", func(s *snapshotHistoryStore) { s.events["middle"] = s.events["middle"][:2] }, "no final suspension record"},
		{"completed terminal", func(s *snapshotHistoryStore) {
			s.events["middle"][2] = snapshotEvents(t, hooks.NewRunCompletedEvent("middle", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "success", run.PhaseCompleted, nil, nil, nil))[0]
		}, "no final suspension record"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := snapshotHistoryFixture(t)
			test.change(store)
			_, err := (&Runtime{Store: store}).snapshotInheritedEvents(t.Context(), store.runs["current"])
			assert.ErrorContains(t, err, test.error)
		})
	}
}

func TestSnapshotNextTurnDoesNotInheritExecution(t *testing.T) {
	t.Parallel()
	store := snapshotHistoryFixture(t)
	seed := store.seeds["current"]
	seed.Declaration.Kind = storage.SeedNextTurn
	seed.Declaration.PredecessorRunID = ""
	store.seeds["current"] = seed
	inherited, err := (&Runtime{Store: store}).snapshotInheritedEvents(t.Context(), store.runs["current"])
	require.NoError(t, err)
	assert.Empty(t, inherited)
	_, err = newRunSnapshot(snapshotEvents(t, hooks.NewToolCallUpdatedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pending", 4)), "", inherited)
	assert.ErrorContains(t, err, "requires active schedule")
}

func TestSnapshotParentCallUsesInheritedScheduleAndCurrentLink(t *testing.T) {
	t.Parallel()
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "pending", true: "completed before link"}[completed], func(t *testing.T) {
			store := snapshotHistoryFixture(t)
			store.events["current"] = snapshotEvents(t,
				hooks.NewChildRunLinkedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "records.lookup", "pending", "child", "specialist"),
				hooks.NewToolResultReceivedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "original", "records.lookup", "pending", "", nil, nil, nil, "", nil, 0, nil, nil),
			)
			if completed {
				store.events["current"][0], store.events["current"][1] = store.events["current"][1], store.events["current"][0]
			}
			parent, err := (&Runtime{Store: store}).snapshotParentToolCall(t.Context(), session.RunMeta{RunID: "child", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent("specialist"))}, SessionID: "session", ParentRunID: "current"})
			if completed {
				assert.ErrorContains(t, err, "no matching active")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, "pending", parent)
			snapshot, err := newRunSnapshot(snapshotEvents(t,
				hooks.NewToolCallScheduledEvent("child", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("specialist"))}, "session", "records.detail", "detail", nil, "", parent, 0),
				hooks.NewToolResultReceivedEvent("child", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("specialist"))}, "session", "child", "records.detail", "detail", parent, nil, nil, nil, "", nil, 0, nil, nil),
			), parent, nil)
			require.NoError(t, err)
			require.Len(t, snapshot.ToolCalls, 1)
			assert.Equal(t, parent, snapshot.ToolCalls[0].ParentToolCallID)
		})
	}
}

func TestGetRunSnapshotAcrossStoredContinuations(t *testing.T) {
	t.Parallel()
	store := newTestStore()
	ctx := t.Context()
	rt := New(store)
	admitRunForTest(t, store, session.RunMeta{RunID: "original", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
		RunExecutorBranchAgent("worker"))}, SessionID: "session", Status: session.RunStatusRunning})
	base := workflowConversation{RunContext: run.Context{RunID: "original", SessionID: "session"}}
	require.NoError(t, rt.appendSelectedModelResponse(ctx, "worker", &base, "turn", "response-original", nil, []*model.Message{assistantTextMsg("Accepted work")}))
	_, err := store.AppendRunRecord(ctx, snapshotEvents(t, hooks.NewToolCallScheduledEvent("original", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "records.lookup", "pending", nil, "", "", 0))[0])
	require.NoError(t, err)
	for _, pair := range [][2]string{{"original", "middle"}, {"middle", "current"}} {
		_, err = store.RecordRunSuspension(ctx, storage.RunSuspension{
			RunID: pair[0], Suspension: session.RunSuspension{ID: "pause-" + pair[0], Data: []byte(`{}`)},
			Record: testHookRecord(t, hooks.NewRunSuspendedEvent(pair[0], genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pause-"+pair[0], "v1", 1, nil), terminalRunEventKey, time.Now().UTC()),
		})
		require.NoError(t, err)
		// Production prepares conversation history from the last saved message,
		// which precedes the tool schedule or update and final suspension.
		predecessor, err := store.LoadRun(ctx, pair[0])
		require.NoError(t, err)
		declaration := storage.SeedDeclaration{
			Origin: predecessor.Origin,
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent("worker"))}, RunID: pair[1], SessionID: "session", CommandID: pair[1], AttemptID: pair[1],
			Kind: storage.SeedContinuation, PredecessorRunID: pair[0], SourceRunID: pair[0], SourceEndID: base.HistoryEndID,
		}
		decision, err := bindHistoryOperation(ctx, store, declaration)
		require.NoError(t, err)
		_, proceed := decision.State.AsProceed()
		require.True(t, proceed)
		seed, err := store.BeginRunSeed(ctx, declaration)
		require.NoError(t, err)
		writer := initialHistoryWriter{store: store, runID: pair[1], attemptID: pair[1], endID: storage.EmptySeedEndID}
		require.NoError(t, writer.appendPrefix(ctx, seed.Source))
		seedEnd := writer.endID
		require.NoError(t, writer.publish(ctx, []byte(`{}`)))
		admitContinuedRunForTest(t, store, session.RunMeta{
			RunID: pair[1], Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent("worker"))}, SessionID: "session", SeedEndID: seedEnd,
			Status: session.RunStatusRunning, Labels: map[string]string{"generation": pair[1]},
		}, pair[0])
		base = workflowConversation{RunContext: run.Context{RunID: pair[1], SessionID: "session"}}
		require.NoError(t, rt.appendSelectedModelResponse(ctx, "worker", &base, "turn", "response-"+pair[1], nil, []*model.Message{assistantTextMsg("Continuing work")}))
		_, err = store.AppendRunRecord(ctx, snapshotEvents(t, hooks.NewToolCallUpdatedEvent(pair[1], genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pending", 2))[0])
		require.NoError(t, err)
	}
	result := snapshotRunlogEvent(t, hooks.NewToolResultReceivedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "original", "records.lookup", "pending", "", nil, nil, nil, "", nil, 0, nil, nil), 2)
	_, err = store.AppendRunRecord(ctx, result)
	require.NoError(t, err)
	_, err = store.RecordRunTerminal(ctx, storage.RunTerminal{
		RunID: "current", Status: session.RunStatusCompleted,
		Record: testHookRecord(t, hooks.NewRunCompletedEvent("current", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "success", run.PhaseCompleted, map[string]string{"generation": "current"}, nil, nil), terminalRunEventKey, time.Now().UTC()),
	})
	require.NoError(t, err)
	snapshot, err := rt.GetRunSnapshot(ctx, "current")
	require.NoError(t, err)
	assert.Equal(t, "current", snapshot.RunID)
	assert.Equal(t, run.StatusCompleted, snapshot.Status)
	assert.Equal(t, map[string]string{"generation": "current"}, snapshot.Labels)
	require.Len(t, snapshot.ToolCalls, 1)
	assert.Equal(t, "pending", snapshot.ToolCalls[0].ToolCallID)
	assert.Equal(t, 2, snapshot.ToolCalls[0].ExpectedChildrenTotal)
	assert.False(t, snapshot.ToolCalls[0].CompletedAt.IsZero())
	require.Len(t, snapshot.Transcript, 3)
	assert.Equal(t, "Accepted work", snapshot.Transcript[0].Text())
}

func (s *snapshotHistoryStore) LoadRun(_ context.Context, id string) (session.RunMeta, error) {
	return s.runs[id], nil
}
func (s *snapshotHistoryStore) LoadRunSeed(_ context.Context, id, _ string) (storage.RunSeed, error) {
	return s.seeds[id], nil
}
func (s *snapshotHistoryStore) ListRunRecords(_ context.Context, id, _ string, _ int) (runlog.Page, error) {
	if s.repeatedCursor {
		return runlog.Page{NextCursor: "stuck"}, nil
	}
	return runlog.Page{Events: s.events[id]}, nil
}

// snapshotHistoryFixture creates two successive continuations. Conversation
// positions precede schedules and results; each suspended log closes afterward.
func snapshotHistoryFixture(t *testing.T) *snapshotHistoryStore {
	t.Helper()
	origin := genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "original"})}
	s := &snapshotHistoryStore{
		runs:   map[string]session.RunMeta{},
		seeds:  map[string]storage.RunSeed{},
		events: map[string][]*runlog.Event{},
	}
	for _, id := range []string{"original", "middle", "current"} {
		s.runs[id] = session.RunMeta{Origin: origin, RunID: id, Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent("worker"))}, SessionID: "session", SeedEndID: "seed", Status: session.RunStatusSuspended}
	}
	for _, pair := range [][2]string{{"middle", "original"}, {"current", "middle"}} {
		s.seeds[pair[0]] = storage.RunSeed{
			Declaration: storage.SeedDeclaration{
				Origin: origin, RunID: pair[0], Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
					RunExecutorBranchAgent("worker"))}, SessionID: "session", CommandID: "command", AttemptID: "attempt",
				Kind: storage.SeedContinuation, PredecessorRunID: pair[1], SourceRunID: pair[1], SourceEndID: "1",
			},
			Source: &storage.HistoryPrefix{RunID: pair[1], EndID: "1"}, EndID: "seed",
		}
	}
	s.seeds["original"] = storage.RunSeed{
		Declaration: storage.SeedDeclaration{Origin: origin, RunID: "original", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent("worker"))}, SessionID: "session", Kind: storage.SeedLiteral}, EndID: "seed",
	}
	s.events["original"] = snapshotEvents(t,
		hooks.NewRunStartedEvent("original", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "", "", nil),
		hooks.NewToolCallScheduledEvent("original", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "records.lookup", "pending", nil, "", "", 0),
		hooks.NewToolCallScheduledEvent("original", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "records.lookup", "completed", nil, "", "", 0),
		hooks.NewToolResultReceivedEvent("original", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "original", "records.lookup", "completed", "", nil, nil, nil, "", nil, 0, nil, nil),
		hooks.NewRunSuspendedEvent("original", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pause-original", "v1", 1, nil),
	)
	s.events["middle"] = snapshotEvents(t,
		hooks.NewRunStartedEvent("middle", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "", "original", nil),
		hooks.NewToolCallUpdatedEvent("middle", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pending", 2),
		hooks.NewRunSuspendedEvent("middle", genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("worker"))}, "session", "pause-middle", "v1", 1, nil),
	)
	return s
}

// snapshotEvents encodes hook inputs with assigned event positions, matching
// the ordered owner records consumed by snapshot loading and replay.
func snapshotEvents(t *testing.T, events ...hooks.Event) []*runlog.Event {
	t.Helper()
	result := make([]*runlog.Event, len(events))
	for index, event := range events {
		result[index] = snapshotRunlogEvent(t, event, index+1)
		result[index].ID = strconv.Itoa(index + 1)
	}
	return result
}
