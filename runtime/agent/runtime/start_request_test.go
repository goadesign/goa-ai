package runtime

// New executions submit later timestamps. Storage must select the original
// closed start before runtime observers, prompts or planner code can run.

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestFreshClosedStartSuppressesEffects(t *testing.T) {
	for _, kind := range []storageCommandKind{
		storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
	} {
		for _, status := range []session.RunStatus{
			session.RunStatusSuspended, session.RunStatusCompleted, session.RunStatusFailed, session.RunStatusCanceled,
		} {
			t.Run(fmt.Sprintf("%d/%s", kind, status), func(t *testing.T) {
				f := newClosedStartFixture(t, kind, status, true)
				_, err := f.runtime.executeStorageCommand(t.Context(), f.command)
				require.NoError(t, err)
				originalMeta, originalRecords := f.store.closedMeta, f.store.closedPage
				fresh := &routeWorkflowContext{
					ctx: t.Context(), runID: f.input.RunID, hookRuntime: f.runtime,
					now: func() time.Time { return time.Unix(200, 0) },
				}
				out, err := f.runtime.ExecuteWorkflow(fresh, f.input)
				require.ErrorIs(t, err, engine.ErrWorkflowCompleted)
				assert.Nil(t, out)
				assert.Empty(t, fresh.lastPlannerCall.Name)
				assert.Empty(t, fresh.lastToolCall.Name)
				assert.Equal(t, originalMeta, f.store.closedMeta)
				assert.Equal(t, originalRecords, f.store.closedPage)
				f.assertUnchanged(t)
			})
		}
	}
}

func TestStartActivityRequiresExactDigestBytes(t *testing.T) {
	for _, kind := range []storageCommandKind{
		storageCommandRootStart, storageCommandChildStart, storageCommandOneShotStart, storageCommandOneShotChildStart,
	} {
		for _, length := range []int{0, 1, 31, 33} {
			t.Run(fmt.Sprintf("%d/%d", kind, length), func(t *testing.T) {
				f := newClosedStartFixture(t, kind, session.RunStatusCompleted, true)
				digest := make([]byte, length)
				if length > 0 {
					digest[0] = 1
				}
				switch kind {
				case storageCommandRootStart:
					f.command.RootStart.RequestDigest = digest
				case storageCommandChildStart:
					f.command.ChildStart.RequestDigest = digest
				case storageCommandOneShotStart:
					f.command.OneShotStart.RequestDigest = digest
				case storageCommandOneShotChildStart:
					f.command.OneShotChildStart.RequestDigest = digest
				case storageCommandAppend, storageCommandCancellation, storageCommandSuspension, storageCommandTerminal,
					storageCommandSynchronousStart, storageCommandSeedBegin, storageCommandSeedAppend, storageCommandSeedPublish:
					t.Fatal("unexpected fixture kind")
				}
				// Decode the real activity payload. Byte length remains visible;
				// a fixed Go array here would pad or truncate malformed JSON.
				codec := workflowcodec.NewDataConverter()
				payload, err := codec.ToPayload(f.command)
				require.NoError(t, err)
				var decoded api.StorageActivityCommand
				require.NoError(t, codec.FromPayload(payload, &decoded))
				_, err = f.runtime.executeStorageCommand(t.Context(), &decoded)
				require.ErrorContains(t, err, "must contain 32 bytes")
				assert.True(t, engine.IsActivityErrorNonRetryable(err))
				_, err = f.store.LoadRun(t.Context(), f.input.RunID)
				require.ErrorIs(t, err, session.ErrRunNotFound)
				assert.Empty(t, f.bus.events)
			})
		}
	}
}
