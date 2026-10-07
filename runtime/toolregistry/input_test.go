// These tests check host-input records where registry delivery accepts external
// messages. Completed tool codecs do not interpret these unfinished outcomes.
package toolregistry

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{name: "empty answer id", round: 1, input: &mcp.CallContinuation{InputResponses: map[string]json.RawMessage{"": json.RawMessage(`{}`)}}, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateInputRound(test.round, test.input, test.textOnly)
			if test.wantError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
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
		{name: "empty state", input: &mcp.InputRequired{RequestState: &state}},
		{name: "empty requests", input: &mcp.InputRequired{Requests: map[string]mcp.InputRequest{}}},
		{name: "form", input: &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"question": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)}}}},
		{name: "url", input: &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"consent": {Method: "elicitation/create", Params: json.RawMessage(`{"mode":"url","message":"Authorize","url":"https://example.test/authorize"}`)}}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := ToolResultMessage{RegistrationToken: strings.Repeat("a", 64), ToolUseID: "call", InputRequired: test.input}
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
				assert.ErrorContains(t, ValidateToolResultMessage(mixed), "input-required outcome cannot contain")
			}
		})
	}
	invalid := ToolResultMessage{RegistrationToken: strings.Repeat("a", 64), ToolUseID: "call", InputRequired: &mcp.InputRequired{}}
	assert.ErrorContains(t, ValidateToolResultMessage(invalid), "input_required needs")
}
