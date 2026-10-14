// These tests prove that early completion stores no Run and that preparation,
// first start and terminal decisions share one immutable execution owner.
package inmem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	genrunterminal "goa.design/goa-ai/registry/gen/runterminal"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestUnstartedTerminalFirstDecisionWinsWithoutRun(t *testing.T) {
	store, start := activeRunStore(t)
	command := earlyTerminalCommand(start)
	first, err := store.RecordUnstartedExecutionTerminal(t.Context(), command)
	require.NoError(t, err)
	winner, ok := first.Result.AsStored()
	require.True(t, ok)
	assert.Equal(t, command, *winner)
	_, err = store.LoadRun(t.Context(), start.RunID)
	assert.ErrorIs(t, err, session.ErrRunNotFound) //nolint:testifylint // The record and replay checks do not use the Run lookup result.
	assert.Empty(t, store.records[start.RunID])

	replay, err := store.RecordUnstartedExecutionTerminal(t.Context(), command)
	require.NoError(t, err)
	_, ok = replay.Result.AsReplayed()
	assert.True(t, ok)
	changed := command
	changed.Outcome = genrunterminal.NewUnstartedExecutionOutcomeCanceled(run.CancellationReasonSessionEnded)
	result, err := store.RecordUnstartedExecutionTerminal(t.Context(), changed)
	require.NoError(t, err)
	existing, ok := result.Result.AsExisting()
	require.True(t, ok)
	assert.Equal(t, command, *existing)

	begin := storage.RunPreparationBegin{Operation: storage.PreparationOperation{Subject: start.Subject, RunID: start.RunID, SessionID: start.SessionID, CommandID: "late"}, Origin: *command.Origin}
	_, err = store.BeginRunPreparationOperation(t.Context(), begin)
	assert.ErrorIs(t, err, session.ErrRunConflict) //nolint:testifylint // The following terminal request independently checks its changed digest.
	command.RequestDigest = append([]byte(nil), command.RequestDigest...)
	command.RequestDigest[0] = 2
	_, err = store.RecordUnstartedExecutionTerminal(t.Context(), command)
	assert.ErrorIs(t, err, session.ErrRunConflict)
}

func TestUnstartedTerminalOrdersWithStartAndPublication(t *testing.T) {
	for _, startFirst := range []bool{false, true} {
		t.Run(map[bool]string{false: "terminal first", true: "start first"}[startFirst], func(t *testing.T) {
			store, start := activeRunStore(t)
			start = publishStartHistory(t, store, start, "")
			if startFirst {
				_, err := store.StartRootRun(t.Context(), rootStartCommand(t, start))
				require.NoError(t, err)
			}
			before := len(store.records[start.RunID])
			result, err := store.RecordUnstartedExecutionTerminal(t.Context(), earlyTerminalCommand(start))
			require.NoError(t, err)
			_, started := result.Result.AsStarted()
			assert.Equal(t, startFirst, started)
			assert.Len(t, store.records[start.RunID], before)
			if startFirst {
				meta, err := store.LoadRun(t.Context(), start.RunID)
				require.NoError(t, err)
				assert.Equal(t, session.RunStatusRunning, meta.Status)
			} else {
				_, err := store.StartRootRun(t.Context(), rootStartCommand(t, start))
				assert.ErrorIs(t, err, session.ErrRunConflict) //nolint:testifylint // The Run lookup must still check whether the rejected start created a Run.
				_, err = store.LoadRun(t.Context(), start.RunID)
				assert.ErrorIs(t, err, session.ErrRunNotFound)
			}
		})
	}
}

