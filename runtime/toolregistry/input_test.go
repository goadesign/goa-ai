// These tests check host-input records where registry delivery accepts external
// messages. Completed tool codecs do not interpret these unfinished outcomes.
package toolregistry

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/tooloperation"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/mcp"
)

func TestRegistryInputRoundBoundary(t *testing.T) {
	t.Parallel()
	empty := &mcp.CallContinuation{}
	valid := &mcp.CallContinuation{InputResponses: map[string]json.RawMessage{"question": json.RawMessage(`{"action":"cancel"}`)}}
	for _, test := range []struct {
		name      string
		round     uint64
		input     *mcp.CallContinuation
		textOnly  bool
		wantError bool
	}{
		{name: "initial"},
		{name: "initial text only", textOnly: true},
		{name: "empty continuation", round: 1, input: empty},
		{name: "answered continuation", round: 2, input: valid},
		{name: "largest representation", round: ^uint64(0), input: empty},
		{name: "answers on initial", input: valid, wantError: true},
		{name: "missing later continuation", round: 1, wantError: true},
		{name: "text only continuation", round: 1, input: empty, textOnly: true, wantError: true},
		{name: "null answer", round: 1, input: &mcp.CallContinuation{InputResponses: map[string]json.RawMessage{"question": json.RawMessage(`null`)}}, wantError: true},
		{name: "scalar answer", round: 1, input: &mcp.CallContinuation{InputResponses: map[string]json.RawMessage{"question": json.RawMessage(`true`)}}, wantError: true},
		{name: "duplicate answer member", round: 1, input: &mcp.CallContinuation{InputResponses: map[string]json.RawMessage{"question": json.RawMessage(`{"action":"accept","action":"cancel"}`)}}, wantError: true},
		{name: "empty answer id", round: 1, input: &mcp.CallContinuation{InputResponses: map[string]json.RawMessage{"": json.RawMessage(`{"action":"cancel"}`)}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var operation *tooloperation.Continuation
			var err error
			if test.input != nil {
				operation, err = tooloperation.NewInput(test.input)
			}
			if err == nil {
				err = ValidateExecution(test.round, operation, test.textOnly)
			}
			if test.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// Task methods retain their explicit operation even when IDs or answer objects
// are empty. Text-only execution permits observations, but never host answers.
func TestRegistryTaskOperationBoundary(t *testing.T) {
	get, err := tooloperation.NewTaskGet("")
	require.NoError(t, err)
	cancel, err := tooloperation.NewTaskCancel("")
	require.NoError(t, err)
	update, err := tooloperation.NewTaskUpdate("", map[string]json.RawMessage{})
	require.NoError(t, err)
	missing, err := tooloperation.NewTaskUpdate("", nil)
	require.Error(t, err)
	assert.Nil(t, missing)
	for _, test := range []struct {
		name      string
		operation *tooloperation.Continuation
		textOnly  bool
		wantError bool
	}{
		{name: "read", operation: get},
		{name: "cancel", operation: cancel},
		{name: "empty answers", operation: update},
		{name: "text only read", operation: get, textOnly: true},
		{name: "text only cancel", operation: cancel, textOnly: true},
		{name: "text only answers", operation: update, textOnly: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateExecution(1, test.operation, test.textOnly)
			if test.wantError {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			assert.Error(t, ValidateExecution(0, test.operation, test.textOnly))
		})
	}
}

func TestRegistryRequiredInputOutcomeBoundary(t *testing.T) {
	t.Parallel()
	state := ""
	for _, test := range []struct {
		name  string
		input *mcp.InputRequired
	}{
		{name: "empty request id", input: &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)}}}},
		{name: "empty state", input: &mcp.InputRequired{RequestState: &state}},
		{name: "empty requests", input: &mcp.InputRequired{Requests: map[string]mcp.InputRequest{}}},
		{name: "form", input: &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"question": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)}}}},
		{name: "url", input: &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"consent": {Method: "elicitation/create", Params: json.RawMessage(`{"mode":"url","message":"Authorize","url":"https://example.test/authorize"}`)}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			message, err := NewInputRequiredResult(strings.Repeat("a", 64), "call", test.input)
			require.NoError(t, err)
			raw, err := json.Marshal(message)
			require.NoError(t, err)
			decoded, err := DecodeToolResultMessage(raw)
			require.NoError(t, err)
			require.NoError(t, ValidateToolResultMessage(decoded))
			assert.Equal(t, message, decoded)
			require.ErrorContains(t, ValidateTextOnlyResult(decoded), "host input")
			for _, mutate := range []func(*ToolResultMessage){
				func(m *ToolResultMessage) { m.Result = json.RawMessage(`{}`) },
				func(m *ToolResultMessage) { m.Error = &ToolError{} },
				func(m *ToolResultMessage) { m.Retry = &ToolRetry{} },
				func(m *ToolResultMessage) { m.Bounds = &agent.Bounds{} },
				func(m *ToolResultMessage) { m.ServerData = []*ServerDataItem{{}} },
			} {
				mixed := decoded
				mutate(&mixed)
				assert.ErrorContains(t, ValidateToolResultMessage(mixed), "unfinished outcome cannot contain")
			}
		})
	}
	_, err := NewInputRequiredResult(strings.Repeat("a", 64), "call", &mcp.InputRequired{})
	require.ErrorContains(t, err, "input_required needs inputRequests or requestState")
}
