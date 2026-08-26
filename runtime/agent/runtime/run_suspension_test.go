package runtime

// run_suspension_test.go verifies that the runtime, rather than an application
// caller or Temporal history, owns the private state needed for continuation.

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/session"
)

func TestSaveAndLoadRunSuspension(t *testing.T) {
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	require.NoError(t, runtime.SessionStore.UpsertRun(context.Background(), session.RunMeta{
		RunID: "run-1", AgentID: "svc.agent", SessionID: "session-1",
	}))
	payload, err := json.Marshal(suspension)
	require.NoError(t, err)
	input := &RecordActivityInput{
		Type: runSuspensionRecordType, RunID: "run-1", AgentID: "svc.agent",
		SessionID: "session-1", Payload: payload,
	}

	require.NoError(t, runtime.saveRunSuspension(context.Background(), input))
	require.NoError(t, runtime.saveRunSuspension(context.Background(), input))
	loaded, err := runtime.LoadRunSuspension(context.Background(), "run-1")
	require.NoError(t, err)
	require.Equal(t, suspension, loaded)
}

func TestSaveRunSuspensionRejectsMismatchedActivityIdentity(t *testing.T) {
	runtime := New()
	spec := newAnyJSONSpec("svc.lookup")
	seedTestToolSpecs(runtime, spec)
	suspension := suspensionContractFixture(t, spec.Name)
	payload, err := json.Marshal(suspension)
	require.NoError(t, err)

	err = runtime.saveRunSuspension(context.Background(), &RecordActivityInput{
		Type: runSuspensionRecordType, RunID: "another-run", AgentID: "svc.agent",
		SessionID: "session-1", Payload: payload,
	})
	require.ErrorContains(t, err, "identity does not match checkpoint")
}

func TestLoadRunSuspensionRejectsCorruptStoredEnvelope(t *testing.T) {
	runtime := New()
	require.NoError(t, runtime.SessionStore.UpsertRun(context.Background(), session.RunMeta{
		RunID: "run-1", AgentID: "svc.agent", SessionID: "session-1",
	}))
	require.NoError(t, runtime.SessionStore.SaveRunSuspension(context.Background(), "run-1", session.RunSuspension{
		ID:   "different-id",
		Data: []byte(`{"id":"suspension-1","version":"` + api.RunSuspensionVersion + `","checkpoint":{},"pending":[]}`),
	}))

	_, err := runtime.LoadRunSuspension(context.Background(), "run-1")
	require.ErrorContains(t, err, "id does not match payload")
}

func TestDecodeStoredRunSuspensionRejectsUnknownAndTrailingData(t *testing.T) {
	for _, data := range [][]byte{
		[]byte(`{"id":"s","unknown":true}`),
		[]byte(`{"id":"s"} {}`),
	} {
		var suspension api.RunSuspension
		require.Error(t, decodeStoredRunSuspension(data, &suspension))
	}
}
