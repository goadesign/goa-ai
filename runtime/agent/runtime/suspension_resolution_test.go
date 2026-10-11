// These tests cancel supported empty suspensions without requiring the original
// execution preparation. Unknown checkpoints remain rejected and historical
// records remain byte-for-byte intact.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/mcp"
)

// continuationBeforeClosureStore admits a real answer after cancellation reads
// the old checkpoint, before storage chooses its exclusive resolution.
type (
	continuationBeforeClosureStore struct {
		storage.Store
		before func()
	}
)

func TestCancelRunRelinquishesEmptyRootWithoutExecutionPreparation(t *testing.T) {
	store := newTestStore()
	admitRunForTest(t, store, session.RunMeta{AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning})
	saved := suspensionContractFixture(t, "svc.lookup")
	data, err := json.Marshal(saved)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), store, "run-1", session.RunSuspension{ID: saved.ID, Data: data}))
	before, err := store.ListRunRecords(t.Context(), "run-1", "", 100)
	require.NoError(t, err)
	backend := &recordingCancellationEngine{Engine: engineinmem.New(), err: engine.ErrWorkflowNotFound}
	runtime := New(store, WithEngine(backend))
	request := CancelRequest{RunID: "run-1", Reason: run.CancellationReasonUserRequested}
	require.NoError(t, runtime.CancelRun(t.Context(), request))
	meta, err := store.LoadRun(t.Context(), request.RunID)
	require.NoError(t, err)
	resolution, ok := meta.SuspensionResolution.(session.RelinquishedSuspension)
	require.True(t, ok)
	assert.Equal(t, request.Reason, resolution.Reason)
	assert.Equal(t, session.RunStatusSuspended, meta.Status)
	assert.Empty(t, meta.CancellationReason)
	require.NoError(t, runtime.CancelRun(t.Context(), request))
	retried, err := store.LoadRun(t.Context(), request.RunID)
	require.NoError(t, err)
	assert.Equal(t, meta, retried)
	request.Reason = run.CancellationReasonSessionEnded
	require.NoError(t, runtime.CancelRun(t.Context(), request))
	settled, err := store.LoadRun(t.Context(), request.RunID)
	require.NoError(t, err)
	assert.Equal(t, meta, settled)
	after, err := store.ListRunRecords(t.Context(), "run-1", "", 100)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	checkpoint, err := store.LoadRunSuspension(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(data), sha256.Sum256(checkpoint.Data))
	loaded, err := runtime.LoadRunSuspension(t.Context(), "run-1")
	require.NoError(t, err)
	assert.Equal(t, saved, loaded)
}

func TestCancelRunDoesNotRelinquishUnknownCheckpoint(t *testing.T) {
	for _, variation := range []string{"unknown version", "mismatched version", "unknown field", "wrong identity", "wrong digest"} {
		t.Run(variation, func(t *testing.T) {
			saved := suspensionContractFixture(t, "svc.lookup")
			switch variation {
			case "unknown version":
				saved.Version = "unsupported"
			case "mismatched version":
				rewriteSuspensionCheckpoint(t, saved, func(value *workflowCheckpoint) { value.Version = "unsupported" })
			case "unknown field":
				saved.Checkpoint = append(saved.Checkpoint[:len(saved.Checkpoint)-1], []byte(`,"OtherWork":true}`)...)
			case "wrong identity":
				rewriteSuspensionCheckpoint(t, saved, func(value *workflowCheckpoint) { value.PreviousRunID = "another-run" })
			case "wrong digest":
				saved.ID = "incorrect"
			}
			data, err := json.Marshal(saved)
			require.NoError(t, err)
			store := newTestStore()
			admitRunForTest(t, store, session.RunMeta{AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusRunning})
			require.NoError(t, storeSuspensionForTest(t.Context(), store, "run-1", session.RunSuspension{ID: saved.ID, Data: data}))
			backend := &recordingCancellationEngine{Engine: engineinmem.New(), err: engine.ErrWorkflowCompleted}
			runtime := New(store, WithEngine(backend))
			err = runtime.CancelRun(t.Context(), CancelRequest{RunID: "run-1", Reason: run.CancellationReasonUserRequested})
			require.ErrorIs(t, err, ErrRunSuspensionCorrupt)
			meta, err := store.LoadRun(t.Context(), "run-1")
			require.NoError(t, err)
			assert.Nil(t, meta.SuspensionResolution)
		})
	}
}

