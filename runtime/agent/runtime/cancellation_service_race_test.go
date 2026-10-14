// Service cancellation tests force an answer to win successor admission before
// the cancellation attempt closes, then verify delivery follows the accepted run.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/mcp"
)

type (
	serviceCancellationRejectStore struct {
		storage.Store
		reject bool
	}

	// serviceCancellationRaceEngine starts the answer before the cancellation
	// workflow and withholds the cancellation query until that workflow closes.
	serviceCancellationRaceEngine struct {
		registeredRootTestEngine
		beforeCancellation func(context.Context) error
		cancellation       engine.WorkflowHandle
		accepted           engine.WorkflowStartRequest
		closed             engine.RunCompletion
		queryError         error
		changeDigest       bool
		firstOpen          bool
		queries            int
		deliveries         []string
	}
)

func TestServiceCancellationFollowsAnswerAfterLosingAttemptCloses(t *testing.T) {
	queryFailure := errors.New("completion query unavailable")
	for _, test := range []struct {
		name         string
		changeDigest bool
		firstOpen    bool
		queryError   error
		wantError    error
		rejectStart  bool
	}{
		{name: "admitted answer receives cancellation"},
		{name: "first observation remains pending", firstOpen: true, wantError: ErrRunCompletionNotReady},
		{name: "changed engine identity remains conflict", changeDigest: true, wantError: engine.ErrWorkflowStartConflict},
		{name: "unrelated query failure remains error", queryError: queryFailure, wantError: queryFailure},
		{name: "failed cancellation without successor is permanent", rejectStart: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			resolution, invocation := registeredRootFixture()
			questions := &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)}}}
			zero := int64(0)
			pending, err := api.NewPendingTaskInput("provider-task", &zero, questions)
			require.NoError(t, err)
			answerRunning := make(chan struct{})
			backend := &serviceCancellationRaceEngine{
				registeredRootTestEngine: registeredRootTestEngine{Engine: engineinmem.New(), execute: func(ctx context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
					assert.Equal(t, *invocation.Invocation, input.Invocation)
					assert.Equal(t, invocation.Arguments, []byte(input.OriginalArguments))
					if input.ExecutionSequence == 0 {
						return &api.ToolOutput{PendingExecution: pending}, nil
					}
					if taskID, canceling := input.ExecutionContinuation.AsTaskCancel(); canceling {
						assert.Equal(t, "provider-task", taskID)
						acknowledged, err := api.NewPendingTaskWait(taskID, &zero)
						return &api.ToolOutput{PendingExecution: acknowledged}, err
					}
					close(answerRunning)
					<-ctx.Done()
					return nil, ctx.Err()
				}},
				changeDigest: test.changeDigest,
				firstOpen:    test.firstOpen,
				queryError:   test.queryError,
			}
			store := &serviceCancellationRejectStore{Store: newTestStore(), reject: test.rejectStart}
			runtime := New(store, WithEngine(backend), WithToolWorkflowQueue("service-worker"))
			require.NoError(t, runtime.RegisterToolWorkflow(ctx))
			handle, err := runtime.StartOneShotToolOperation(ctx, "registry", resolution, invocation, nil, nil)
			require.NoError(t, err)
			output, err := handle.Wait(ctx)
			require.NoError(t, err)
			require.NotNil(t, output.Suspension)
			var answer engine.WorkflowHandle
			backend.beforeCancellation = func(ctx context.Context) error {
				if test.rejectStart {
					return nil
				}
				var err error
				answer, err = runtime.StartToolContinuation(ctx, "service-root", "answer", &api.PendingInputResponse{MCP: &api.MCPInputResponse{ToolCallID: "service-root", Responses: map[string]json.RawMessage{"choice": json.RawMessage(`{"action":"accept","content":{}}`)}}})
				if err != nil {
					return err
				}
				select {
				case <-answerRunning:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			request := engine.CancellationRequest{RunID: "service-root", Reason: "user-canceled"}
			settled, err := runtime.cancellationActivity(ctx, request)
			assert.False(t, settled)
			assert.Equal(t, 1, backend.queries)
			if test.rejectStart {
				require.ErrorContains(t, err, "cancellation continuation failed before admission")
				assert.True(t, engine.IsActivityErrorNonRetryable(err))
				predecessor, loadErr := store.LoadRun(ctx, "service-root")
				require.NoError(t, loadErr)
				assert.Nil(t, predecessor.SuspensionResolution)
				_, loadErr = store.LoadRun(ctx, backend.accepted.ID)
				assert.ErrorIs(t, loadErr, session.ErrRunNotFound) //nolint:testifylint // The next observation must independently preserve the permanent error.
				settled, err = runtime.cancellationActivity(ctx, request)
				assert.False(t, settled)
				require.ErrorContains(t, err, "cancellation continuation failed before admission")
				assert.True(t, engine.IsActivityErrorNonRetryable(err))
				return
			}
			if test.wantError != nil {
				require.ErrorIs(t, err, test.wantError)
			} else {
				require.NoError(t, err)
			}
			assert.Equal(t, engine.RunStatusFailed, backend.closed.Status)
			require.ErrorIs(t, backend.closed.WorkflowError, engine.ErrWorkflowStartConflict)
			snapshot, err := startrecipe.SnapshotRequest(backend.accepted)
			require.NoError(t, err)
			assert.Equal(t, snapshot.Digest, backend.closed.StartRequestDigest)
			predecessor, err := store.LoadRun(ctx, "service-root")
			require.NoError(t, err)
			assert.Equal(t, session.ContinuedSuspension{RunID: "answer"}, predecessor.SuspensionResolution)
			assert.Empty(t, backend.deliveries)
			// A later observation follows only the answer already admitted by
			// storage, preserving the cancellation reason and original invocation.
			settled, err = runtime.cancellationActivity(ctx, request)
			require.NoError(t, err)
			assert.False(t, settled)
			_, err = answer.Wait(ctx)
			require.Error(t, err)
			assert.Equal(t, []string{"answer"}, backend.deliveries)
			meta, err := store.LoadRun(ctx, "answer")
			require.NoError(t, err)
			assert.Equal(t, session.RunStatusCanceled, meta.Status)
			assert.Equal(t, request.Reason, meta.CancellationReason)
			assert.Equal(t, *invocation.Invocation, meta.Origin)
		})
	}
}

