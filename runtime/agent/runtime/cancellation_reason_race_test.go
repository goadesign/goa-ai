// These tests interpose another owner's cancellation after the caller has read
// a suspension. The runtime may acknowledge an observed permanent closure but
// must not mistake cancellation intent or an unavailable read for completion.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"testing"
	"time"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// closureObservationStore counts the caller's operations separately from
	// the competing owner and can return one controlled post-conflict read.
	closureObservationStore struct {
		storage.Store
		before           func()
		reread           func(session.RunMeta) (session.RunMeta, error)
		mutationErr      error
		runReads         []string
		checkpointReads  int
		mutations        int
		preparationReads int
	}

	// closureObservationEngine records any unexpected cleanup job while using
	// the normal closed-engine response to enter suspension cancellation.
	closureObservationEngine struct {
		recordingCancellationEngine
		jobs int
	}
)

func TestCancelRunCompetingReasonsPreserveWinningResolution(t *testing.T) {
	for _, winnerReason := range []string{run.CancellationReasonUserRequested, run.CancellationReasonSessionEnded} {
		t.Run(winnerReason, func(t *testing.T) {
			requestReason := run.CancellationReasonSessionEnded
			if winnerReason == requestReason {
				requestReason = run.CancellationReasonUserRequested
			}
			stored, checkpoint := cancellationRaceFixture(t, "")
			before, err := stored.ListRunRecords(t.Context(), "run-1", "", 100)
			require.NoError(t, err)
			var winner session.RunMeta
			store := &closureObservationStore{Store: stored}
			store.before = func() {
				winnerRuntime := New(stored, WithEngine(&recordingCancellationEngine{Engine: engineinmem.New(), err: engine.ErrWorkflowCompleted}))
				require.NoError(t, winnerRuntime.CancelRun(t.Context(), CancelRequest{RunID: "run-1", Reason: winnerReason}))
				var err error
				winner, err = stored.LoadRun(t.Context(), "run-1")
				require.NoError(t, err)
			}
			backend := &closureObservationEngine{recordingCancellationEngine: recordingCancellationEngine{Engine: engineinmem.New(), err: engine.ErrWorkflowCompleted}}
			runtime := New(store, WithEngine(backend))
			require.NoError(t, runtime.CancelRun(t.Context(), CancelRequest{RunID: "run-1", Reason: requestReason}))
			assert.Equal(t, []string{"run-1", "run-1"}, store.runReads)
			assert.Equal(t, 1, store.checkpointReads)
			assert.Equal(t, 1, store.mutations)
			assert.Zero(t, store.preparationReads)
			assert.Zero(t, backend.jobs)
			after, err := stored.LoadRun(t.Context(), "run-1")
			require.NoError(t, err)
			assert.Equal(t, winner, after)
			assert.Equal(t, session.RunStatusSuspended, after.Status)
			assert.Equal(t, winnerReason, after.SuspensionResolution.(session.RelinquishedSuspension).Reason)
			records, err := stored.ListRunRecords(t.Context(), "run-1", "", 100)
			require.NoError(t, err)
			assert.Equal(t, before, records)
			retained, err := stored.LoadRunSuspension(t.Context(), "run-1")
			require.NoError(t, err)
			assert.Equal(t, sha256.Sum256(checkpoint.Data), sha256.Sum256(retained.Data))
		})
	}
}

func TestCancelRunReasonConflictDoesNotTreatIntentAsClosure(t *testing.T) {
	stored, _ := cancellationRaceFixture(t, run.CancellationReasonUserRequested)
	store := &closureObservationStore{Store: stored}
	backend := &closureObservationEngine{recordingCancellationEngine: recordingCancellationEngine{Engine: engineinmem.New(), err: engine.ErrWorkflowCompleted}}
	runtime := New(store, WithEngine(backend))
	var conflict *CancellationReasonConflictError
	require.ErrorAs(t, runtime.CancelRun(t.Context(), CancelRequest{RunID: "run-1", Reason: run.CancellationReasonSessionEnded}), &conflict)
	assert.Equal(t, []string{"run-1", "run-1"}, store.runReads)
	assert.Equal(t, 1, store.checkpointReads)
	assert.Equal(t, 1, store.mutations)
	assert.Zero(t, store.preparationReads)
	assert.Zero(t, backend.jobs)
	meta, err := stored.LoadRun(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, run.CancellationReasonUserRequested, meta.CancellationReason)
	assert.Nil(t, meta.SuspensionResolution)
}

