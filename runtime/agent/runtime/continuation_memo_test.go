// These tests submit direct and registered successors with caller-owned workflow
// metadata. Saved requests must retain those exact bytes after a lost reply,
// reject changed metadata, and complete even when the workflow never creates a Run.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/protobuf/proto"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	continuationLostReplyEngine struct {
		stubEngine
	}
)

func TestToolContinuationMemoSurvivesLostReplyAndEarlyCompletion(t *testing.T) {
	for _, executor := range []string{"native", "service"} {
		for _, sessionID := range []string{"", "session-1"} {
			for _, canceled := range []bool{false, true} {
				name := executor + "/session=" + sessionID + "/failed"
				if canceled {
					name = executor + "/session=" + sessionID + "/canceled"
				}
				t.Run(name, func(t *testing.T) {
					store, predecessorID, response := continuationMemoPredecessor(t, executor, sessionID)
					memo := map[string]any{
						"workflow":  "successor",
						"binary":    []byte("retained bytes"),
						"number":    json.Number("9007199254740993"),
						"raw":       converter.NewRawValue(&commonpb.Payload{Metadata: map[string][]byte{"encoding": []byte("json/plain"), "custom": []byte("retained metadata")}, Data: []byte(`{"count":9007199254740993}`)}),
						"alias":     preparedMemoAlias("named value"),
						"structure": preparedMemoValue{Name: "structured value"},
						"message":   &commonpb.WorkflowExecution{WorkflowId: "workflow-1", RunId: "run-1"},
					}
					backend := &continuationLostReplyEngine{}
					runtime := New(store, WithEngine(backend))
					_, err := runtime.StartToolContinuation(t.Context(), predecessorID, "successor", response, memo)
					require.ErrorContains(t, err, "engine reply lost")
					first, err := startrecipe.SnapshotRequest(backend.last)
					require.NoError(t, err)
					for key, value := range memo {
						expected, err := workflowcodec.NewDataConverter().ToPayload(value)
						require.NoError(t, err)
						assert.True(t, proto.Equal(expected, startrecipe.MemoPayload(first.Request.Memo[key])), key)
					}
					restarted := New(store, WithEngine(backend))
					_, err = restarted.StartToolContinuation(t.Context(), predecessorID, "successor", response, memo)
					require.NoError(t, err)
					again, err := startrecipe.SnapshotRequest(backend.last)
					require.NoError(t, err)
					assert.Equal(t, first, again)
					accepted, found, err := store.FindRunPreparation(t.Context(), storage.PreparationOperation{Subject: first.Request.Input.Subject, SessionID: sessionID, RunID: "successor", CommandID: "successor"})
					require.NoError(t, err)
					require.True(t, found)
					beforeBytes, err := readPreparation(t.Context(), store, accepted)
					require.NoError(t, err)

					// Different successor IDs may select different workflow metadata.
					// This does not change the request already saved for the first ID.
					other, err := restarted.prepareOperationContinuationRequest(t.Context(), predecessorID, "independent-successor", &api.RunContinuationInput{Response: response}, map[string]any{"workflow": "independent"})
					require.NoError(t, err)
					assert.Equal(t, "independent", decodePreparedMemo[string](t, other.Memo, "workflow"))
					memo["workflow"] = "changed"
					_, err = restarted.StartToolContinuation(t.Context(), predecessorID, "successor", response, memo)
					require.ErrorIs(t, err, storage.ErrSeedConflict)
					memo["workflow"] = "successor"
					_, err = restarted.StartToolContinuation(t.Context(), predecessorID, "successor", response, nil)
					require.ErrorIs(t, err, storage.ErrSeedConflict)
					changed := &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{ID: "different-answer", Approved: false}}
					_, err = restarted.StartToolContinuation(t.Context(), predecessorID, "successor", changed, memo)
					require.ErrorIs(t, err, storage.ErrSeedConflict)
					assert.Equal(t, 2, backend.startCalls)
					afterBytes, err := readPreparation(t.Context(), store, accepted)
					require.NoError(t, err)
					assert.Equal(t, beforeBytes, afterBytes)

					completion := &completionOnlyEngine{completion: engine.RunCompletion{
						Status: engine.RunStatusFailed, StartRequestDigest: first.Digest,
						CompletedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), WorkflowError: errors.New("closed before workflow entry"),
					}}
					if canceled {
						completion.completion.Status = engine.RunStatusCanceled
						completion.completion.WorkflowError = context.Canceled
					}
					captured := &earlyTerminalCaptureStore{Store: store.Store}
					repair := New(captured, WithEngine(completion))
					for range 2 {
						require.NoError(t, repair.EnsureToolContinuationCompletion(t.Context(), predecessorID, "successor", response, memo))
					}
					memo["workflow"] = "changed"
					require.ErrorIs(t, repair.EnsureToolContinuationCompletion(t.Context(), predecessorID, "successor", response, memo), storage.ErrSeedConflict)
					assert.Zero(t, completion.starts)
					require.NotNil(t, captured.terminal)
					assert.Equal(t, first.Digest[:], captured.terminal.RequestDigest)
					assert.Equal(t, "successor", captured.terminal.RunID)
					if canceled {
						_, matches := captured.terminal.Outcome.AsCanceled()
						assert.True(t, matches)
					} else {
						_, matches := captured.terminal.Outcome.AsFailed()
						assert.True(t, matches)
					}
					_, err = store.LoadRun(t.Context(), "successor")
					assert.ErrorIs(t, err, session.ErrRunNotFound)
				})
			}
		}
	}
}

