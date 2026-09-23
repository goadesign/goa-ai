package runtime

// These tests run the child activity with the engine's heartbeat context.
// A controlled store and virtual time expose slow reads and cancellation
// without waiting for real activity deadlines or contacting an engine.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/runlog"
	"goa.design/goa-ai/runtime/agent/storage"
)

type (
	// childHistoryGate pauses each page before delegating to the real store.
	childHistoryGate struct {
		storage.Store
		entered chan string
		release chan struct{}
		err     error
	}

	childActivityResult struct {
		output *api.AgentChildActivityOutput
		err    error
	}

	childPublicationFailure struct {
		storage.Store
		err      error
		attempts int
	}

	// cancelingChildHeartbeat delivers cancellation on a heartbeat, as an
	// engine can do when it learns that the activity has been canceled.
	cancelingChildHeartbeat struct {
		heartbeatRecorder
		cancel context.CancelFunc
	}
)

func TestChildActivityClassifiesPublicationFailures(t *testing.T) {
	cause := errors.New("publication rejected")
	for _, test := range []struct {
		name      string
		err       error
		permanent bool
	}{
		{"wrapped permanent", fmt.Errorf("store: %w", storage.NewContractError(cause)), true},
		{"transient", cause, false},
		{"canceled", context.Canceled, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt, input := childHeartbeatFixture(t)
			rt.Store = &childPublicationFailure{Store: rt.Store, err: test.err}
			output, err := rt.prepareAgentChildActivity(t.Context(), input)
			require.Nil(t, output)
			require.ErrorIs(t, err, test.err)
			require.Equal(t, test.permanent, engine.IsActivityErrorNonRetryable(err))
		})
	}
}

func TestChildPublicationFailureEngineRetry(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		attempts int
	}{
		{"permanent", storage.NewContractError(storage.ErrSeedConflict), 1},
		{"transient", errors.New("publication unavailable"), 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			rt, input := childHeartbeatFixture(t)
			store := &childPublicationFailure{Store: rt.Store, err: test.err}
			rt.Store = store
			require.NoError(t, rt.Engine.RegisterAgentChildActivity(t.Context(), "test.prepare", engine.ActivityOptions{
				RetryPolicy: engine.RetryPolicy{MaxAttempts: 3, InitialInterval: time.Millisecond, BackoffCoefficient: 2},
			}, rt.prepareAgentChildActivity))
			require.NoError(t, rt.Engine.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
				Name: "test.workflow",
				Handler: func(wf engine.WorkflowContext, _ *api.RunInput) (*api.RunOutput, error) {
					_, err := wf.ExecuteAgentChildActivity(engine.AgentChildActivityCall{Name: "test.prepare", Input: input})
					return nil, err
				},
			}))
			handle, err := rt.Engine.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
				ID: "test-parent", Workflow: "test.workflow", TaskQueue: "test.queue",
				Input: &api.RunInput{RunID: "test-parent"},
			})
			require.NoError(t, err)
			_, err = handle.Wait(t.Context())
			require.Error(t, err)
			require.Equal(t, test.attempts, store.attempts)
		})
	}
}

func (s *childPublicationFailure) PublishRunSeed(context.Context, storage.SeedPublication) error {
	s.attempts++
	return s.err
}

func TestChildActivityHeartbeatsAcrossPagedHistoryAndStopsOnSuccess(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, input := childHeartbeatFixture(t)
		gate := &childHistoryGate{Store: rt.Store, entered: make(chan string), release: make(chan struct{})}
		rt.Store = gate
		recorder := &heartbeatRecorder{}
		ctx := engine.WithActivityHeartbeatTimeout(t.Context(), 20*time.Second)
		ctx = engine.WithActivityHeartbeatRecorder(ctx, recorder)
		done := make(chan childActivityResult, 1)
		go func() {
			output, err := rt.prepareAgentChildActivity(ctx, input)
			done <- childActivityResult{output, err}
		}()
		var previous string
		for page := range 3 {
			cursor := <-gate.entered
			if page == 0 {
				require.Empty(t, cursor)
			} else {
				require.NotEmpty(t, cursor)
				require.NotEqual(t, previous, cursor)
			}
			previous = cursor
			before := recorder.Count()
			time.Sleep(20 * time.Second)
			synctest.Wait()
			require.Greater(t, recorder.Count(), before, "blocked page reads must keep the attempt alive")
			gate.release <- struct{}{}
		}
		result := <-done
		require.NoError(t, result.err)
		require.NotNil(t, result.output.Success)
		synctest.Wait()
		count := recorder.Count()
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, count, recorder.Count(), "completed activity must stop heartbeat work")
	})
}

