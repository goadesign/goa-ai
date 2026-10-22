package runtime

// Exact root worker selection survives prepared starts and is required when
// a caller proves completion of the same accepted operation.

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/rawjson"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
)

func TestPreparedRunRetainsRootExecutionTarget(t *testing.T) {
	backend := &stubEngine{}
	client, store := newPreparedRunTestClient(backend, testAgentDefinition("svc.agent", "agent.workflow", "agent.queue", nil, nil))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	target := engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: "build-a"}
	prepared, err := client.Prepare(t.Context(), "session-1", nil, WithRunID("root"), WithRootExecutionTarget(target))
	require.NoError(t, err)
	data, err := prepared.MarshalBinary()
	require.NoError(t, err)
	target.BuildID = "build-b"
	parsed, err := ParsePreparedRun(data)
	require.NoError(t, err)
	for range 2 {
		_, err = client.StartPrepared(t.Context(), parsed)
		require.NoError(t, err)
		require.Equal(t, &engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: "build-a"}, backend.last.RootExecutionTarget)
	}
}

func TestExactOperationCompletionRequiresAcceptedRootTarget(t *testing.T) {
	operation := testNativeRegistryResolution("revision/1").Toolset.Tools[0]
	arguments := rawjson.Message(`{"value":1}`)
	target := engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: "build-a"}
	request, err := prepareOperationRequest("", "root", operation, arguments, nil, nil, WithRootExecutionTarget(target))
	require.NoError(t, err)
	require.Equal(t, &target, request.RootExecutionTarget)
	snapshot, err := startrecipe.SnapshotRequest(request)
	require.NoError(t, err)
	for _, test := range []struct {
		name     string
		options  []RunOption
		conflict bool
	}{
		{"same target", []RunOption{WithRootExecutionTarget(target)}, false},
		{"missing target", nil, true},
		{"changed target", []RunOption{WithRootExecutionTarget(engine.WorkerDeploymentVersion{DeploymentName: "execution", BuildID: "build-b"})}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := &completionOnlyEngine{completion: engine.RunCompletion{
				Status: engine.RunStatusFailed, StartRequestDigest: snapshot.Digest,
				CompletedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), WorkflowError: errors.New("closed before entry"),
			}}
			store := &earlyTerminalCaptureStore{Store: storageinmem.New()}
			rt := New(store, WithEngine(backend))
			err := rt.EnsureOneShotOperationCompletion(t.Context(), "root", operation, arguments, nil, nil, test.options...)
			if test.conflict {
				require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
				require.Nil(t, store.terminal)
			} else {
				require.NoError(t, err)
				require.NotNil(t, store.terminal)
			}
			require.Zero(t, backend.starts)
		})
	}
}
