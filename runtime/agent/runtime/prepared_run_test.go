// prepared_run_test.go verifies that initial and continuation runs use one
// durable request format before the workflow engine sees them.
package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	commonpb "go.temporal.io/api/common/v1"
	"go.temporal.io/sdk/converter"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/internal/startrecipe"
	"goa.design/goa-ai/runtime/agent/internal/workflowcodec"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	preparedMemoAlias string

	preparedMemoValue struct {
		Name string
	}
)

func TestPreparedInitialRunSurvivesMutationAndFreshParse(t *testing.T) {
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.agent", "agent.workflow", "agent.queue", nil, nil,
	))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	message := &model.Message{
		Role:  model.ConversationRoleUser,
		Parts: []model.Part{model.TextPart{Text: "original question"}},
	}
	memoBytes := []byte("original memo")
	prepared, err := client.Prepare(
		"session-1",
		[]*model.Message{message},
		WithRunID("run-1"),
		WithMemo(map[string]any{
			"alias":     preparedMemoAlias("named value"),
			"binary":    memoBytes,
			"message":   &commonpb.WorkflowExecution{WorkflowId: "workflow-1", RunId: "run-1"},
			"structure": preparedMemoValue{Name: "structured value"},
		}),
		WithSearchAttributes(map[string]any{"SessionID": "session-1", "priority": int64(7)}),
	)
	require.NoError(t, err)
	require.Zero(t, eng.startCalls)
	require.Zero(t, eng.sealCalls)

	message.Parts[0] = model.TextPart{Text: "mutated question"}
	memoBytes[0] = 'X'
	data, err := prepared.MarshalBinary()
	require.NoError(t, err)
	dataCopy := append([]byte(nil), data...)
	parsed, err := ParsePreparedRun(data)
	require.NoError(t, err)
	data[0] = 'X'

	_, err = client.StartPrepared(t.Context(), parsed)
	require.NoError(t, err)
	require.Equal(t, 1, eng.sealCalls)
	require.Equal(t, "original question", eng.last.Input.Messages[0].Parts[0].(model.TextPart).Text)
	require.Equal(t, preparedMemoAlias("named value"), decodePreparedMemo[preparedMemoAlias](t, eng.last.Memo, "alias"))
	require.Equal(t, []byte("original memo"), decodePreparedMemo[[]byte](t, eng.last.Memo, "binary"))
	messageValue := decodePreparedMemo[*commonpb.WorkflowExecution](t, eng.last.Memo, "message")
	require.Equal(t, "workflow-1", messageValue.GetWorkflowId())
	require.Equal(t, "run-1", messageValue.GetRunId())
	require.Equal(t, preparedMemoValue{Name: "structured value"}, decodePreparedMemo[preparedMemoValue](t, eng.last.Memo, "structure"))
	require.Equal(t, int64(7), eng.last.SearchAttributes["priority"])
	parsedData, err := parsed.MarshalBinary()
	require.NoError(t, err)
	require.Equal(t, dataCopy, parsedData)
}

func TestPrepareRejectsInvalidLaunchBeforeEngineActivation(t *testing.T) {
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.agent", "agent.workflow", "agent.queue", nil, nil,
	))
	require.NoError(t, createPreparedRunSession(t.Context(), store))

	_, err := client.Prepare(
		"session-1",
		nil,
		WithRunID("run-1"),
		WithMemo(map[string]any{"": "missing name"}),
	)

	require.ErrorContains(t, err, "workflow memo name is required")
	require.NotErrorIs(t, err, ErrPreparedRunRejected)
	require.Zero(t, eng.sealCalls)
	require.Zero(t, eng.startCalls)
}

