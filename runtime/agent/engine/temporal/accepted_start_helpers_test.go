package temporal

// These fixtures submit requests through the engine and a local Temporal gRPC
// service. SDK tests consume the resulting input and memo, not invented proof.

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"

	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
)

// acceptedStartForTest captures the exact request accepted through the normal
// engine and generated Temporal client. It does not execute a workflow.
func acceptedStartForTest(t *testing.T, name, queue string, input *api.RunInput) *workflowservice.StartWorkflowExecutionRequest {
	t.Helper()
	service := &testWorkflowService{}
	eng := &Engine{client: newWorkflowServiceClient(t, service)}
	_, err := eng.StartWorkflow(t.Context(), engine.WorkflowStartRequest{
		ID: input.RunID, Workflow: name, TaskQueue: queue, Input: input,
	})
	require.NoError(t, err)
	accepted := service.startRequest()
	require.NotNil(t, accepted)
	return accepted
}

// prepareAcceptedTestWorkflow installs the actual accepted memo and returns the
// accepted input for a new SDK fixture. It never updates a saved history.
func prepareAcceptedTestWorkflow(t *testing.T, env *testsuite.TestWorkflowEnvironment, name, queue string, input *api.RunInput) *api.RunInput {
	t.Helper()
	accepted := acceptedStartForTest(t, name, queue, input)
	var owned *api.RunInput
	require.NoError(t, NewAgentDataConverter().FromPayloads(accepted.Input, &owned))
	digest := decodePayload[[]byte](t, accepted.Memo.Fields[workflowStartRecipeMemoKey])
	env.SetStartWorkflowOptions(client.StartWorkflowOptions{ID: accepted.WorkflowId, TaskQueue: queue})
	require.NoError(t, env.SetMemoOnStart(map[string]any{workflowStartRecipeMemoKey: digest}))
	return owned
}

// installFirstAttemptRecoveryControl supplies one field the SDK fixture omits.
// These existing child-input tests run only a first attempt. Retry and
// continuation identity are verified against a real server in separate tests.
func installFirstAttemptRecoveryControl(
	t *testing.T, env *testsuite.TestWorkflowEnvironment, eng *Engine, opts worker.Options,
) {
	t.Helper()
	opts.Interceptors = append(opts.Interceptors, &workflowControlInterceptor{engine: eng})
	env.SetWorkerOptions(opts)
	env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
		require.EqualValues(t, 1, info.Attempt)
		require.Empty(t, info.FirstRunID, "remove fixture field when the SDK supplies it")
		info.FirstRunID = info.WorkflowExecution.RunID
	})
}