func TestEmptyRootProofLeavesSavedWorkOnCleanupPath(t *testing.T) {
	for _, variation := range []string{"task", "child record", "child input", "remote input"} {
		t.Run(variation, func(t *testing.T) {
			saved := suspensionContractFixture(t, "svc.lookup")
			rewriteSuspensionCheckpoint(t, saved, func(value *workflowCheckpoint) {
				// These fields are the saved-work inputs the cleanup loop owns.
				// The private state decoder validates their encoding; execution
				// additionally validates nested tools on the existing cleanup path.
				switch variation {
				case "task":
					value.Batch.Records = []checkpointToolRecord{{Task: &taskExecution{TaskID: ""}}}
				case "child record":
					value.Batch.Records = []checkpointToolRecord{{ChildSuspension: &api.RunSuspension{ID: "child"}}}
				case "child input":
					value.Pending[0].Child = &checkpointChildContinuation{Suspension: &api.RunSuspension{ID: "child"}}
				case "remote input":
					state := "accepted-input"
					value.Batch.Records = []checkpointToolRecord{{MCPPending: mustPendingInput(t, &mcp.InputRequired{RequestState: &state})}}
				}
			})
			data, err := json.Marshal(saved)
			require.NoError(t, err)
			meta := session.RunMeta{AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1", Status: session.RunStatusSuspended}
			store := suspensionReadStore{run: meta, suspension: session.RunSuspension{ID: saved.ID, Data: data}}
			runtime := New(store)
			settled, err := runtime.relinquishEmptyRootSuspension(t.Context(), CancelRequest{RunID: meta.RunID, Reason: run.CancellationReasonUserRequested}, &meta)
			assert.False(t, settled)
			if variation == "child input" {
				assert.Error(t, err, "inconsistent public pending input must reject before mutation")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCancelRunFollowsAnswerThatWinsClosureRace(t *testing.T) {
	for _, order := range []string{"before cancellation", "during closure"} {
		t.Run(order, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			stored := newTestStore()
			store := &continuationBeforeClosureStore{Store: stored}
			runtime := New(store, WithEngine(engineinmem.New()))
			var resumed atomic.Int32
			question := func() *planner.PlanResult {
				return &planner.PlanResult{Await: planner.NewAwait(planner.AwaitClarificationItem(&planner.AwaitClarification{ID: "question", Question: "Choose a region"}))}
			}
			definition := testAgentDefinition("race.agent", "race.workflow", "race.queue", nil, nil)
			require.NoError(t, runtime.RegisterAgent(ctx, AgentRegistration{
				Definition: definition, WorkflowHandler: runtime.ExecuteWorkflow,
				PlanActivityName: "race.plan", ResumeActivityName: "race.resume", ExecuteToolActivity: "race.execute",
				Planner: &stubPlanner{
					start: func(ctx context.Context, _ *planner.PlanInput) (*planner.PlanResult, error) {
						// This accepted Store state exercises historical cancellation intent;
						// it does not simulate the current workflow's cancellation handler.
						input, err := buildCancellationRecord(&RecordActivityInput{AgentID: "race.agent", RunID: "race-original", SessionID: "race-session", TimestampMS: time.Now().UnixMilli()}, run.CancellationReasonUserRequested)
						if err != nil {
							return nil, err
						}
						_, err = stored.RecordRunCancellation(ctx, storage.RunCancellation{RunID: "race-original", Reason: run.CancellationReasonUserRequested, Record: runLogEvent(input, input.Payload, input.TimestampMS)})
						return question(), err
					},
					resume: func(context.Context, *planner.PlanResumeInput) (*planner.PlanResult, error) {
						resumed.Add(1)
						return question(), nil
					},
				},
			}))
			_, err := stored.CreateSession(ctx, "race-session", time.Now().UTC())
			require.NoError(t, err)
			client := runtime.MustClient("race.agent")
			first, err := client.Run(ctx, "race-session", nil, WithRunID("race-original"))
			require.NoError(t, err)
			require.NotNil(t, first.Suspension)
			store.before = func() {
				answered, err := client.Continue(ctx, "race-session", "race-original", "race-answer", "race-turn",
					&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: "question", Answer: "west"}}, WorkflowOptions{})
				require.NoError(t, err)
				require.NotNil(t, answered.Suspension)
			}
			if order == "before cancellation" {
				before := store.before
				store.before = nil
				before()
			}
			require.NoError(t, runtime.CancelRun(ctx, CancelRequest{RunID: "race-original", Reason: run.CancellationReasonSessionEnded}))
			require.Eventually(t, func() bool {
				meta, err := stored.LoadRun(ctx, "race-answer")
				if err != nil {
					return false
				}
				_, closed := meta.SuspensionResolution.(session.RelinquishedSuspension)
				return closed
			}, 5*time.Second, 10*time.Millisecond)
			original, err := stored.LoadRun(ctx, "race-original")
			require.NoError(t, err)
			assert.Equal(t, session.ContinuedSuspension{RunID: "race-answer"}, original.SuspensionResolution)
			answer, err := stored.LoadRun(ctx, "race-answer")
			require.NoError(t, err)
			assert.Equal(t, run.CancellationReasonUserRequested, original.CancellationReason)
			assert.Equal(t, run.CancellationReasonSessionEnded, answer.SuspensionResolution.(session.RelinquishedSuspension).Reason)
			assert.Equal(t, session.RunStatusSuspended, answer.Status)
			assert.EqualValues(t, 1, resumed.Load())
		})
	}
}

func TestCancelRunAlreadyRelinquishedDoesNotDecodeHistoricalCheckpoint(t *testing.T) {
	resolved := session.RelinquishedSuspension{Reason: run.CancellationReasonUserRequested, ResolvedAt: time.Unix(123, 0).UTC()}
	store := suspensionReadStore{
		run:        session.RunMeta{AgentID: "history.agent", RunID: "history-run", SessionID: "history-session", Status: session.RunStatusSuspended, SuspensionResolution: resolved},
		suspension: session.RunSuspension{ID: "historical", Data: []byte(`{"version":"unsupported"}`)},
	}
	backend := &recordingCancellationEngine{Engine: engineinmem.New(), err: engine.ErrWorkflowNotFound}
	runtime := New(store, WithEngine(backend))
	require.NoError(t, runtime.CancelRun(t.Context(), CancelRequest{RunID: store.run.RunID, Reason: run.CancellationReasonSessionEnded}))
	assert.Equal(t, resolved, store.run.SuspensionResolution)
}

func (s *continuationBeforeClosureStore) RelinquishRunSuspension(ctx context.Context, command storage.RunSuspensionRelinquishment) (session.SuspensionResolution, error) {
	if s.before != nil {
		before := s.before
		s.before = nil
		before()
	}
	return s.Store.RelinquishRunSuspension(ctx, command)
}