func TestCancellationWorkflowUnresolvedReasonConflictIsPermanent(t *testing.T) {
	unavailable := errors.New("owner read temporarily unavailable")
	for _, failRead := range []bool{false, true} {
		t.Run(map[bool]string{false: "permanent conflict", true: "temporary owner read"}[failRead], func(t *testing.T) {
			stored, checkpoint := cancellationRaceFixture(t, run.CancellationReasonUserRequested)
			before, err := stored.LoadRun(t.Context(), "run-1")
			require.NoError(t, err)
			records, err := stored.ListRunRecords(t.Context(), "run-1", "", 100)
			require.NoError(t, err)
			store := &closureObservationStore{Store: stored}
			if failRead {
				store.reread = func(session.RunMeta) (session.RunMeta, error) {
					return session.RunMeta{}, unavailable
				}
			}
			runtime := New(store)
			settled, err := runtime.cancellationActivity(t.Context(), engine.CancellationRequest{RunID: "run-1", Reason: run.CancellationReasonSessionEnded})
			assert.False(t, settled)
			if failRead {
				require.ErrorIs(t, err, unavailable)
				assert.False(t, engine.IsActivityErrorNonRetryable(err))
			} else {
				var conflict *CancellationReasonConflictError
				require.ErrorAs(t, err, &conflict)
				assert.Equal(t, "run-1", conflict.RunID)
				assert.Equal(t, run.CancellationReasonSessionEnded, conflict.Reason)
				assert.True(t, engine.IsActivityErrorNonRetryable(err))
			}
			assert.Equal(t, []string{"run-1", "run-1"}, store.runReads)
			assert.Equal(t, 1, store.checkpointReads)
			assert.Equal(t, 1, store.mutations)
			assert.Zero(t, store.preparationReads)
			after, err := stored.LoadRun(t.Context(), "run-1")
			require.NoError(t, err)
			assert.Equal(t, before, after)
			retained, err := stored.ListRunRecords(t.Context(), "run-1", "", 100)
			require.NoError(t, err)
			assert.Equal(t, records, retained)
			saved, err := stored.LoadRunSuspension(t.Context(), "run-1")
			require.NoError(t, err)
			assert.Equal(t, sha256.Sum256(checkpoint.Data), sha256.Sum256(saved.Data))
		})
	}
}

func TestCancelRunReasonConflictReadFollowsSuccessor(t *testing.T) {
	stored, _ := cancellationRaceFixture(t, run.CancellationReasonUserRequested)
	store := &closureObservationStore{Store: stored, mutationErr: storage.NewContractError(session.ErrRunCancellationConflict)}
	store.before = func() {
		admitContinuedRunForTest(t, stored, session.RunMeta{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}, RunID: "successor", SessionID: "session-1", Status: session.RunStatusRunning}, "run-1")
	}
	runtime := New(store)
	target, err := store.LoadRun(t.Context(), "run-1")
	require.NoError(t, err)
	settled, err := runtime.relinquishEmptyRootSuspension(t.Context(), CancelRequest{RunID: "run-1", Reason: run.CancellationReasonSessionEnded}, &target)
	require.NoError(t, err)
	assert.False(t, settled)
	assert.Equal(t, session.ContinuedSuspension{RunID: "successor"}, target.SuspensionResolution)
	assert.Equal(t, []string{"run-1", "run-1"}, store.runReads)
	assert.Equal(t, 1, store.checkpointReads)
	assert.Equal(t, 1, store.mutations)
	assert.Zero(t, store.preparationReads)
	owner, err := runtime.cancellationRoot(t.Context(), target)
	require.NoError(t, err)
	assert.Equal(t, "successor", owner.RunID)
}

