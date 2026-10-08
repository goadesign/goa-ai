// These tests check the saved operation boundary and the ownership of mutable
// host answers. A later accessor cannot change an already admitted operation.
package tooloperation

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	genregistryclient "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/mcp"
)

func TestContinuationRoundTrip(t *testing.T) {
	state := ""
	input, err := NewInput(&mcp.CallContinuation{RequestState: &state, InputResponses: map[string]json.RawMessage{"": json.RawMessage(`{ "action": "cancel" }`)}})
	require.NoError(t, err)
	get, err := NewTaskGet("")
	require.NoError(t, err)
	update, err := NewTaskUpdate("job / α", map[string]json.RawMessage{"": json.RawMessage(`{}`)})
	require.NoError(t, err)
	empty, err := NewTaskUpdate("", map[string]json.RawMessage{})
	require.NoError(t, err)
	cancel, err := NewTaskCancel("")
	require.NoError(t, err)
	for _, test := range []struct {
		name      string
		operation *Continuation
	}{
		{name: "input", operation: input},
		{name: "task get", operation: get},
		{name: "task update", operation: update},
		{name: "empty task answers", operation: empty},
		{name: "task cancel", operation: cancel},
	} {
		t.Run(test.name, func(t *testing.T) {
			data, err := json.Marshal(test.operation)
			require.NoError(t, err)
			var restored Continuation
			require.NoError(t, json.Unmarshal(data, &restored))
			assert.Equal(t, *test.operation, restored)
		})
	}
}

func TestContinuationOwnsAnswers(t *testing.T) {
	state := "original"
	answer := json.RawMessage(`{ "choice": "yes" }`)
	answers := map[string]json.RawMessage{"": answer}
	input, err := NewInput(&mcp.CallContinuation{RequestState: &state, InputResponses: answers})
	require.NoError(t, err)
	update, err := NewTaskUpdate("job", answers)
	require.NoError(t, err)
	state = "changed"
	answer[0] = '['
	delete(answers, "")
	for _, operation := range []*Continuation{input, update} {
		var received map[string]json.RawMessage
		if value, ok := operation.AsInput(); ok {
			assert.Equal(t, "original", *value.RequestState)
			*value.RequestState = "accessor mutation"
			received = value.InputResponses
		} else {
			id, value, ok := operation.AsTaskUpdate()
			require.True(t, ok)
			assert.Equal(t, "job", id)
			received = value
		}
		assert.Equal(t, json.RawMessage(`{ "choice": "yes" }`), received[""]) //nolint:testifylint // Host answer copies preserve the exact submitted bytes.
		received[""][0] = '['
		delete(received, "")
		require.NoError(t, operation.Validate())
	}
	value, ok := input.AsInput()
	require.True(t, ok)
	assert.Equal(t, "original", *value.RequestState)
	assert.Equal(t, json.RawMessage(`{ "choice": "yes" }`), value.InputResponses[""]) //nolint:testifylint // Host answer copies preserve the exact submitted bytes.
	_, valueAnswers, ok := update.AsTaskUpdate()
	require.True(t, ok)
	assert.Equal(t, json.RawMessage(`{ "choice": "yes" }`), valueAnswers[""]) //nolint:testifylint // Host answer copies preserve the exact submitted bytes.
}

func TestContinuationRejectsInvalidSavedOperations(t *testing.T) {
	for _, data := range []string{
		`null`, `{}`, `{"operation":null}`,
		`{"Operation":{"type":"task_get","value":"job"}}`,
		`{"operation":{"type":"unknown","value":"job"}}`,
		`{"operation":{"type":"task_get"}}`,
		`{"operation":{"type":"task_get","value":null}}`,
		`{"operation":{"type":"task_get","value":42}}`,
		`{"operation":{"type":"task_get","value":"job","responses":{}}}`,
		`{"operation":{"type":"input","value":{"task_id":"job"}}}`,
		`{"operation":{"type":"task_update","value":{"responses":{}}}}`,
		`{"operation":{"type":"task_update","value":{"task_id":"job"}}}`,
		`{"operation":{"type":"task_update","value":{"task_id":"job","responses":null}}}`,
		`{"operation":{"type":"task_update","value":{"task_id":"job","responses":[]}}}`,
		`{"operation":{"type":"task_update","value":{"task_id":"job","responses":{"key":"not base64"}}}}`,
		`{"operation":{"type":"task_get","type":"task_cancel","value":"job"}}`,
		`{"operation":{"type":"task_cancel","value":false}}`,
	} {
		t.Run(data, func(t *testing.T) {
			operation, err := NewTaskGet("retained")
			require.NoError(t, err)
			require.Error(t, json.Unmarshal([]byte(data), operation))
			id, ok := operation.AsTaskGet()
			assert.True(t, ok)
			assert.Equal(t, "retained", id)
		})
	}
}

