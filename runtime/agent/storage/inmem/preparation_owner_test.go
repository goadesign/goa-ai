// Preparation ownership is fixed before provider resolution. Closing an upload
// attempt cannot choose another invocation's owner or reopen that attempt.
package inmem

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestPreparationOwnerRejectsRootChildCollision(t *testing.T) {
	root := rootExecutionOrigin("destination")
	child := genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: "issuer", ToolCallID: "call"})}
	for _, firstRoot := range []bool{true, false} {
		t.Run(map[bool]string{true: "root first", false: "child first"}[firstRoot], func(t *testing.T) {
			store := New()
			operation := storage.PreparationOperation{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, RunID: "destination", CommandID: "command"}
			first, second := root, child
			if !firstRoot {
				first, second = child, root
			}
			accepted := storage.RunPreparationBegin{Operation: operation, Origin: first}
			decision, err := store.BeginRunPreparationOperation(t.Context(), accepted)
			require.NoError(t, err)
			_, proceed := decision.State.AsProceed()
			assert.True(t, proceed)
			retry, err := store.BeginRunPreparationOperation(t.Context(), accepted)
			require.NoError(t, err)
			assert.Equal(t, decision, retry)
			_, err = store.BeginRunPreparationOperation(t.Context(), storage.RunPreparationBegin{Operation: operation, Origin: second})
			require.ErrorIs(t, err, storage.ErrSeedConflict)
			changed := accepted
			changed.Operation.CommandID = "other"
			_, err = store.BeginRunPreparationOperation(t.Context(), changed)
			require.ErrorIs(t, err, storage.ErrSeedConflict)
			declaration := storage.SeedDeclaration{Subject: operation.Subject, RunID: operation.RunID, CommandID: operation.CommandID, AttemptID: "attempt", Kind: storage.SeedLiteral}
			seed, err := store.BeginRunSeed(t.Context(), declaration)
			require.NoError(t, err)
			assert.Equal(t, first, seed.Origin)
			retrySeed, err := store.BeginRunSeed(t.Context(), declaration)
			require.NoError(t, err)
			assert.Equal(t, first, retrySeed.Origin)
		})
	}
}

func TestPreparationSettlementBeforeBeginDoesNotChooseOwner(t *testing.T) {
	store := New()
	operation := storage.PreparationOperation{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("agent")}, RunID: "destination", CommandID: "command"}
	_, found, err := store.SettleRunPreparation(t.Context(), storage.PreparationAttempt{Operation: operation, AttemptID: "closed"})
	require.NoError(t, err)
	assert.False(t, found)
	require.Empty(t, store.preparationOperations)
	declaration := storage.SeedDeclaration{Subject: operation.Subject, RunID: operation.RunID, CommandID: operation.CommandID, AttemptID: "fresh", Kind: storage.SeedLiteral}
	_, err = store.BeginRunSeed(t.Context(), declaration)
	require.ErrorIs(t, err, storage.ErrSeedConflict)
	decision, err := store.BeginRunPreparationOperation(t.Context(), storage.RunPreparationBegin{Operation: operation, Origin: rootExecutionOrigin(operation.RunID)})
	require.NoError(t, err)
	_, proceed := decision.State.AsProceed()
	assert.True(t, proceed)
	declaration.AttemptID = "closed"
	_, err = store.BeginRunSeed(t.Context(), declaration)
	require.ErrorIs(t, err, storage.ErrPreparationAbandoned)
	declaration.AttemptID = "fresh"
	_, err = store.BeginRunSeed(t.Context(), declaration)
	require.NoError(t, err)
	require.Len(t, store.preparationOperations, 1)
}

// Large invocation identifiers belong to operation binding, not to each bounded
// history write. Every read must still return the original, independently owned value.
func TestSeedOriginComesFromPreparationBinding(t *testing.T) {
	for _, size := range []int{1_300_000, 4 * 1024 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store := New()
			operation := storage.PreparationOperation{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("worker")}, RunID: "child", CommandID: "command"}
			origin := genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: "original-parent", ToolCallID: strings.Repeat("x", size)})}
			_, err := store.BeginRunPreparationOperation(t.Context(), storage.RunPreparationBegin{Operation: operation, Origin: origin})
			require.NoError(t, err)
			declaration := storage.SeedDeclaration{Subject: operation.Subject, RunID: operation.RunID, CommandID: operation.CommandID, AttemptID: "attempt", Kind: storage.SeedLiteral}
			require.NoError(t, storage.ValidateSeedCommandSize(declaration))
			seed, err := store.BeginRunSeed(t.Context(), declaration)
			require.NoError(t, err)
			assert.Equal(t, origin, seed.Origin)
			returned, ok := seed.Origin.Invocation.AsTool()
			require.True(t, ok)
			returned.RunID = "changed-parent"
			retry, err := store.BeginRunSeed(t.Context(), declaration)
			require.NoError(t, err)
			assert.Equal(t, origin, retry.Origin)
			require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, declaration, storage.EmptySeedEndID)))
			loaded, err := store.LoadRunSeed(t.Context(), operation.RunID, storage.EmptySeedEndID)
			require.NoError(t, err)
			assert.Equal(t, origin, loaded.Origin)
			recovered, found, err := store.FindRunPreparation(t.Context(), operation)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, origin, recovered.Seed.Origin)
			changed := declaration
			changed.CommandID = "another-command"
			_, err = store.BeginRunSeed(t.Context(), changed)
			assert.ErrorIs(t, err, storage.ErrSeedConflict)
		})
	}
}

func TestLargeSeedOriginSurvivesChildStartAndEarlyTerminal(t *testing.T) {
	for _, size := range []int{1_300_000, 4 * 1024 * 1024} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			store := New()
			parent := publishStartHistory(t, store, session.RunStart{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("parent")}, RunID: "original-parent", StartedAt: time.Now().UTC().Truncate(time.Millisecond)}, "")
			_, err := store.StartOneShotRun(t.Context(), storage.OneShotRunStart(rootStartCommand(t, parent)))
			require.NoError(t, err)
			callID := strings.Repeat("x", size)
			child := oneShotChildContinuationCommand(t, store, parent, "child", "", callID)
			seed, err := store.LoadRunSeed(t.Context(), child.Run.RunID, child.Run.SeedEndID)
			require.NoError(t, err)
			_, err = store.StartOneShotChildRun(t.Context(), child)
			require.NoError(t, err)
			started, err := store.LoadRun(t.Context(), child.Run.RunID)
			require.NoError(t, err)
			assert.Equal(t, seed.Origin, started.Origin)
			invocation, ok := started.Origin.Invocation.AsTool()
			require.True(t, ok)
			assert.Equal(t, callID, invocation.ToolCallID)
			assert.Equal(t, parent.RunID, invocation.RunID)

			unstarted := oneShotChildContinuationCommand(t, store, parent, "unstarted", "", callID)
			seed, err = store.LoadRunSeed(t.Context(), unstarted.Run.RunID, unstarted.Run.SeedEndID)
			require.NoError(t, err)
			terminal := earlyTerminalCommand(unstarted.Run)
			terminal.Origin = &seed.Origin
			result, err := store.RecordUnstartedExecutionTerminal(t.Context(), terminal)
			require.NoError(t, err)
			recorded, ok := result.Result.AsStored()
			require.True(t, ok)
			assert.Equal(t, seed.Origin, *recorded.Origin)
			replay, err := store.RecordUnstartedExecutionTerminal(t.Context(), terminal)
			require.NoError(t, err)
			original, ok := replay.Result.AsReplayed()
			require.True(t, ok)
			assert.Equal(t, *recorded, *original)
		})
	}
}
