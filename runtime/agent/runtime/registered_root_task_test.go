// Service Task tests verify that saved provider work receives host answers on
// the next sequence while the original invocation and arguments remain fixed.
package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/mcp"
)

func TestRegisteredRootTaskInputContinuation(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	questions := &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)}}}
	zero := int64(0)
	pending, err := api.NewPendingTaskInput("provider-task", &zero, questions)
	require.NoError(t, err)
	var calls uint64
	backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(_ context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
		assert.Equal(t, *invocation.Invocation, input.Invocation)
		assert.Equal(t, invocation.Arguments, []byte(input.OriginalArguments))
		assert.Equal(t, calls, input.ExecutionSequence)
		calls++
		if input.ExecutionSequence == 0 {
			return &api.ToolOutput{PendingExecution: pending}, nil
		}
		id, answers, ok := input.ExecutionContinuation.AsTaskUpdate()
		require.True(t, ok)
		assert.Equal(t, "provider-task", id)
		assert.JSONEq(t, `{"action":"accept","content":{}}`, string(answers["choice"]))
		return &api.ToolOutput{Payload: []byte(`{"value":3}`)}, nil
	}}
	runtime := New(newTestStore(), WithEngine(backend), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
	handle, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil)
	require.NoError(t, err)
	output, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, output.Suspension)
	response := &api.PendingInputResponse{MCP: &api.MCPInputResponse{ToolCallID: "service-root", Responses: map[string]json.RawMessage{"choice": json.RawMessage(`{"action":"accept","content":{}}`)}}}
	handle, err = runtime.StartToolContinuation(t.Context(), "service-root", "successor", response, nil)
	require.NoError(t, err)
	output, err = handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, output.FinalToolResult)
	assert.Equal(t, uint64(2), calls)
	assert.JSONEq(t, `{"value":3}`, string(output.FinalToolResult.Result))
}

func TestRegisteredRootTaskCancellationIsDurable(t *testing.T) {
	resolution, invocation := registeredRootFixture()
	questions := &mcp.InputRequired{Requests: map[string]mcp.InputRequest{"choice": {Method: "elicitation/create", Params: json.RawMessage(`{"message":"Choose","requestedSchema":{"type":"object","properties":{}}}`)}}}
	zero := int64(0)
	pending, err := api.NewPendingTaskInput("provider-task", &zero, questions)
	require.NoError(t, err)
	canceled := make(chan struct{}, 1)
	backend := registeredRootTestEngine{Engine: engineinmem.New(), execute: func(_ context.Context, input *api.ToolInput) (*api.ToolOutput, error) {
		if input.ExecutionSequence == 0 {
			return &api.ToolOutput{PendingExecution: pending}, nil
		}
		id, ok := input.ExecutionContinuation.AsTaskCancel()
		require.True(t, ok)
		assert.Equal(t, "provider-task", id)
		assert.Equal(t, uint64(1), input.ExecutionSequence)
		canceled <- struct{}{}
		acknowledged, err := api.NewPendingTaskWait(id, &zero)
		return &api.ToolOutput{PendingExecution: acknowledged}, err
	}}
	store := newTestStore()
	runtime := New(store, WithEngine(backend), WithToolWorkflowQueue("service-worker"))
	require.NoError(t, runtime.RegisterToolWorkflow(t.Context()))
	handle, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil)
	require.NoError(t, err)
	output, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, output.Suspension)
	require.NoError(t, runtime.CancelRun(t.Context(), CancelRequest{RunID: "service-root", Reason: "user-canceled"}))
	select {
	case <-canceled:
	case <-time.After(2 * time.Second):
		t.Fatal("durable cancellation did not reach the saved provider task")
	}
	require.Eventually(t, func() bool {
		original, err := store.LoadRun(t.Context(), "service-root")
		resolution, continued := original.SuspensionResolution.(session.ContinuedSuspension)
		if err != nil || !continued {
			return false
		}
		successor, err := store.LoadRun(t.Context(), resolution.RunID)
		return err == nil && successor.Status == session.RunStatusCanceled && successor.CancellationReason == "user-canceled"
	}, 2*time.Second, time.Millisecond)
}