func TestPrepareOneShotNormalizesSearchValuesWithoutEngineWork(t *testing.T) {
	eng := &stubEngine{}
	definition := testAgentDefinition("svc.agent", "agent.workflow", "agent.queue", nil, nil)
	client, _ := newPreparedRunTestClient(eng, definition)
	tags := []string{"alpha", "beta"}
	attributes := map[string]any{
		"attempt": int(3),
		"tags":    tags,
	}

	prepared, err := client.PrepareOneShot(
		nil,
		WithRunID("run-1"),
		WithSearchAttributes(attributes),
	)
	require.NoError(t, err)
	require.Zero(t, eng.sealCalls)
	require.Zero(t, eng.startCalls)
	attributes["attempt"] = int(9)
	tags[0] = "changed"
	data, err := prepared.MarshalBinary()
	require.NoError(t, err)
	parsed, err := ParsePreparedRun(data)
	require.NoError(t, err)

	freshClient, _ := newPreparedRunTestClient(eng, definition)
	_, err = freshClient.StartPrepared(t.Context(), parsed)
	require.NoError(t, err)
	require.Equal(t, int64(3), eng.last.SearchAttributes["attempt"])
	require.Equal(t, []string{"alpha", "beta"}, eng.last.SearchAttributes["tags"])
	require.Empty(t, eng.last.Input.SessionID)
}

func TestPreparedOneShotRunIDSurvivesStorageRoundTrip(t *testing.T) {
	client, _ := newPreparedRunTestClient(&stubEngine{}, testAgentDefinition(
		"svc.agent", "agent.workflow", "agent.queue", nil, nil,
	))
	prepared, err := client.PrepareOneShot(nil)
	require.NoError(t, err)
	require.NotEmpty(t, prepared.RunID())

	data, err := prepared.MarshalBinary()
	require.NoError(t, err)
	parsed, err := ParsePreparedRun(data)
	require.NoError(t, err)
	require.Equal(t, prepared.RunID(), parsed.RunID())
}

func TestPreparedRunIDRejectsInvalidReceiver(t *testing.T) {
	for _, prepared := range []*PreparedRun{nil, {}} {
		require.PanicsWithValue(t, "runtime: prepared run is required", func() {
			prepared.RunID()
		})
	}
}

func TestPreparedRunMarshalBinaryRejectsInvalidReceiver(t *testing.T) {
	for _, prepared := range []*PreparedRun{nil, {}} {
		_, err := prepared.MarshalBinary()
		require.ErrorIs(t, err, ErrPreparedRunRejected)
		require.ErrorContains(t, err, "prepared run is required")
	}
}

func TestPreparedRunStorageFailureDoesNotChangeStartRequest(t *testing.T) {
	eng := &stubEngine{}
	// Many empty memo entries fit the engine request limit, but the stored JSON
	// also records field names and syntax and therefore exceeds its larger limit.
	workflow := strings.Repeat("\x01", 170_000)
	queue := strings.Repeat("\x01", 170_000)
	client, _ := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.agent", workflow, queue, nil, nil,
	))
	empty := converter.NewRawValue(&commonpb.Payload{})
	memo := make(map[string]any, 99_000)
	for index := range 99_000 {
		memo[fmt.Sprintf("%06d", index)] = empty
	}
	prepared, err := client.PrepareOneShot(nil, WithMemo(memo), WithTaskQueue(queue))
	require.NoError(t, err)

	_, err = prepared.MarshalBinary()
	require.ErrorIs(t, err, ErrPreparedRunRejected)
	require.ErrorContains(t, err, "prepared run exceeds maximum stored size")
	_, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	require.Equal(t, prepared.RunID(), eng.last.ID)
	_, err = prepared.MarshalBinary()
	require.ErrorIs(t, err, ErrPreparedRunRejected)

	_, err = client.Start(
		t.Context(), "session-1", nil,
		WithRunID("run-2"), WithMemo(memo), WithTaskQueue(queue),
	)
	require.NoError(t, err)
	require.Equal(t, 2, eng.startCalls)
}

func TestPreparedRunReportsActivationFailureAsStartFailure(t *testing.T) {
	eng := &stubEngine{sealErrors: []error{errors.New("worker unavailable"), nil}}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.agent", "agent.workflow", "agent.queue", nil, nil,
	))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	prepared, err := client.Prepare("session-1", nil, WithRunID("run-1"))
	require.NoError(t, err)
	require.Zero(t, eng.sealCalls)

	_, err = client.StartPrepared(t.Context(), prepared)
	require.ErrorIs(t, err, ErrWorkflowStartFailed)
	require.NotErrorIs(t, err, ErrPreparedRunRejected)
	require.ErrorContains(t, err, "worker unavailable")
	require.Zero(t, eng.startCalls)

	_, err = client.StartPrepared(t.Context(), prepared)
	require.NoError(t, err)
	require.Equal(t, 2, eng.sealCalls)
	require.Equal(t, 1, eng.startCalls)
}