func TestCancellationSuccessorOwnsReasonAcceptance(t *testing.T) {
	for _, successorReason := range []string{"", run.CancellationReasonUserRequested} {
		t.Run("successor reason="+successorReason, func(t *testing.T) {
			stored, _ := cancellationRaceFixture(t, run.CancellationReasonUserRequested)
			admitContinuedRunForTest(t, stored, session.RunMeta{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}, RunID: "successor", SessionID: "session-1", Status: session.RunStatusRunning}, "run-1")
			backend := &recordingCancellationEngine{Engine: engineinmem.New()}
			if successorReason != "" {
				input, err := buildCancellationRecord(&RecordActivityInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}, RunID: "successor", SessionID: "session-1", TimestampMS: time.Now().UnixMilli()}, successorReason)
				require.NoError(t, err)
				_, err = stored.RecordRunCancellation(t.Context(), storage.RunCancellation{RunID: "successor", Reason: successorReason, Record: runLogEvent(input, input.Payload, input.TimestampMS)})
				require.NoError(t, err)
				backend.err = &engine.CancellationConflictError{RunID: "successor", Reason: run.CancellationReasonSessionEnded}
			}
			runtime := New(stored, WithEngine(backend))
			settled, err := runtime.cancellationActivity(t.Context(), engine.CancellationRequest{RunID: "run-1", Reason: run.CancellationReasonSessionEnded})
			assert.False(t, settled)
			if successorReason == "" {
				require.NoError(t, err)
			} else {
				var conflict *engine.CancellationConflictError
				require.ErrorAs(t, err, &conflict)
				assert.Equal(t, "successor", conflict.RunID)
				assert.True(t, engine.IsActivityErrorNonRetryable(err))
			}
			assert.Equal(t, []engine.CancellationRequest{{RunID: "successor", Reason: run.CancellationReasonSessionEnded}}, backend.requests)
			meta, err := stored.LoadRun(t.Context(), "run-1")
			require.NoError(t, err)
			assert.Equal(t, run.CancellationReasonUserRequested, meta.CancellationReason)
			successor, err := stored.LoadRun(t.Context(), "successor")
			require.NoError(t, err)
			assert.Equal(t, successorReason, successor.CancellationReason)
		})
	}
}

func TestCancelRunReasonConflictRequiresValidOwnerRead(t *testing.T) {
	unavailable := errors.New("owner read unavailable")
	for _, variation := range []string{"read failure", "missing run", "run", "agent", "session", "parent", "status", "invalid resolution"} {
		t.Run(variation, func(t *testing.T) {
			stored, _ := cancellationRaceFixture(t, "")
			store := &closureObservationStore{Store: stored, mutationErr: storage.NewContractError(session.ErrRunCancellationConflict)}
			store.reread = func(meta session.RunMeta) (session.RunMeta, error) {
				resolved := session.RelinquishedSuspension{Reason: run.CancellationReasonUserRequested, ResolvedAt: time.Unix(123, 0).UTC()}
				meta.SuspensionResolution = resolved
				switch variation {
				case "read failure":
					return session.RunMeta{}, unavailable
				case "missing run":
					return session.RunMeta{}, session.ErrRunNotFound
				case "run":
					meta.RunID = "different"
				case "agent":
					meta.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("different"))}
				case "session":
					meta.SessionID = "different"
				case "parent":
					meta.ParentRunID = "parent"
				case "status":
					meta.Status = session.RunStatusRunning
				case "invalid resolution":
					meta.SuspensionResolution = &resolved
				}
				return meta, nil
			}
			backend := &closureObservationEngine{recordingCancellationEngine: recordingCancellationEngine{Engine: engineinmem.New(), err: engine.ErrWorkflowCompleted}}
			runtime := New(store, WithEngine(backend))
			err := runtime.CancelRun(t.Context(), CancelRequest{RunID: "run-1", Reason: run.CancellationReasonSessionEnded})
			switch variation {
			case "read failure":
				require.ErrorIs(t, err, unavailable)
				assert.False(t, engine.IsActivityErrorNonRetryable(classifyCancellationActivityError(err)))
			case "missing run":
				require.ErrorIs(t, err, session.ErrRunNotFound)
			default:
				var contract *storage.ContractError
				require.ErrorAs(t, err, &contract)
			}
			assert.Equal(t, []string{"run-1", "run-1"}, store.runReads)
			assert.Equal(t, 1, store.checkpointReads)
			assert.Equal(t, 1, store.mutations)
			assert.Zero(t, store.preparationReads)
			assert.Zero(t, backend.jobs)
		})
	}
}

