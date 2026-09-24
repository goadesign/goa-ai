package runtime

// Closed start responses stop execution and cancellation before either can
// publish effects or replace the saved lifecycle result.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/prompt"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestClosedStartSuppressesHooksAndWorkflowEffects(t *testing.T) {
	for _, kind := range []storageCommandKind{
		storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
	} {
		for _, status := range []session.RunStatus{
			session.RunStatusSuspended, session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled,
		} {
			t.Run(fmt.Sprintf("%d/%s", kind, status), func(t *testing.T) {
				f := newClosedStartFixture(t, kind, status)
				f.input.Messages = []*model.Message{{
					Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "meaningful input"}},
				}}
				f.input.RenderedPrompts = []prompt.RenderEvent{{PromptID: "agent.system", Version: "v1"}}

				output, err := f.runtime.ExecuteWorkflow(f.context, f.input)

				require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
				assert.Nil(t, output)
				assert.Empty(t, f.context.lastPlannerCall.Name)
				assert.Empty(t, f.context.lastToolCall.Name)
				require.NotNil(t, f.context.cancellationHandler)
				err = f.context.cancellationHandler(f.context, engine.CancellationRequest{
					RunID: "run", Reason: run.CancellationReasonUserRequested,
				})
				require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
				f.assertUnchanged(t)
				if status == session.RunStatusSuspended {
					checkpoint, err := f.store.LoadRunSuspension(t.Context(), "run")
					require.NoError(t, err)
					assert.JSONEq(t, `{"version":"v6"}`, string(checkpoint.Data))
				}
			})
		}
	}
}

func TestRunOneShotClosedStartSkipsCallback(t *testing.T) {
	for _, status := range []session.RunStatus{
		session.RunStatusSuspended, session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled,
	} {
		t.Run(string(status), func(t *testing.T) {
			f := newClosedStartFixture(t, storageCommandOneShotStart, status)
			calls := 0

			err := f.runtime.RunOneShot(t.Context(), OneShotRunInput{
				AgentID: "agent", RunID: "run", TurnID: "turn",
			}, func(context.Context) error {
				calls++
				return nil
			})

			require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
			assert.Zero(t, calls)
			f.assertUnchanged(t)
		})
	}
}

func TestClosedStartCancellationDoesNotBecomeAccepted(t *testing.T) {
	for _, kind := range []storageCommandKind{
		storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
	} {
		for _, status := range []session.RunStatus{
			session.RunStatusSuspended, session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled,
		} {
			t.Run(fmt.Sprintf("%d/%s", kind, status), func(t *testing.T) {
				f := newClosedStartFixture(t, kind, status)
				state := &workflowFinalizationState{}

				err := f.runtime.handleWorkflowCancellation(state, f.context, f.input, f.command, engine.CancellationRequest{
					RunID: "run", Reason: run.CancellationReasonUserRequested,
				})

				require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
				assert.Equal(t, workflowOpen, state.phase.Load())
				accepted, err := state.beginFinalization(f.context)
				require.NoError(t, err)
				assert.False(t, accepted)
				state.finishFinalization()
				assert.Equal(t, workflowFinalizationFinished, state.phase.Load())
				f.assertUnchanged(t)
			})
		}
	}
}

func TestEndedSessionStartKeepsOriginalStopBehavior(t *testing.T) {
	for _, kind := range []storageCommandKind{storageCommandRootStart, storageCommandChildStart} {
		t.Run(fmt.Sprint(kind), func(t *testing.T) {
			f := newClosedStartFixture(t, kind, session.RunStatusCanceled)
			store := f.store.Store
			f.runtime.Store = store
			_, err := store.(interface {
				EndSession(context.Context, string, time.Time) (session.Session, error)
			}).EndSession(t.Context(), "session", time.Unix(3, 0))
			require.NoError(t, err)

			result, err := f.runtime.executeStorageCommand(t.Context(), f.command)

			require.NoError(t, err)
			start := runStartStorageResult(f.input, result)
			assert.Equal(t, session.RunStartStop, start.Outcome)
			assert.Equal(t, session.RunStatusCanceled, start.RunStatus)
			assert.Equal(t, run.CancellationReasonSessionEnded, start.CancellationReason)
			count := 2
			if kind == storageCommandChildStart {
				count = 3
			}
			assert.Len(t, f.bus.events, count)
			assert.Empty(t, f.sink.snapshot())
			retry, err := f.runtime.executeStorageCommand(t.Context(), f.command)
			require.NoError(t, err)
			assert.Equal(t, session.RunStatusCanceled, runStartStorageResult(f.input, retry).RunStatus)
			assert.Len(t, f.bus.events, count)

			state := &workflowFinalizationState{}
			err = f.runtime.handleWorkflowCancellation(state, f.context, f.input, f.command, engine.CancellationRequest{
				RunID: "run", Reason: run.CancellationReasonSessionEnded,
			})
			require.NoError(t, err)
			assert.Equal(t, workflowCancellationAccepted, state.phase.Load())
			state = &workflowFinalizationState{}
			err = f.runtime.handleWorkflowCancellation(state, f.context, f.input, f.command, engine.CancellationRequest{
				RunID: "run", Reason: run.CancellationReasonUserRequested,
			})
			var conflict *engine.CancellationConflictError
			require.ErrorAs(t, err, &conflict)
			assert.Equal(t, workflowOpen, state.phase.Load())
		})
	}
}