func TestPreparedRunRejectsAnotherAgent(t *testing.T) {
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.agent", "agent.workflow", "agent.queue", nil, nil,
	))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	prepared, err := client.Prepare("session-1", nil, WithRunID("run-1"))
	require.NoError(t, err)
	other, _ := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.other", "other.workflow", "other.queue", nil, nil,
	))

	_, err = other.StartPrepared(t.Context(), prepared)
	require.ErrorIs(t, err, ErrPreparedRunRejected)
	require.ErrorContains(t, err, `belongs to agent "svc.agent", not "svc.other"`)
	require.Zero(t, eng.startCalls)
}

func TestPreparedRunRejectsChangedAgentDefinition(t *testing.T) {
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.agent", "agent.workflow", "agent.queue", nil, nil,
	))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	prepared, err := client.Prepare("session-1", nil, WithRunID("run-1"))
	require.NoError(t, err)
	changed, _ := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.agent", "agent.workflow", "changed.queue", nil, nil,
	))

	_, err = changed.StartPrepared(t.Context(), prepared)
	require.ErrorIs(t, err, ErrPreparedRunRejected)
	require.ErrorContains(t, err, "does not match its run input and agent definition")
	require.Zero(t, eng.startCalls)
}

func TestParsePreparedRunRejectsInvalidBytes(t *testing.T) {
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, testAgentDefinition(
		"svc.agent", "agent.workflow", "agent.queue", nil, nil,
	))
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	prepared, err := client.Prepare("session-1", nil, WithRunID("run-1"))
	require.NoError(t, err)
	valid, err := prepared.MarshalBinary()
	require.NoError(t, err)

	tests := []struct {
		name      string
		data      []byte
		wantError string
	}{
		{name: "malformed", data: []byte(`{"version":`), wantError: "decode prepared run"},
		{
			name: "version",
			data: []byte(strings.Replace(
				string(valid), "goa-ai-prepared-run-v1", "goa-ai-prepared-run-v2", 1,
			)),
			wantError: "unsupported version",
		},
		{name: "trailing", data: append(append([]byte(nil), valid...), []byte(" true")...), wantError: "trailing data"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParsePreparedRun(test.data)
			require.ErrorIs(t, err, ErrPreparedRunRejected)
			require.ErrorContains(t, err, test.wantError)
		})
	}
}

func TestPreparedRunKeepsExactDuplicateIdentity(t *testing.T) {
	eng := engineinmem.New()
	received := make(chan map[string]any, 1)
	require.NoError(t, eng.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(_ engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
			received <- input.Metadata
			return &RunOutput{RunID: "run-1"}, nil
		},
	}))
	definition := testAgentDefinition(
		"svc.agent", "agent.workflow", "agent.queue", nil, nil,
	)
	client, store := newPreparedRunTestClient(eng, definition)
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	original, err := client.Prepare(
		"session-1",
		[]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "first"}}}},
		WithRunID("run-1"),
		WithTaskQueue("custom.queue"),
		WithMetadata(map[string]any{
			"request": map[string]any{
				"approved": true,
				"labels":   []any{"alpha", "beta"},
			},
		}),
		WithMemo(map[string]any{"details": preparedMemoValue{Name: "original"}}),
		WithSearchAttributes(map[string]any{"Attempt": int(3)}),
	)
	require.NoError(t, err)
	originalBytes, err := original.MarshalBinary()
	require.NoError(t, err)
	parsed, err := ParsePreparedRun(originalBytes)
	require.NoError(t, err)

	// A caller-only runtime reconstructed after preparation does not need the
	// source runtime's in-memory state to submit the stored request.
	freshClient, _ := newPreparedRunTestClient(eng, definition)
	handle, err := freshClient.StartPrepared(t.Context(), parsed)
	require.NoError(t, err)
	_, err = handle.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, map[string]any{
		"request": map[string]any{
			"approved": true,
			"labels":   []any{"alpha", "beta"},
		},
	}, <-received)
	retry, err := ParsePreparedRun(originalBytes)
	require.NoError(t, err)
	_, err = freshClient.StartPrepared(t.Context(), retry)
	require.NoError(t, err)

	changed, err := client.Prepare(
		"session-1",
		[]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "changed"}}}},
		WithRunID("run-1"),
	)
	require.NoError(t, err)
	changedBytes, err := changed.MarshalBinary()
	require.NoError(t, err)
	changedParsed, err := ParsePreparedRun(changedBytes)
	require.NoError(t, err)
	_, err = freshClient.StartPrepared(t.Context(), changedParsed)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
	require.ErrorIs(t, err, ErrWorkflowStartFailed)
}

