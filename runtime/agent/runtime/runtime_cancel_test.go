package runtime

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
)

type recordingCancelByIDEngine struct {
	engine.Engine

	mu       sync.Mutex
	canceled []string
	err      error
}

func (e *recordingCancelByIDEngine) CancelByID(ctx context.Context, runID string) error {
	_ = ctx
	e.mu.Lock()
	e.canceled = append(e.canceled, runID)
	e.mu.Unlock()
	return e.err
}

func (e *recordingCancelByIDEngine) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.canceled))
	copy(out, e.canceled)
	return out
}

func TestCancelRun_CancelsByID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	eng := &recordingCancelByIDEngine{Engine: engineinmem.New()}
	rt := New(WithEngine(eng))

	err := rt.CancelRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, []string{"run-1"}, eng.snapshot())
}

func TestCancelRun_IgnoresWorkflowNotFound(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	eng := &recordingCancelByIDEngine{
		Engine: engineinmem.New(),
		err:    engine.ErrWorkflowNotFound,
	}
	rt := New(WithEngine(eng))

	err := rt.CancelRun(ctx, "run-1")
	require.NoError(t, err)
	require.Equal(t, []string{"run-1"}, eng.snapshot())
}

func TestCancelRun_RequiresRunID(t *testing.T) {
	t.Parallel()

	rt := New(WithEngine(engineinmem.New()))
	err := rt.CancelRun(context.Background(), "")
	require.Error(t, err)
}
