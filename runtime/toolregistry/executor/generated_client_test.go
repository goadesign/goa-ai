// These tests pass the generated registry client directly to the executor.
// Original admission and registry-directed overload recovery must send the same
// saved operation, arguments and runtime identity without application conversion.
package executor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/tooloperation"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	gentooloperations "goa.design/goa-ai/registry/gen/tooloperations"
	"goa.design/goa-ai/runtime/agent/api"
	agentsruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/mcp"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
)

func TestExecutorGeneratedClientPreservesExecutionOperations(t *testing.T) {
	state := ""
	input, err := tooloperation.NewInput(&mcp.CallContinuation{RequestState: &state, InputResponses: map[string]json.RawMessage{"": json.RawMessage(`{ "action": "cancel" }`)}})
	require.NoError(t, err)
	get, err := tooloperation.NewTaskGet("")
	require.NoError(t, err)
	update, err := tooloperation.NewTaskUpdate("job / α", map[string]json.RawMessage{})
	require.NoError(t, err)
	cancel, err := tooloperation.NewTaskCancel("")
	require.NoError(t, err)
	for _, test := range []struct {
		name      string
		sequence  uint64
		operation *api.ExecutionContinuation
	}{
		{"original", 0, nil},
		{"input", 1, input},
		{"task get", 2, get},
		{"task update", 3, update},
		{"task cancel", 4, cancel},
	} {
		t.Run(test.name, func(t *testing.T) {
			const toolUseID = "generated-client-call"
			deadline := testResultStreamExpiration(toolregistry.MaxToolCallWait)
			expiration := testResultStreamExpiration(toolregistry.DefaultResultStreamTTL)
			result := &genregistry.CallToolResult{ToolUseID: toolUseID, RegistrationToken: testRegistrationTokenA, ExecutionDeadline: deadline.Format(time.RFC3339Nano), ResultStreamExpiresAt: expiration.Format(time.RFC3339Nano)}
			var admitted *genregistry.CallToolPayload
			var retried *genregistry.RetryToolPayload
			client := &genregistry.Client{
				CallToolEndpoint: func(_ context.Context, value any) (any, error) {
					admitted = value.(*genregistry.CallToolPayload)
					return result, nil
				},
				RetryToolEndpoint: func(_ context.Context, value any) (any, error) {
					retried = value.(*genregistry.RetryToolPayload)
					return result, nil
				},
			}
			stream := &fakeStream{t: t, requiredStart: "0", events: []*streaming.Event{
				{ID: "1-0", EventName: toolregistry.ResultEventKey, Payload: mustJSON(t, toolregistry.NewToolResultRetryMessage(testRegistrationTokenA, toolUseID, toolregistry.ToolRetryReasonProviderOverloaded, toolregistry.ProviderOverloadRetryAfter))},
				{ID: "2-0", EventName: toolregistry.ResultEventKey, Payload: mustJSON(t, toolregistry.NewToolResultMessage(testRegistrationTokenA, toolUseID, json.RawMessage(`{}`)))},
			}}
			exec := newExecutor(t, client, fakePulseClient{streamID: toolregistry.ResultStreamID(toolUseID), stream: stream}, "records", fakeSpecs{spec: &tools.ToolSpec{Name: "records.read"}})
			out, err := exec.Execute(t.Context(), &agentsruntime.ToolCallMeta{RunID: "run", SessionID: "session", ToolCallID: "call", TurnID: "turn", ParentToolCallID: "parent", Labels: map[string]string{"scope": "selected"}}, &agentsruntime.ToolCall{Name: "records.read", Payload: []byte(`{"query":"active"}`), ExecutionSequence: test.sequence, ExecutionContinuation: test.operation})
			require.NoError(t, err)
			require.NotNil(t, out.ToolResult)
			assert.Nil(t, out.ToolResult.Failure)
			require.NotNil(t, admitted)
			require.NotNil(t, retried)
			assert.Equal(t, "records", admitted.Toolset)
			assert.Equal(t, "records.read", admitted.Tool)
			assert.JSONEq(t, `{"query":"active"}`, string(admitted.PayloadJSON))
			assert.Equal(t, toolregistry.WireProtocolVersion, admitted.WireProtocolVersion)
			assert.Equal(t, test.sequence, admitted.Meta.ExecutionSequence)
			assert.Equal(t, "run", admitted.Meta.RunID)
			assert.Equal(t, "session", admitted.Meta.SessionID)
			assert.Equal(t, "call", admitted.Meta.ToolCallID)
			assert.Equal(t, "turn", *admitted.Meta.TurnID)
			assert.Equal(t, "parent", *admitted.Meta.ParentToolCallID)
			assert.Equal(t, map[string]string{"scope": "selected"}, admitted.Meta.Labels)
			if test.operation == nil {
				assert.Nil(t, admitted.Meta.ExecutionContinuation)
			} else {
				encoded, err := gentooloperations.EncodeToolOperationExecutionContinuation(admitted.Meta.ExecutionContinuation)
				require.NoError(t, err)
				var operation api.ExecutionContinuation
				require.NoError(t, json.Unmarshal(encoded, &operation))
				assert.Equal(t, *test.operation, operation)
			}
			assert.Equal(t, testRegistrationTokenA, retried.ExpectedRegistrationToken)
			assert.Equal(t, admitted.Meta, retried.Meta)
			assert.Equal(t, admitted.Toolset, retried.Toolset)
			assert.Equal(t, admitted.Tool, retried.Tool)
			assert.Equal(t, admitted.PayloadJSON, retried.PayloadJSON)
			assert.Equal(t, admitted.WireProtocolVersion, retried.WireProtocolVersion)
		})
	}
}
