// These tests verify that saved unfinished executions select one action, retain
// exact server data and cannot be changed through constructor inputs or accessors.
package tooloperation

import (
	"encoding/json"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gentooloperations "goa.design/goa-ai/registry/gen/tooloperations"
	"goa.design/goa-ai/runtime/mcp"
)

func TestPendingRoundTrip(t *testing.T) {
	state := ""
	hint := int64(math.MaxInt64)
	for _, test := range []struct {
		name string
		make func() (*Pending, error)
	}{
		{"ordinary state", func() (*Pending, error) { return NewPendingInput(&mcp.InputRequired{RequestState: &state}) }},
		{"ordinary empty questions", func() (*Pending, error) {
			return NewPendingInput(&mcp.InputRequired{Requests: map[string]mcp.InputRequest{}})
		}},
		{"Task wait", func() (*Pending, error) { return NewPendingTaskWait("", &hint) }},
		{"Task input", func() (*Pending, error) { return NewPendingTaskInput(" / α", &hint, pendingTestInput()) }},
		{"Task empty questions", func() (*Pending, error) {
			return NewPendingTaskInput("", nil, &mcp.InputRequired{Requests: map[string]mcp.InputRequest{}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pending, err := test.make()
			require.NoError(t, err)
			encoded, err := json.Marshal(pending)
			require.NoError(t, err)
			var decoded Pending
			require.NoError(t, json.Unmarshal(encoded, &decoded))
			assert.Equal(t, *pending, decoded)
			for _, value := range []any{pending, *pending} {
				size, known, err := EncodedJSONSize(value)
				require.NoError(t, err)
				assert.True(t, known)
				assert.Len(t, encoded, size)
			}
		})
	}
}

func TestPendingOwnsMutableData(t *testing.T) {
	hint := int64(17)
	input := pendingTestInput()
	original := append(json.RawMessage(nil), input.Requests[""].Params...)
	pending, err := NewPendingTaskInput("", &hint, input)
	require.NoError(t, err)
	encoded, err := pending.MarshalJSON()
	require.NoError(t, err)
	hint = 99
	request := input.Requests[""]
	request.Params[0] = '!'
	delete(input.Requests, "")
	id, returnedHint, returnedInput, ok := pending.AsTaskInput()
	require.True(t, ok)
	assert.Empty(t, id)
	assert.EqualValues(t, 17, *returnedHint)
	assert.Equal(t, string(original), string(returnedInput.Requests[""].Params))
	*returnedHint = 88
	request = returnedInput.Requests[""]
	request.Params[0] = '!'
	delete(returnedInput.Requests, "")
	encoded[0] = '!'
	fresh, err := pending.MarshalJSON()
	require.NoError(t, err)
	assert.Equal(t, byte('{'), fresh[0])
	_, returnedHint, returnedInput, ok = pending.AsTaskInput()
	require.True(t, ok)
	assert.EqualValues(t, 17, *returnedHint)
	assert.Equal(t, string(original), string(returnedInput.Requests[""].Params))
	_, ordinary := pending.AsInput()
	_, _, waiting := pending.AsTaskWait()
	assert.False(t, ordinary)
	assert.False(t, waiting)
}

func TestPendingRejectsInvalidInputs(t *testing.T) {
	state := ""
	for _, test := range []struct {
		name string
		make func() (*Pending, error)
	}{
		{"missing ordinary input", func() (*Pending, error) { return NewPendingInput(nil) }},
		{"ordinary absence", func() (*Pending, error) { return NewPendingInput(&mcp.InputRequired{}) }},
		{"missing Task input", func() (*Pending, error) { return NewPendingTaskInput("", nil, nil) }},
		{"Task state", func() (*Pending, error) {
			return NewPendingTaskInput("", nil, &mcp.InputRequired{RequestState: &state})
		}},
		{"missing Task questions", func() (*Pending, error) { return NewPendingTaskInput("", nil, &mcp.InputRequired{}) }},
		{"unsupported interaction", func() (*Pending, error) {
			return NewPendingInput(&mcp.InputRequired{Requests: map[string]mcp.InputRequest{"": {Method: "unknown", Params: json.RawMessage(`{}`)}}})
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			pending, err := test.make()
			require.Error(t, err)
			assert.Nil(t, pending)
		})
	}
}

func TestPendingDecodeRejectsMalformedBranches(t *testing.T) {
	for _, raw := range []string{
		`{}`,
		`{"outcome":{"type":"task_wait","value":{}}}`,
		`{"outcome":{"type":"task_wait","value":{"task_id":"","requests":{}}}}`,
		`{"outcome":{"type":"task_input","value":{"task_id":""}}}`,
		`{"outcome":{"type":"input","value":{"requests":null}}}`,
		`{"outcome":{"type":"task_wait","value":{"task_id":"","poll_interval_ms":1.5}}}`,
		`{"outcome":{"type":"task_wait","value":{"task_id":"","task_id":"other"}}}`,
		`{"outcome":{"type":"unknown","value":{}}}`,
	} {
		t.Run(raw, func(t *testing.T) {
			pending, err := NewPendingTaskWait("original", nil)
			require.NoError(t, err)
			require.Error(t, json.Unmarshal([]byte(raw), pending))
			id, _, ok := pending.AsTaskWait()
			assert.True(t, ok)
			assert.Equal(t, "original", id)
		})
	}
	var missing *Pending
	size, known, err := EncodedJSONSize(missing)
	require.NoError(t, err)
	assert.True(t, known)
	assert.Equal(t, len("null"), size)
	_, known, err = EncodedJSONSize(Pending{})
	require.Error(t, err)
	assert.True(t, known)
}

func pendingTestInput() *mcp.InputRequired {
	return &mcp.InputRequired{Requests: map[string]mcp.InputRequest{
		"": {Method: "elicitation/create", Params: json.RawMessage(`{ "message": "Choose", "requestedSchema": {"type":"object","properties":{"choice":{"type":"string"}}} }`)},
	}}
}

// An optional questions object must keep {} distinct from absence through the
// authored Goa tag and generated codec, without another runtime outcome branch.
func TestPendingInputGeneratedPresence(t *testing.T) {
	state := ""
	for _, test := range []struct {
		name  string
		input *gentooloperations.ToolOperationPendingInput
		want  string
	}{
		{"state without questions", &gentooloperations.ToolOperationPendingInput{State: &state}, `{"outcome":{"type":"input","value":{"state":""}}}`},
		{"empty questions", &gentooloperations.ToolOperationPendingInput{Requests: map[string]*gentooloperations.ToolOperationHostRequest{}}, `{"outcome":{"type":"input","value":{"requests":{}}}}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := &gentooloperations.ToolOperationPendingExecution{Outcome: gentooloperations.NewOutcomeInput(test.input)}
			encoded, err := gentooloperations.EncodeToolOperationPendingExecution(value)
			require.NoError(t, err)
			assert.JSONEq(t, test.want, string(encoded))
			decoded, err := gentooloperations.DecodeToolOperationPendingExecution(encoded)
			require.NoError(t, err)
			input, ok := decoded.Outcome.AsInput()
			require.True(t, ok)
			assert.Equal(t, test.input, input)
		})
	}
}