func TestUnstartedTerminalRacesFirstStart(t *testing.T) {
	store, start := activeRunStore(t)
	start = publishStartHistory(t, store, start, "")
	startCommand := rootStartCommand(t, start)
	terminalCommand := earlyTerminalCommand(start)
	ready := make(chan struct{})
	started := make(chan error, 1)
	type terminalReply struct {
		result genrunterminal.UnstartedExecutionTerminalResult
		err    error
	}
	terminal := make(chan terminalReply, 1)
	go func() {
		<-ready
		_, err := store.StartRootRun(t.Context(), startCommand)
		started <- err
	}()
	go func() {
		<-ready
		result, err := store.RecordUnstartedExecutionTerminal(t.Context(), terminalCommand)
		terminal <- terminalReply{result: result, err: err}
	}()
	close(ready)
	startErr, result := <-started, <-terminal
	require.NoError(t, result.err)
	if _, exists := result.result.Result.AsStarted(); exists {
		require.NoError(t, startErr)
		assert.Contains(t, store.runs, start.RunID)
		assert.NotContains(t, store.unstartedTerminals, start.RunID)
	} else {
		_, stored := result.result.Result.AsStored()
		require.True(t, stored)
		assert.ErrorIs(t, startErr, session.ErrRunConflict)
		assert.NotContains(t, store.runs, start.RunID)
		assert.Empty(t, store.records[start.RunID])
	}
}

func TestUnstartedTerminalRejectsLatePreparationPublication(t *testing.T) {
	store, start := activeRunStore(t)
	declaration := storage.SeedDeclaration{
		Origin: rootExecutionOrigin(start.RunID), Subject: start.Subject,
		RunID: start.RunID, SessionID: start.SessionID,
		CommandID: "command", AttemptID: "attempt", Kind: storage.SeedLiteral,
	}
	bindSeedTestOperation(t, store, declaration)
	seed, err := store.BeginRunSeed(t.Context(), declaration)
	require.NoError(t, err)
	publication := appendSeedTestCompletion(t, store, declaration, seed.EndID)
	_, err = store.RecordUnstartedExecutionTerminal(t.Context(), earlyTerminalCommand(start))
	require.NoError(t, err)
	assert.ErrorIs(t, store.PublishRunSeed(t.Context(), publication), session.ErrRunConflict) //nolint:testifylint // The seed and Run lookups must still check storage after this publication attempt.
	_, err = store.LoadRunSeed(t.Context(), start.RunID, publication.SeedEndID)
	assert.ErrorIs(t, err, storage.ErrSeedNotFound) //nolint:testifylint // The Run lookup independently checks that terminal storage created no Run.
	_, err = store.LoadRun(t.Context(), start.RunID)
	assert.ErrorIs(t, err, session.ErrRunNotFound)
}

func TestUnstartedTerminalRejectsChildCollisionBeforePreparation(t *testing.T) {
	store, start := activeRunStore(t)
	command := earlyTerminalCommand(start)
	command.Origin = &genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: "parent", ToolCallID: "call"})}
	_, err := store.RecordUnstartedExecutionTerminal(t.Context(), command)
	assert.ErrorIs(t, err, storage.ErrSeedConflict) //nolint:testifylint // The storage checks must still verify that the rejected child invocation saved nothing.
	assert.Empty(t, store.unstartedTerminals)
	assert.Empty(t, store.records)
	_, err = store.RecordUnstartedExecutionTerminal(t.Context(), earlyTerminalCommand(start))
	require.NoError(t, err)
	_, err = store.RecordUnstartedExecutionTerminal(t.Context(), command)
	assert.ErrorIs(t, err, session.ErrRunConflict)
}

func TestUnstartedContinuationKeepsOriginalInvocation(t *testing.T) {
	for _, kind := range []string{"root", "child"} {
		t.Run(kind, func(t *testing.T) {
			store, predecessor := activeRunStore(t)
			replayStartOperation(t, store, predecessor, kind)()
			meta, err := store.LoadRun(t.Context(), predecessor.RunID)
			require.NoError(t, err)
			predecessor.ParentRunID = meta.ParentRunID
			closeReplayRun(t, store, predecessor, session.RunStatusSuspended)
			successor := predecessor
			successor.RunID = "successor"
			successor.PredecessorRunID = predecessor.RunID
			successor = publishStartHistory(t, store, successor, "")
			competitor := successor
			competitor.RunID = "competitor"
			competitor = publishStartHistory(t, store, competitor, "")
			command := earlyTerminalCommand(successor)
			_, err = store.RecordUnstartedExecutionTerminal(t.Context(), command)
			require.ErrorIs(t, err, session.ErrRunConflict, "current Run ID cannot replace original invocation")
			command.Origin = &meta.Origin
			_, err = store.RecordUnstartedExecutionTerminal(t.Context(), command)
			require.NoError(t, err)
			selected, err := store.LoadRun(t.Context(), predecessor.RunID)
			require.NoError(t, err)
			assert.Nil(t, selected.SuspensionResolution)
			assert.Equal(t, session.RunStatusSuspended, selected.Status)
			other := earlyTerminalCommand(competitor)
			other.Origin = &meta.Origin
			_, err = store.RecordUnstartedExecutionTerminal(t.Context(), other)
			require.NoError(t, err)
			_, err = store.LoadRun(t.Context(), successor.RunID)
			assert.ErrorIs(t, err, session.ErrRunNotFound) //nolint:testifylint // The record check must still verify that the terminal-only successor saved no Run records.
			assert.Empty(t, store.records[successor.RunID])
		})
	}
}