func TestToolContinuationNilMemoRecoversEmptyPublishedRequest(t *testing.T) {
	for _, executor := range []string{"native", "service"} {
		t.Run(executor, func(t *testing.T) {
			store, predecessorID, response := continuationMemoPredecessor(t, executor, "")
			runtime := New(store)
			request, err := runtime.prepareOperationContinuationRequest(t.Context(), predecessorID, "successor", &api.RunContinuationInput{Response: response}, nil)
			require.NoError(t, err)
			require.Empty(t, request.Memo)
			accepted, found, err := store.FindRunPreparation(t.Context(), storage.PreparationOperation{Subject: request.Input.Subject, SessionID: request.Input.SessionID, RunID: "successor", CommandID: "successor"})
			require.NoError(t, err)
			require.True(t, found)
			beforeBytes, err := readPreparation(t.Context(), store, accepted)
			require.NoError(t, err)
			restarted := New(store)
			replayed, err := restarted.prepareOperationContinuationRequest(t.Context(), predecessorID, "successor", &api.RunContinuationInput{Response: response}, nil)
			require.NoError(t, err)
			afterBytes, err := readPreparation(t.Context(), store, accepted)
			require.NoError(t, err)
			assert.Equal(t, beforeBytes, afterBytes)
			before, err := startrecipe.SnapshotRequest(request)
			require.NoError(t, err)
			after, err := startrecipe.SnapshotRequest(replayed)
			require.NoError(t, err)
			assert.Equal(t, before.Digest, after.Digest)
			_, err = restarted.prepareOperationContinuationRequest(t.Context(), predecessorID, "successor", &api.RunContinuationInput{Response: response}, map[string]any{"workflow": "added"})
			assert.ErrorIs(t, err, storage.ErrSeedConflict)
		})
	}
}

// continuationMemoPredecessor saves a suspended native or service operation and
// returns its answer so each test can prepare a successor without local providers.
func continuationMemoPredecessor(t *testing.T, executor, sessionID string) (*testStore, string, *api.PendingInputResponse) {
	t.Helper()
	store := newTestStore()
	if sessionID != "" {
		require.NoError(t, createPreparedRunSession(t.Context(), store))
	}
	if executor == "native" {
		suspension, definition := directConsumerSuspension(t)
		rewriteSuspensionCheckpointAndPublic(t, suspension, func(saved *workflowCheckpoint) {
			saved.SessionID = sessionID
		})
		admitRunForTest(t, store, session.RunMeta{
			Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(definition.route.ID))},
			RunID:   "run-1", SessionID: sessionID, Status: session.RunStatusRunning,
		})
		data, err := json.Marshal(suspension)
		require.NoError(t, err)
		require.NoError(t, storeSuspensionForTest(t.Context(), store, "run-1", session.RunSuspension{ID: suspension.ID, Data: data}))
		return store, "run-1", directConsumerResponse(rawjson.Message(`9007199254740993`))
	}
	resolution, invocation := registeredRootFixture()
	resolution.Toolset.Tools[0].ConsumerContract.Confirmation = testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)).Toolset.Tools[0].ConsumerContract.Confirmation
	runtime := New(store, WithEngine(engineinmem.New()), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
	request, err := runtime.prepareRegisteredRootRequest(sessionID, "registry", resolution, invocation, nil, map[string]any{"workflow": "initial"})
	require.NoError(t, err)
	handle, err := runtime.startWorkflow(t.Context(), request)
	require.NoError(t, err)
	output, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, output.Suspension)
	return store, "service-root", &api.PendingInputResponse{Confirmation: &api.ConfirmationDecision{ID: output.Suspension.Pending[0].Confirmation.ID, Approved: true}}
}

// StartWorkflow records the submitted request, then loses its first reply so a
// fresh runtime must recover the same saved request before submitting it again.
func (e *continuationLostReplyEngine) StartWorkflow(ctx context.Context, request engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	handle, err := e.stubEngine.StartWorkflow(ctx, request)
	if err != nil {
		return nil, err
	}
	if e.startCalls == 1 {
		return nil, errors.New("engine reply lost")
	}
	return handle, nil
}
