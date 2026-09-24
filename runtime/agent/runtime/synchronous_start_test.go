package runtime

// Synchronous start commands cross the same strict activity boundary as engine
// starts, but carry no engine proof and cannot select an engine-owned Run.

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/storage"
)

func TestSynchronousStartActivityContract(t *testing.T) {
	f := newClosedStartFixture(t, storageCommandOneShotStart, session.RunStatusCompleted, true)
	command := &api.StorageActivityCommand{SynchronousStart: &api.SynchronousRunStartCommand{
		SeedEndID: f.input.SeedEndID, Started: f.command.OneShotStart.Started,
	}}
	codec := workflowcodec.NewDataConverter()
	encoded, err := codec.ToPayload(command)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded.Data), `"RequestDigest"`)
	var decoded api.StorageActivityCommand
	require.NoError(t, codec.FromPayload(encoded, &decoded))
	result, err := f.runtime.executeStorageCommand(t.Context(), &decoded)
	require.NoError(t, err)
	require.NotNil(t, result.SynchronousStart)
	assert.Equal(t, session.RunStatusCompleted, result.SynchronousStart.RunStatus)
	f.assertUnchanged(t)

	for _, name := range []string{"missing status", "inserted closed", "wrong branch", "missing record"} {
		t.Run(name, func(t *testing.T) {
			result := &api.StorageActivityResult{SynchronousStart: &api.StartRunResult{
				Outcome: session.RunStartProceed, RunStatus: session.RunStatusCompleted,
				Records: []storage.AppendResult{{ID: "record"}},
			}}
			switch name {
			case "missing status":
				result.SynchronousStart.RunStatus = ""
			case "inserted closed":
				result.SynchronousStart.Records[0].Inserted = true
			case "wrong branch":
				result.OneShotStart, result.SynchronousStart = result.SynchronousStart, nil
			case "missing record":
				result.SynchronousStart.Records = nil
			}
			require.Error(t, validateStorageResult(storageCommandSynchronousStart, result))
		})
	}
	encoded.Data = bytes.Replace(encoded.Data, []byte(`"SynchronousStart":{`), []byte(`"SynchronousStart":{"RequestDigest":"AA==",`), 1)
	require.ErrorContains(t, codec.FromPayload(encoded, &decoded), `unknown field "RequestDigest"`)
}

func TestRunOneShotRejectsAnotherPreparedCommand(t *testing.T) {
	store := newTestStore()
	runtime := newFromOptions(store, Options{Hooks: hooks.NewBus()})
	event := hooks.NewRunStartedEvent("run", "svc.agent", "", "", "", nil)
	record, err := hooks.EncodeToRecordInput(event, hooks.EncodeOptions{
		TurnID: "run", EventKey: "run-started", TimestampMS: time.Unix(1, 0).UnixMilli(),
	})
	require.NoError(t, err)
	writer, err := stageLiteralHistory(t.Context(), store, storage.SeedDeclaration{
		AgentID: "svc.agent", RunID: "run", CommandID: "run", AttemptID: "run", Kind: storage.SeedLiteral,
	}, nil)
	require.NoError(t, err)
	command := &api.StorageActivityCommand{SynchronousStart: &api.SynchronousRunStartCommand{
		SeedEndID: writer.endID, Started: record,
	}}
	compiled, err := json.Marshal(command)
	require.NoError(t, err)
	require.NoError(t, writer.publish(t.Context(), compiled))
	_, err = runtime.executeStorageCommand(t.Context(), command)
	require.NoError(t, err)
	calls := 0
	err = runtime.RunOneShot(t.Context(), OneShotRunInput{AgentID: "svc.agent", RunID: "run"}, func(context.Context) error {
		calls++
		return nil
	})
	require.ErrorIs(t, err, storage.ErrSeedConflict)
	assert.True(t, engine.IsActivityErrorNonRetryable(err))
	assert.Zero(t, calls)
	page, err := store.ListRunRecords(t.Context(), "run", "", 10)
	require.NoError(t, err)
	assert.Len(t, page.Events, 1)
}
