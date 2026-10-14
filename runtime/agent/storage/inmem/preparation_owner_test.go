// Preparation ownership is fixed before provider resolution. Closing an upload
// attempt cannot choose another invocation's owner or reopen that attempt.
package inmem

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
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
			declaration := storage.SeedDeclaration{Origin: first, Subject: operation.Subject, RunID: operation.RunID, CommandID: operation.CommandID, AttemptID: "attempt", Kind: storage.SeedLiteral}
			seed, err := store.BeginRunSeed(t.Context(), declaration)
			require.NoError(t, err)
			assert.Equal(t, first, seed.Declaration.Origin)
			declaration.Origin = second
			_, err = store.BeginRunSeed(t.Context(), declaration)
			require.ErrorIs(t, err, storage.ErrSeedConflict)
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
	declaration := storage.SeedDeclaration{Origin: rootExecutionOrigin(operation.RunID), Subject: operation.Subject, RunID: operation.RunID, CommandID: operation.CommandID, AttemptID: "fresh", Kind: storage.SeedLiteral}
	_, err = store.BeginRunSeed(t.Context(), declaration)
	require.ErrorIs(t, err, storage.ErrSeedConflict)
	decision, err := store.BeginRunPreparationOperation(t.Context(), storage.RunPreparationBegin{Operation: operation, Origin: declaration.Origin})
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