func TestParsedContinuationStartsWithoutPredecessorStorage(t *testing.T) {
	spec := newAnyJSONSpec("svc.lookup")
	definition := testAgentDefinition("svc.agent", "agent.workflow", "agent.queue", []tools.ToolSpec{spec}, nil)
	sourceClient, sourceStore := newPreparedRunTestClient(&stubEngine{}, definition)
	require.NoError(t, createPreparedRunSession(t.Context(), sourceStore))
	suspension := suspensionContractFixtureWithContext(
		t, spec.Name, "svc.agent", "run-1", nil, nil,
	)
	now := time.Now().UTC()
	admitRunForTest(t, sourceStore, session.RunMeta{
		AgentID: "svc.agent", RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning, StartedAt: now, UpdatedAt: now,
	})
	suspensionData, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), sourceStore, "run-1", session.RunSuspension{
		ID: suspension.ID, Data: suspensionData,
	}))
	prepared, err := sourceClient.PrepareContinuation(
		t.Context(), "session-1", "run-1", "run-2", "turn-2",
		&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
			ID: "clarification-1", Answer: "Building A",
		}},
		WorkflowOptions{},
	)
	require.NoError(t, err)
	data, err := prepared.MarshalBinary()
	require.NoError(t, err)
	parsed, err := ParsePreparedRun(data)
	require.NoError(t, err)

	freshEngine := engineinmem.New()
	received := make(chan *RunInput, 1)
	require.NoError(t, freshEngine.RegisterWorkflow(t.Context(), engine.WorkflowDefinition{
		Name: "agent.workflow",
		Handler: func(_ engine.WorkflowContext, input *RunInput) (*RunOutput, error) {
			received <- input
			return &RunOutput{RunID: input.RunID}, nil
		},
	}))
	freshClient, freshStore := newPreparedRunTestClient(freshEngine, definition)
	_, err = freshStore.LoadRun(t.Context(), "run-1")
	require.ErrorIs(t, err, session.ErrRunNotFound)
	handle, err := freshClient.StartPrepared(t.Context(), parsed)
	require.NoError(t, err)
	output, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.Equal(t, "run-2", output.RunID)
	input := <-received
	require.NotNil(t, input.Continuation)
	require.Equal(t, suspension.ID, input.Continuation.Suspension.ID)
}

// newPreparedRunTestClient creates a runtime with the supplied engine and
// returns a caller client bound to definition plus the runtime's empty store.
func newPreparedRunTestClient(workflowEngine engine.Engine, definition AgentDefinition) (AgentClient, *testStore) {
	store := newTestStore()
	runtime := New(store, WithEngine(workflowEngine))
	return runtime.MustClientFor(definition), store
}

// createPreparedRunSession creates the session required by sessionful tests.
func createPreparedRunSession(ctx context.Context, store *testStore) error {
	_, err := createSessionForTest(ctx, store, "session-1")
	return err
}

// decodePreparedMemo decodes one raw memo payload for assertions about the
// value eventually submitted to the engine.
func decodePreparedMemo[T any](t *testing.T, values map[string]engine.EncodedValue, name string) T {
	t.Helper()
	var value T
	require.NoError(t, workflowcodec.NewDataConverter().FromPayload(startrecipe.MemoPayload(values[name]), &value))
	return value
}
