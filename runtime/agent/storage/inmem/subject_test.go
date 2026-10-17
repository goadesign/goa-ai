// These tests verify copied service-tool subjects and semantic replay through
// storage operations, while child links keep real Agent identifiers.
package inmem

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

// Service-tool subjects are copied at storage boundaries and compared by value
// so separately decoded retries preserve the same run and prepared body.
func TestServiceSubjectStorageIsolationAndReplay(t *testing.T) {
	ctx := t.Context()
	store := New()
	tool := &genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"}
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(tool)}
	at := time.Now().UTC().Truncate(time.Millisecond)
	_, err := store.CreateSession(ctx, "session", at)
	require.NoError(t, err)
	declaration := storage.SeedDeclaration{Subject: subject, RunID: "run", SessionID: "session", CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral}
	bindSeedTestOperation(t, store, declaration, rootExecutionOrigin("run"))
	seed, err := store.BeginRunSeed(ctx, declaration)
	require.NoError(t, err)
	returnedTool, ok := seed.Declaration.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	returnedTool.Registry = "changed-return"
	tool.Registry = "changed-input"
	fresh := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}
	declaration.Subject = fresh
	retriedSeed, err := store.BeginRunSeed(ctx, declaration)
	require.NoError(t, err)
	require.Equal(t, fresh, retriedSeed.Declaration.Subject)
	publication := appendSeedTestCompletion(t, store, declaration, storage.EmptySeedEndID)
	require.NoError(t, store.PublishRunSeed(ctx, publication))
	operation := storage.PreparationOperation{Subject: fresh, RunID: declaration.RunID, SessionID: declaration.SessionID, CommandID: declaration.CommandID}
	prepared, found, err := store.FindRunPreparation(ctx, operation)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, storage.EmptySeedEndID, prepared.Seed.EndID)
	history, err := store.ListRunSeedRecords(ctx, "run", prepared.Seed.EndID, "", 10)
	require.NoError(t, err)
	require.Empty(t, history.Records)
	preparedTool, ok := prepared.Seed.Declaration.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	preparedTool.Tool = "catalog.changed"
	preparedAgain, found, err := store.FindRunPreparation(ctx, operation)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, fresh, preparedAgain.Seed.Declaration.Subject)
	start := session.RunStart{Subject: fresh, RunID: "run", SessionID: "session", SeedEndID: prepared.Seed.EndID, StartedAt: at}
	command := rootStartCommand(t, start)
	first, err := store.StartRootRun(ctx, command)
	require.NoError(t, err)
	decoded := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}
	start.Subject = decoded
	retry, err := store.StartRootRun(ctx, rootStartCommand(t, start))
	require.NoError(t, err)
	require.Equal(t, first.Started.ID, retry.Started.ID)
	require.False(t, retry.Started.Inserted)
	inputTool, ok := fresh.Executor.AsServiceTool()
	require.True(t, ok)
	inputTool.Registry = "changed-start"
	meta, err := store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, decoded, meta.Subject)
	metaTool, ok := meta.Subject.Executor.AsServiceTool()
	require.True(t, ok)
	metaTool.Tool = "catalog.changed"
	page, err := store.ListRunRecords(ctx, "run", "", 10)
	require.NoError(t, err)
	require.Len(t, page.Events, 1)
	require.Equal(t, decoded, page.Events[0].Subject)
	pageTool, ok := page.Events[0].Subject.Executor.AsServiceTool()
	require.True(t, ok)
	pageTool.Registry = "changed-page"
	page, err = store.ListRunRecords(ctx, "run", "", 10)
	require.NoError(t, err)
	require.Equal(t, decoded, page.Events[0].Subject)
	meta, err = store.LoadRun(ctx, "run")
	require.NoError(t, err)
	require.Equal(t, decoded, meta.Subject)
	for _, different := range []genrunidentity.RunSubject{
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "other", Tool: "catalog.lookup"})},
		{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.other"})},
		{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("catalog.lookup"))},
	} {
		changed := operation
		changed.Subject = different
		_, _, err := store.FindRunPreparation(ctx, changed)
		require.ErrorIs(t, err, storage.ErrSeedConflict)
		event := record("note", "run", different, "session", "note")
		_, err = store.AppendRunRecord(ctx, event)
		require.ErrorIs(t, err, storage.ErrRunRecordOwnerMismatch)
	}
	ordinary := record("note", "run", decoded, "session", "note")
	inserted, err := store.AppendRunRecord(ctx, ordinary)
	require.NoError(t, err)
	ordinary.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}
	replayed, err := store.AppendRunRecord(ctx, ordinary)
	require.NoError(t, err)
	require.Equal(t, inserted.ID, replayed.ID)
	require.False(t, replayed.Inserted)
}