func TestContinuationRejectsInvalidAnswerObjects(t *testing.T) {
	for _, data := range []string{`null`, `true`, `[]`, `{`, `{"action":"accept","action":"cancel"}`} {
		t.Run(data, func(t *testing.T) {
			answers := map[string]json.RawMessage{"": json.RawMessage(data)}
			input, err := NewInput(&mcp.CallContinuation{InputResponses: answers})
			require.Error(t, err)
			assert.Nil(t, input)
			update, err := NewTaskUpdate("", answers)
			require.Error(t, err)
			assert.Nil(t, update)
		})
	}
	require.Error(t, (Continuation{}).Validate())
	operation, err := NewTaskUpdate("", nil)
	require.Error(t, err)
	assert.Nil(t, operation)
}

// An empty update must survive a real protobuf serialization, not only the Go
// transforms. The operation branch keeps it distinct from a Task observation.
func TestTaskUpdateSurvivesRegistryProtobuf(t *testing.T) {
	update, err := NewTaskUpdate("", map[string]json.RawMessage{})
	require.NoError(t, err)
	input := &genregistry.CallResolvedToolPayload{
		Toolset: "remote.tools", Tool: "lookup", PayloadJSON: []byte(`{}`),
		ExpectedRegistrationToken: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		WireProtocolVersion:       12, Meta: &genregistry.ToolCallMeta{
			RunID: "run", SessionID: "session", ToolCallID: "call",
			ExecutionSequence:     1,
			ExecutionContinuation: Value(update),
		}}
	request := genregistryclient.NewProtoCallResolvedToolRequest(input)
	data, err := proto.Marshal(request)
	require.NoError(t, err)
	var restored genregistrypb.CallResolvedToolRequest
	require.NoError(t, proto.Unmarshal(data, &restored))
	require.NoError(t, genregistryserver.ValidateCallResolvedToolRequest(&restored))
	payload := genregistryserver.NewCallResolvedToolPayload(&restored)
	operation, err := FromValue(payload.Meta.ExecutionContinuation)
	require.NoError(t, err)
	id, answers, ok := operation.AsTaskUpdate()
	assert.True(t, ok)
	assert.Empty(t, id)
	assert.Equal(t, map[string]json.RawMessage{}, answers)
}

// The JSON used by size guards is the same immutable JSON the workflow saves,
// including escaped strings. Returning its bytes must not expose saved state.
func TestContinuationEncodedSizeOwnsBytes(t *testing.T) {
	operation, err := NewTaskGet("\"<&\x00\nα\u2028")
	require.NoError(t, err)
	data, err := json.Marshal(operation)
	require.NoError(t, err)
	for _, value := range []any{operation, *operation} {
		size, known, err := EncodedJSONSize(value)
		require.NoError(t, err)
		assert.True(t, known)
		assert.Equal(t, len(data), size)
	}
	returned, err := operation.MarshalJSON()
	require.NoError(t, err)
	clear(returned)
	retained, err := json.Marshal(operation)
	require.NoError(t, err)
	assert.Equal(t, data, retained)
	var absent *Continuation
	size, known, err := EncodedJSONSize(absent)
	require.NoError(t, err)
	assert.True(t, known)
	assert.Equal(t, len("null"), size)
	_, known, err = EncodedJSONSize(struct{}{})
	require.NoError(t, err)
	assert.False(t, known)
	_, known, err = EncodedJSONSize(Continuation{})
	assert.True(t, known)
	require.Error(t, err)
}