func TestChildActivityHeartbeatCancellationReachesBlockedRead(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		rt, input := childHeartbeatFixture(t)
		gate := &childHistoryGate{Store: rt.Store, entered: make(chan string), release: make(chan struct{})}
		rt.Store = gate
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		recorder := &cancelingChildHeartbeat{cancel: cancel}
		ctx = engine.WithActivityHeartbeatTimeout(ctx, 20*time.Second)
		ctx = engine.WithActivityHeartbeatRecorder(ctx, recorder)
		done := make(chan childActivityResult, 1)
		go func() {
			output, err := rt.prepareAgentChildActivity(ctx, input)
			done <- childActivityResult{output, err}
		}()
		require.Empty(t, <-gate.entered)
		gate.release <- struct{}{}
		require.NotEmpty(t, <-gate.entered)
		require.NoError(t, ctx.Err(), "the second read begins before engine cancellation")
		time.Sleep(10 * time.Second)
		result := <-done
		require.ErrorIs(t, result.err, context.Canceled)
		require.Nil(t, result.output)
		synctest.Wait()
		count := recorder.Count()
		require.Equal(t, 3, count)
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, count, recorder.Count())
	})
}

func TestChildActivityHeartbeatStopsOnRejectedOwnerAndReadFailure(t *testing.T) {
	for _, ownerFailure := range []bool{true, false} {
		name := "read failure"
		if ownerFailure {
			name = "owner rejection"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				rt, input := childHeartbeatFixture(t)
				cause := errors.New("store read failed")
				gate := &childHistoryGate{Store: rt.Store, entered: make(chan string), release: make(chan struct{}), err: cause}
				rt.Store = gate
				if ownerFailure {
					input.Call.RunID = "foreign-run"
				}
				recorder := &heartbeatRecorder{}
				ctx := engine.WithActivityHeartbeatTimeout(t.Context(), 20*time.Second)
				ctx = engine.WithActivityHeartbeatRecorder(ctx, recorder)
				done := make(chan childActivityResult, 1)
				go func() {
					output, err := rt.prepareAgentChildActivity(ctx, input)
					done <- childActivityResult{output, err}
				}()
				if !ownerFailure {
					<-gate.entered
					time.Sleep(20 * time.Second)
					gate.release <- struct{}{}
				}
				result := <-done
				require.Nil(t, result.output)
				if ownerFailure {
					require.True(t, engine.IsActivityErrorNonRetryable(result.err))
				} else {
					require.ErrorIs(t, result.err, cause)
					require.False(t, engine.IsActivityErrorNonRetryable(result.err))
				}
				synctest.Wait()
				count := recorder.Count()
				require.Positive(t, count)
				time.Sleep(time.Minute)
				synctest.Wait()
				require.Equal(t, count, recorder.Count())
			})
		})
	}
}

// childHeartbeatFixture stores three separate transcript deltas so one-record
// page reads must preserve the same committed end across the activity.
func childHeartbeatFixture(t *testing.T) (*Runtime, *api.AgentChildActivityInput) {
	t.Helper()
	rt := New(newTestStore())
	parent := run.Context{RunID: "parent-run", SessionID: "session"}
	input := seedTestPlanInput(t, rt, PlanActivityInput{
		AgentID: "parent.agent", RunID: parent.RunID, RunContext: parent,
	}, []*model.Message{userMsg("original request")})
	input.HistoryEndID = appendTestActivityHistory(t, rt, input, []*model.Message{assistantTextMsg("earlier answer")})
	input.HistoryEndID = appendTestActivityHistory(t, rt, input, []*model.Message{userMsg("follow-up request")})
	cfg := AgentToolConfig{Definition: testAgentDefinition("child.agent", "child.workflow", "child.queue", nil, nil)}
	registerAgentToolTestConfig(rt, cfg, "child", newAnyJSONSpec("child.lookup"))
	return rt, &api.AgentChildActivityInput{
		Call: ToolCall{AgentID: "parent.agent", RunID: parent.RunID, SessionID: parent.SessionID,
			Name: "child.lookup", ToolCallID: "child-call", Payload: rawjson.Message(`{}`)},
		ParentRun: parent, HistoryEndID: input.HistoryEndID,
	}
}

func (s *childHistoryGate) ListRunTranscriptRecords(ctx context.Context, runID, endID, afterID string, _ int) (runlog.Page, error) {
	select {
	case s.entered <- afterID:
	case <-ctx.Done():
		return runlog.Page{}, ctx.Err()
	}
	select {
	case <-s.release:
	case <-ctx.Done():
		return runlog.Page{}, ctx.Err()
	}
	if s.err != nil {
		return runlog.Page{}, s.err
	}
	return s.Store.ListRunTranscriptRecords(ctx, runID, endID, afterID, 1)
}

func (r *cancelingChildHeartbeat) RecordHeartbeat(details ...any) {
	r.heartbeatRecorder.RecordHeartbeat(details...)
	if r.Count() == 3 {
		r.cancel()
	}
}
