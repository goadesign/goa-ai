package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

type completionOnlyEngine struct {
	engine.Engine
	completion engine.RunCompletion
	starts     int
}

func (e *completionOnlyEngine) QueryRunCompletion(context.Context, string) (engine.RunCompletion, error) {
	return e.completion, nil
}

func (e *completionOnlyEngine) StartWorkflow(context.Context, engine.WorkflowStartRequest) (engine.WorkflowHandle, error) {
	e.starts++
	return nil, errors.New("repair must not start workflows")
}

func TestEnsureOperationCompletionRequiresExactAcceptedRequest(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "changed memo"}[changed], func(t *testing.T) {
			operation := testNativeRegistryResolution("revision/1").Toolset.Tools[0]
			args := rawjson.Message(`{"value":1}`)
			memo := map[string]any{"selection": "original"}
			request, err := prepareOperationRequest("", "closed-before-start", operation, args, nil, memo)
			require.NoError(t, err)
			expected, err := startrecipe.SnapshotRequest(request)
			require.NoError(t, err)
			adapter := &completionOnlyEngine{completion: engine.RunCompletion{
				Status: engine.RunStatusFailed, StartRequestDigest: expected.Digest,
				CompletedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), WorkflowError: errors.New("closed before workflow entry"),
			}}
			store := &earlyTerminalCaptureStore{Store: storageinmem.New()}
			rt := New(store, WithEngine(adapter))
			if changed {
				memo["selection"] = "other"
			}
			err = rt.EnsureOneShotOperationCompletion(t.Context(), "closed-before-start", operation, args, nil, memo)
			assert.Zero(t, adapter.starts)
			_, loadErr := store.LoadRun(t.Context(), "closed-before-start")
			assert.ErrorIs(t, loadErr, session.ErrRunNotFound) //nolint:testifylint // Terminal storage and completion errors are checked independently of this Run lookup.
			if changed {
				assert.ErrorIs(t, err, engine.ErrWorkflowStartConflict) //nolint:testifylint // The terminal storage check must still run if completion returns a different error.
				assert.Nil(t, store.terminal)
			} else {
				require.NoError(t, err)
				require.NotNil(t, store.terminal)
				timestamp, ok := store.terminal.Time.AsEngineClosure()
				assert.True(t, ok)
				assert.Equal(t, "2026-01-02T03:04:05Z", string(timestamp))
				assert.Equal(t, expected.Digest[:], store.terminal.RequestDigest)
			}
		})
	}
}