func TestCancelRunUnrelatedMutationErrorsDoNotRereadOwner(t *testing.T) {
	for _, mutationErr := range []error{errors.New("temporary write failure"), storage.NewContractError(session.ErrRunSuspensionConflict), errors.New(session.ErrRunCancellationConflict.Error())} {
		t.Run(mutationErr.Error(), func(t *testing.T) {
			stored, _ := cancellationRaceFixture(t, "")
			store := &closureObservationStore{Store: stored, mutationErr: mutationErr}
			backend := &closureObservationEngine{recordingCancellationEngine: recordingCancellationEngine{Engine: engineinmem.New(), err: engine.ErrWorkflowCompleted}}
			runtime := New(store, WithEngine(backend))
			err := runtime.CancelRun(t.Context(), CancelRequest{RunID: "run-1", Reason: run.CancellationReasonSessionEnded})
			require.ErrorIs(t, err, mutationErr)
			assert.Equal(t, []string{"run-1"}, store.runReads)
			assert.Equal(t, 1, store.checkpointReads)
			assert.Equal(t, 1, store.mutations)
			assert.Zero(t, store.preparationReads)
			assert.Zero(t, backend.jobs)
		})
	}
}

// cancellationRaceFixture stores a real run, optional accepted cancellation
// intent, and its exact suspended record so conflict tests use owner state.
func cancellationRaceFixture(t *testing.T, priorReason string) (*testStore, session.RunSuspension) {
	t.Helper()
	stored := newTestStore()
	admitRunForTest(t, stored, session.RunMeta{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}, RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning})
	if priorReason != "" {
		input, err := buildCancellationRecord(&RecordActivityInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("svc.agent"))}, RunID: "run-1", SessionID: "session-1", TimestampMS: time.Now().UnixMilli()}, priorReason)
		require.NoError(t, err)
		_, err = stored.RecordRunCancellation(t.Context(), storage.RunCancellation{RunID: "run-1", Reason: priorReason, Record: runLogEvent(input, input.Payload, input.TimestampMS)})
		require.NoError(t, err)
	}
	saved := suspensionContractFixture(t, "svc.lookup")
	data, err := json.Marshal(saved)
	require.NoError(t, err)
	checkpoint := session.RunSuspension{ID: saved.ID, Data: data}
	require.NoError(t, storeSuspensionForTest(t.Context(), stored, "run-1", checkpoint))
	return stored, checkpoint
}

func (s *closureObservationStore) LoadRun(ctx context.Context, runID string) (session.RunMeta, error) {
	s.runReads = append(s.runReads, runID)
	meta, err := s.Store.LoadRun(ctx, runID)
	if err != nil || len(s.runReads) == 1 || s.reread == nil {
		return meta, err
	}
	return s.reread(meta)
}

func (s *closureObservationStore) LoadRunSuspension(ctx context.Context, runID string) (session.RunSuspension, error) {
	s.checkpointReads++
	return s.Store.LoadRunSuspension(ctx, runID)
}

func (s *closureObservationStore) RelinquishRunSuspension(ctx context.Context, command storage.RunSuspensionRelinquishment) (session.SuspensionResolution, error) {
	s.mutations++
	if s.before != nil {
		s.before()
	}
	if s.mutationErr != nil {
		return nil, s.mutationErr
	}
	return s.Store.RelinquishRunSuspension(ctx, command)
}

func (s *closureObservationStore) LoadRunSeed(context.Context, string, string) (storage.RunSeed, error) {
	s.preparationReads++
	return storage.RunSeed{}, errors.New("unexpected cancellation preparation read")
}

func (e *closureObservationEngine) StartCancellationWorkflow(context.Context, string, string, engine.CancellationRequest) error {
	e.jobs++
	return errors.New("unexpected cancellation workflow")
}
