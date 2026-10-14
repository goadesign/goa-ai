// Service continuation tests keep empty initial history while checking that
// only one successor can claim a suspended invocation in either session scope.
package inmem

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestServiceContinuationHasOneSuccessor(t *testing.T) {
	for _, sessionID := range []string{"session", ""} {
		t.Run("session="+sessionID, func(t *testing.T) {
			store := New()
			at := time.Now().UTC().Truncate(time.Millisecond)
			if sessionID != "" {
				_, err := store.CreateSession(t.Context(), sessionID, at)
				require.NoError(t, err)
			}
			subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorServiceTool(&genrunidentity.ServiceToolSubject{Registry: "registry", Tool: "records.find"})}
			prepare := func(id, predecessor string) session.RunStart {
				d := storage.SeedDeclaration{Origin: rootExecutionOrigin("root"), Subject: subject, RunID: id, SessionID: sessionID, CommandID: id, AttemptID: id, Kind: storage.SeedLiteral, PredecessorRunID: predecessor}
				bindSeedTestOperation(t, store, d)
				_, err := store.BeginRunSeed(t.Context(), d)
				require.NoError(t, err)
				require.NoError(t, store.PublishRunSeed(t.Context(), appendSeedTestCompletion(t, store, d, storage.EmptySeedEndID)))
				return session.RunStart{Subject: subject, RunID: id, SessionID: sessionID, PredecessorRunID: predecessor, SeedEndID: storage.EmptySeedEndID, StartedAt: at}
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
			require.NoError(t, start(prepare("root", "")))
			root := store.runs["root"]
			root.Status = session.RunStatusSuspended
			store.runs["root"] = root
			first, second := prepare("first", "root"), prepare("second", "root")
			results := make(chan error, 2)
			go func() { results <- start(first) }()
			go func() { results <- start(second) }()
			a, b := <-results, <-results
			assert.NotEqual(t, a == nil, b == nil)
			stored, err := store.LoadRun(t.Context(), "root")
			require.NoError(t, err)
			assert.Contains(t, []string{"first", "second"}, stored.SuccessorRunID)
			winner, err := store.LoadRun(t.Context(), stored.SuccessorRunID)
			require.NoError(t, err)
			assert.Equal(t, root.Origin, winner.Origin)
			assert.Equal(t, sessionID, winner.SessionID)
		})
	}
}
