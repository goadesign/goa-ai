package inmem

// Store admission compares the original call under the same lock that writes
// the successor and its parent link. Runtime checkpoint membership is tested
// separately through the real workflow callbacks.

import (
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/storage/lifecycle"
)

func TestChildContinuationPreservesCallAcrossExecutionParents(t *testing.T) {
	store, parent0 := runningRootStore(t)
	ctx := t.Context()
	command := func(parent session.RunStart, runID, predecessor, callID string) storage.ChildRunStart {
		start := publishStartHistory(t, store, session.RunStart{
			AgentID: "child", RunID: runID, SessionID: parent0.SessionID, ParentRunID: parent.RunID,
			PredecessorRunID: predecessor, StartedAt: parent0.StartedAt,
		})
		return storage.ChildRunStart{
			RequestDigest: [32]byte{1}, Run: start,
			ParentLinked: hookRecord(t, "link-"+runID, start.StartedAt, hooks.NewChildRunLinkedEvent(
				parent.RunID, agent.Ident(parent.AgentID), start.SessionID, "child", callID, runID, "child",
			)),
			Started:  startedRecord(t, "start", start),
			Canceled: completedRecord(t, "stop", start, "canceled", &run.Cancellation{Reason: run.CancellationReasonSessionEnded}),
		}
	}
	suspend := func(start session.RunStart) {
		_, err := store.RecordRunSuspension(ctx, storage.RunSuspension{
			RunID: start.RunID, Suspension: session.RunSuspension{ID: "checkpoint-" + start.RunID, Data: []byte(`{"version":"v6"}`)},
			Record: suspendedRecord(t, "suspended", start, "checkpoint-"+start.RunID),
		})
		require.NoError(t, err)
	}
	a0 := command(parent0, "A0", "", "call-A")
	b0 := command(parent0, "B0", "", "call-B")
	for _, child := range []storage.ChildRunStart{a0, b0} {
		_, err := store.StartChildRun(ctx, child)
		require.NoError(t, err)
		suspend(child.Run)
	}
	suspend(parent0)
	parent1 := publishStartHistory(t, store, session.RunStart{AgentID: parent0.AgentID, RunID: "parent-1", SessionID: parent0.SessionID, PredecessorRunID: parent0.RunID, StartedAt: parent0.StartedAt})
	_, err := store.StartRootRun(ctx, rootStartCommand(t, parent1))
	require.NoError(t, err)
	a1 := command(parent1, "A1", a0.Run.RunID, "call-A")
	first, err := store.StartChildRun(ctx, a1)
	require.NoError(t, err)
	require.Equal(t, session.RunStartProceed, first.Outcome)
	before, err := store.ListRunRecords(ctx, parent1.RunID, "", 100)
	require.NoError(t, err)
	wrong := command(parent1, "wrong-call", a0.Run.RunID, "call-B")
	_, err = store.StartChildRun(ctx, wrong)
	require.ErrorContains(t, err, "does not match predecessor parent tool call")
	_, err = store.LoadRun(ctx, wrong.Run.RunID)
	require.ErrorIs(t, err, session.ErrRunNotFound)
	after, err := store.ListRunRecords(ctx, parent1.RunID, "", 100)
	require.NoError(t, err)
	require.Equal(t, before, after)

	_, err = store.CreateSession(ctx, "other-chat", parent0.StartedAt)
	require.NoError(t, err)
	foreign := publishStartHistory(t, store, session.RunStart{AgentID: parent0.AgentID, RunID: "foreign-parent", SessionID: "other-chat", StartedAt: parent0.StartedAt})
	_, err = store.StartRootRun(ctx, rootStartCommand(t, foreign))
	require.NoError(t, err)
	crossChat := command(foreign, "cross-chat-child", a0.Run.RunID, "call-A")
	_, err = store.StartChildRun(ctx, crossChat)
	require.Error(t, err)
	_, err = store.LoadRun(ctx, crossChat.Run.RunID)
	require.ErrorIs(t, err, session.ErrRunNotFound)
	foreignRecords, err := store.ListRunRecords(ctx, foreign.RunID, "", 100)
	require.NoError(t, err)
	require.Len(t, foreignRecords.Events, 1)

	suspend(a1.Run)
	// This direct store case keeps a still-running physical parent and the same call.
	a2 := command(parent1, "A2", a1.Run.RunID, "call-A")
	_, err = store.StartChildRun(ctx, a2)
	require.NoError(t, err)
	suspend(a2.Run)
	suspend(parent1)
	parent2 := publishStartHistory(t, store, session.RunStart{AgentID: parent0.AgentID, RunID: "parent-2", SessionID: parent0.SessionID, PredecessorRunID: parent1.RunID, StartedAt: parent0.StartedAt})
	_, err = store.StartRootRun(ctx, rootStartCommand(t, parent2))
	require.NoError(t, err)
	b1 := command(parent2, "B1", b0.Run.RunID, "call-B")
	admitted, err := store.StartChildRun(ctx, b1)
	require.NoError(t, err)
	for _, selected := range []storage.ChildRunStart{b0, b1} {
		childMeta, err := store.LoadRun(ctx, selected.Run.RunID)
		require.NoError(t, err)
		parentMeta, err := store.LoadRun(ctx, selected.Run.ParentRunID)
		require.NoError(t, err)
		link, err := store.selectedStartRecordLocked(parentMeta.RunID, store.lifecycle[childMeta.RunID].parentLink)
		require.NoError(t, err)
		require.NoError(t, lifecycle.ValidateStoredChildLink(link, parentMeta, childMeta))
		require.Equal(t, selected.Run.ParentRunID, childMeta.ParentRunID)
	}
	suspend(b1.Run)
	suspend(parent2)
	beforeClosed, err := store.ListSessionRunRecords(ctx, parent0.SessionID, "", 100)
	require.NoError(t, err)
	retried, err := store.StartChildRun(ctx, b1)
	require.NoError(t, err)
	require.Equal(t, admitted.ParentRecord.ID, retried.ParentRecord.ID)
	require.Equal(t, admitted.Started.ID, retried.Started.ID)
	require.False(t, retried.ParentRecord.Inserted)
	require.False(t, retried.Started.Inserted)
	require.Equal(t, session.RunStatusSuspended, retried.RunStatus)
	afterClosed, err := store.ListSessionRunRecords(ctx, parent0.SessionID, "", 100)
	require.NoError(t, err)
	require.Equal(t, beforeClosed, afterClosed)
	fresh := command(parent2, "late-child", b0.Run.RunID, "call-B")
	_, err = store.StartChildRun(ctx, fresh)
	require.ErrorIs(t, err, session.ErrRunNotActive)
	_, err = store.LoadRun(ctx, fresh.Run.RunID)
	require.ErrorIs(t, err, session.ErrRunNotFound)
	changed := b1
	changed.RequestDigest = [32]byte{2}
	_, err = store.StartChildRun(ctx, changed)
	require.ErrorIs(t, err, session.ErrRunConflict)
}
