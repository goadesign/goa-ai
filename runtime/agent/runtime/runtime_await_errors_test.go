package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
)

type signalByIDEngine struct {
	engine.Engine
	err error
}

func (e *signalByIDEngine) SignalByID(ctx context.Context, workflowID, runID, name string, payload any) error {
	_ = ctx
	_ = workflowID
	_ = runID
	_ = name
	_ = payload
	return e.err
}

func TestProvideClarification_MapsCompletedRunToTypedError(t *testing.T) {
	t.Parallel()

	rt := New(WithEngine(&signalByIDEngine{
		Engine: engineinmem.New(),
		err:    engine.ErrWorkflowCompleted,
	}))
	err := rt.ProvideClarification(context.Background(), &api.ClarificationAnswer{
		RunID:  "run-1",
		ID:     "await-1",
		Answer: "ok",
	})
	require.Error(t, err)
	require.True(t, IsRunNotAwaitable(err))
	require.ErrorIs(t, err, engine.ErrWorkflowCompleted)

	typed, ok := AsRunNotAwaitable(err)
	require.True(t, ok)
	require.Equal(t, "run-1", typed.RunID)
	require.Equal(t, RunNotAwaitableCompletedRun, typed.Reason)
}

func TestProvideToolResults_MapsUnknownRunToTypedError(t *testing.T) {
	t.Parallel()

	rt := New(WithEngine(&signalByIDEngine{
		Engine: engineinmem.New(),
		err:    engine.ErrWorkflowNotFound,
	}))
	err := rt.ProvideToolResults(context.Background(), &api.ToolResultsSet{
		RunID: "run-2",
		ID:    "await-2",
	})
	require.Error(t, err)
	require.True(t, IsRunNotAwaitable(err))
	require.ErrorIs(t, err, engine.ErrWorkflowNotFound)

	typed, ok := AsRunNotAwaitable(err)
	require.True(t, ok)
	require.Equal(t, "run-2", typed.RunID)
	require.Equal(t, RunNotAwaitableUnknownRun, typed.Reason)
}

func TestProvideConfirmation_PassesThroughNonContractError(t *testing.T) {
	t.Parallel()

	want := errors.New("signal transport unavailable")
	rt := New(WithEngine(&signalByIDEngine{
		Engine: engineinmem.New(),
		err:    want,
	}))
	err := rt.ProvideConfirmation(context.Background(), &api.ConfirmationDecision{
		RunID:    "run-3",
		ID:       "await-3",
		Approved: true,
	})
	require.ErrorIs(t, err, want)
	require.False(t, IsRunNotAwaitable(err))
}