func (s *serviceCancellationRejectStore) StartOneShotRun(ctx context.Context, command storage.OneShotRunStart) (storage.OneShotRunStartResult, error) {
	if s.reject && command.Run.PredecessorRunID != "" {
		return storage.OneShotRunStartResult{}, storage.NewContractError(session.ErrRunConflict)
	}
	return s.Store.StartOneShotRun(ctx, command)
}

func (e *serviceCancellationRaceEngine) StartWorkflow(ctx context.Context, request engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	if request.Input.Continuation != nil && request.Input.Continuation.Cancellation != nil {
		if err := e.beforeCancellation(ctx); err != nil {
			return nil, err
		}
		e.accepted = request
		handle, err := e.Engine.StartWorkflow(ctx, request)
		e.cancellation = handle
		return handle, err
	}
	return e.Engine.StartWorkflow(ctx, request)
}

func (e *serviceCancellationRaceEngine) QueryRunCompletion(ctx context.Context, id string) (engine.RunCompletion, error) {
	if id != e.accepted.ID {
		return e.Engine.QueryRunCompletion(ctx, id)
	}
	if _, err := e.cancellation.Wait(ctx); err == nil {
		return engine.RunCompletion{}, errors.New("losing cancellation unexpectedly succeeded")
	}
	completion, err := e.Engine.QueryRunCompletion(ctx, id)
	if err != nil {
		return engine.RunCompletion{}, err
	}
	e.closed = completion
	e.queries++
	if e.firstOpen && e.queries == 1 {
		return engine.RunCompletion{Status: engine.RunStatusRunning, StartRequestDigest: completion.StartRequestDigest}, nil
	}
	if e.queryError != nil {
		return engine.RunCompletion{}, e.queryError
	}
	if e.changeDigest {
		completion.StartRequestDigest[0] ^= 1
	}
	return completion, nil
}

func (e *serviceCancellationRaceEngine) RequestCancellation(ctx context.Context, id string, request engine.CancellationRequest) error {
	e.deliveries = append(e.deliveries, id)
	return e.registeredRootTestEngine.RequestCancellation(ctx, id, request)
}