func TestServicePreparationSettlementCopiesSubject(t *testing.T) {
	store := New()
	tool := &genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"}
	attempt := storage.PreparationAttempt{Operation: storage.PreparationOperation{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(tool)}, RunID: "run", CommandID: "command",
	}, AttemptID: "attempt"}
	_, found, err := store.SettleRunPreparation(t.Context(), attempt)
	require.NoError(t, err)
	require.False(t, found)
	tool.Registry = "changed"
	attempt.Operation.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}
	_, found, err = store.SettleRunPreparation(t.Context(), attempt)
	require.NoError(t, err)
	require.False(t, found)
	declaration := storage.SeedDeclaration{
		Subject: attempt.Operation.Subject, RunID: "run", CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral,
	}
	bindSeedTestOperation(t, store, declaration, rootExecutionOrigin("run"))
	_, err = store.BeginRunSeed(t.Context(), declaration)
	require.ErrorIs(t, err, storage.ErrPreparationAbandoned)
}

// Service-tool parents use the same lifecycle writes and real Agent-child links
// as Agent parents, including value-equal subjects decoded from separate records.
func TestServiceSubjectLifecycleAndAgentChild(t *testing.T) {
	for _, sessionless := range []bool{false, true} {
		t.Run(fmt.Sprint("sessionless=", sessionless), func(t *testing.T) {
			store, start := activeRunStore(t)
			start.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "primary", Tool: "catalog.lookup"})}
			if sessionless {
				start.SessionID = ""
			}
			start = publishStartHistory(t, store, start, "")
			if sessionless {
				_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart{RequestDigest: [32]byte{1}, Run: start, Started: startedRecord(t, "start", start)})
				require.NoError(t, err)
			} else {
				_, err := store.StartRootRun(t.Context(), rootStartCommand(t, start))
				require.NoError(t, err)
			}
			child := publishStartHistory(t, store, session.RunStart{
				Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("child")},
				RunID:   "child", SessionID: start.SessionID, ParentRunID: start.RunID, StartedAt: start.StartedAt,
			}, "tool-call")
			if sessionless {
				_, err := store.StartOneShotChildRun(t.Context(), storage.OneShotChildRunStart{RequestDigest: [32]byte{2}, Run: child, ParentLinked: childLinkRecord(t, "child-link", start, child), Started: startedRecord(t, "start", child)})
				require.NoError(t, err)
			} else {
				_, err := store.StartChildRun(t.Context(), storage.ChildRunStart{RequestDigest: [32]byte{2}, Run: child, ParentLinked: childLinkRecord(t, "child-link", start, child), Started: startedRecord(t, "start", child)})
				require.NoError(t, err)
			}
			_, err := store.RecordRunTerminal(t.Context(), storage.RunTerminal{RunID: child.RunID, Status: session.RunStatusCompleted, Record: completedRecord(t, "done", child, "success", nil)})
			require.NoError(t, err)
			_, err = store.RecordRunCancellation(t.Context(), storage.RunCancellation{RunID: start.RunID, Reason: run.CancellationReasonEngineCanceled, Record: cancellationRecord(t, "cancel", start, run.CancellationReasonEngineCanceled)})
			require.NoError(t, err)
			_, err = store.RecordRunTerminal(t.Context(), storage.RunTerminal{RunID: start.RunID, Status: session.RunStatusCanceled, Record: completedRecord(t, "done", start, "canceled", &run.Cancellation{Reason: run.CancellationReasonEngineCanceled})})
			require.NoError(t, err)
			meta, err := store.LoadRun(t.Context(), start.RunID)
			require.NoError(t, err)
			require.Equal(t, session.RunStatusCanceled, meta.Status)
			require.Equal(t, start.Subject, meta.Subject)
		})
	}
}

// subjectAgent verifies that a child-link fixture names a real Agent before
// passing its identifier to the Agent-only child contract.
func subjectAgent(t *testing.T, subject genrunidentity.RunSubject) agent.Ident {
	t.Helper()
	id, ok := subject.Executor.AsAgent()
	require.True(t, ok)
	return agent.Ident(id)
}