func TestUnstartedServiceContinuationPreservesActualStart(t *testing.T) {
	for _, sessionID := range []string{"session", ""} {
		t.Run("session="+sessionID, func(t *testing.T) {
			store, root := activeRunStore(t)
			root.SessionID = sessionID
			root.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "registry", Tool: "records.find"})}
			prepare := func(id, predecessor string) session.RunStart {
				d := storage.SeedDeclaration{Origin: rootExecutionOrigin(root.RunID), Subject: root.Subject, RunID: id, SessionID: sessionID, CommandID: id, AttemptID: id, Kind: storage.SeedLiteral, PredecessorRunID: predecessor}
				bindSeedTestOperation(t, store, d)
				_, err := store.BeginRunSeed(t.Context(), d)
				require.NoError(t, err)
				require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, d, storage.EmptySeedEndID)))
				value := root
				value.RunID, value.PredecessorRunID, value.SeedEndID = id, predecessor, storage.EmptySeedEndID
				return value
			}
			start := func(value session.RunStart) error {
				command := rootStartCommand(t, value)
				if sessionID == "" {
					_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart(command))
					return err
				}
				_, err := store.StartRootRun(t.Context(), command)
				return err
			}
			require.NoError(t, start(prepare(root.RunID, "")))
			closeReplayRun(t, store, root, session.RunStatusSuspended)
			terminalStart, runningStart := prepare("early-successor", root.RunID), prepare("running-successor", root.RunID)
			terminal := earlyTerminalCommand(terminalStart)
			origin := rootExecutionOrigin(root.RunID)
			terminal.Origin = &origin
			ready := make(chan struct{})
			results := make(chan error, 2)
			go func() {
				<-ready
				_, err := store.RecordUnstartedExecutionTerminal(t.Context(), terminal)
				results <- err
			}()
			go func() {
				<-ready
				results <- start(runningStart)
			}()
			close(ready)
			first, second := <-results, <-results
			require.NoError(t, first)
			require.NoError(t, second)
			selected, err := store.LoadRun(t.Context(), root.RunID)
			require.NoError(t, err)
			resolution := selected.SuspensionResolution.(session.ContinuedSuspension)
			assert.Equal(t, runningStart.RunID, resolution.RunID)
			assert.Equal(t, origin, selected.Origin)
			_, err = store.LoadRun(t.Context(), terminalStart.RunID)
			assert.ErrorIs(t, err, session.ErrRunNotFound) //nolint:testifylint // Record and terminal checks independently verify the successor selected by the race.
			assert.Empty(t, store.records[terminalStart.RunID])
			_, stored := store.unstartedTerminals[terminalStart.RunID]
			assert.True(t, stored)
		})
	}
}

func earlyTerminalCommand(start session.RunStart) genrunterminal.UnstartedExecutionTerminal {
	digest := [32]byte{1}
	origin := rootExecutionOrigin(start.RunID)
	command := genrunterminal.UnstartedExecutionTerminal{
		RunID: start.RunID, Subject: &start.Subject, RequestDigest: digest[:], Origin: &origin,
		Time:    genrunterminal.NewExecutionTerminalTimeWorkflowDecision("2026-01-02T03:04:05Z"),
		Outcome: genrunterminal.NewUnstartedExecutionOutcomeCanceled(run.CancellationReasonEngineCanceled),
	}
	if start.SessionID != "" {
		command.SessionID = &start.SessionID
	}
	return command
}
