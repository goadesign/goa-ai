// These tests feed actual event JSON into the executor. Foreign call identity
// still wins over union validation; malformed fixed records still fail parsing.
package executor

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
)

func TestExecutorTerminalEnvelopeIdentityBeforeUnionValidation(t *testing.T) {
	t.Parallel()

	const toolUseID = "terminal-envelope-call"
	for _, test := range []struct {
		name      string
		token     string
		toolUseID string
		wantError bool
	}{
		{name: "foreign admission", token: testRegistrationTokenB, toolUseID: toolUseID},
		{name: "foreign call", token: testRegistrationTokenA, toolUseID: "another-call"},
		{name: "matching invalid union", token: testRegistrationTokenA, toolUseID: toolUseID, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int64
			stream := &fakeStream{
				t:             t,
				requiredStart: "0",
				events: []*streaming.Event{
					{
						ID:        "1-0",
						EventName: toolregistry.ResultEventKey,
						Payload: []byte(fmt.Sprintf(
							`{"registration_token":%q,"tool_use_id":%q,"error":{},"retry":{"reason":"provider_overloaded","retry_after_ms":250}}`,
							test.token, test.toolUseID,
						)),
					},
					{
						ID:        "2-0",
						EventName: toolregistry.ResultEventKey,
						Payload: []byte(fmt.Sprintf(
							`{"registration_token":%q,"tool_use_id":%q}`,
							testRegistrationTokenA, toolUseID,
						)),
					},
				},
			}
			exec := newExecutor(t,
				fakeRegistryClient{toolUseID: toolUseID, calls: &calls},
				fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
				"todos.todos",
				fakeSpecs{spec: &tools.ToolSpec{Name: "queue.update_items"}},
			)
			result, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
				RunID: "run", SessionID: "session", ToolCallID: "call-1",
			}, &agentsruntime.ToolCall{Name: "queue.update_items", Payload: []byte(`{}`)})

			if test.wantError {
				require.ErrorContains(t, err, "retry and error are both set")
				assert.Nil(t, result)
			} else {
				require.NoError(t, err)
				require.NotNil(t, result)
				require.NotNil(t, result.ToolResult)
				assert.Nil(t, result.ToolResult.Result)
				assert.Nil(t, result.ToolResult.Failure)
			}
			assert.Equal(t, int64(1), calls.Load(), "foreign retry must not republish the call")
			assert.Empty(t, stream.acked)
		})
	}
}

func TestExecutorTerminalEnvelopeRejectsUnknownFixedFields(t *testing.T) {
	t.Parallel()

	for _, token := range []string{testRegistrationTokenA, testRegistrationTokenB} {
		const toolUseID = "malformed-envelope-call"
		stream := &fakeStream{
			t:             t,
			requiredStart: "0",
			events: []*streaming.Event{{
				ID:        "1-0",
				EventName: toolregistry.ResultEventKey,
				Payload: []byte(fmt.Sprintf(
					`{"registration_token":%q,"tool_use_id":%q,"server_data":[{"kind":"record","audience":"timeline","data":{},"extra":true}]}`,
					token, toolUseID,
				)),
			}},
		}
		exec := newExecutor(t,
			fakeRegistryClient{toolUseID: toolUseID},
			fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream},
			"todos.todos",
			fakeSpecs{spec: &tools.ToolSpec{Name: "queue.update_items"}},
		)
		result, err := exec.Execute(context.Background(), &agentsruntime.ToolCallMeta{
			RunID: "run", SessionID: "session", ToolCallID: "call-1",
		}, &agentsruntime.ToolCall{Name: "queue.update_items", Payload: []byte(`{}`)})

		require.ErrorContains(t, err, "decode terminal tool result event 1-0")
		require.ErrorContains(t, err, `unknown field "extra"`)
		assert.Nil(t, result)
	}
}
