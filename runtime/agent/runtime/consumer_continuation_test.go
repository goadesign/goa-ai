// Direct continuation tests exercise independent callers, retained response
// schemas and exact prepared-request recovery without provider-private tools.
package runtime

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/session"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDirectConsumerContinuationRecoversAndStartsWithoutPrivateTools(t *testing.T) {
	suspension, definition := directConsumerSuspension(t)
	eng := &stubEngine{}
	client, store := newPreparedRunTestClient(eng, definition)
	require.NoError(t, createPreparedRunSession(t.Context(), store))
	admitRunForTest(t, store, session.RunMeta{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
			RunExecutorBranchAgent(string(definition.route.ID)))}, RunID: "run-1", SessionID: "session-1",
		Status: session.RunStatusRunning,
	})
	data, err := json.Marshal(suspension)
	require.NoError(t, err)
	require.NoError(t, storeSuspensionForTest(t.Context(), store, "run-1", session.RunSuspension{ID: suspension.ID, Data: data}))
	response := directConsumerResponse(rawjson.Message(`9007199254740993`))
	prepared, err := client.PrepareContinuation(t.Context(), "session-1", "run-1", "run-2", "turn-2", response,
		WorkflowOptions{Memo: map[string]any{"origin": "independent"}})
	require.NoError(t, err)
	assert.Zero(t, eng.startCalls)
	recovered, found, err := client.RecoverPrepared(t.Context(), "session-1", "run-2", "run-2")
	require.NoError(t, err)
	require.True(t, found)
	before, err := prepared.MarshalBinary()
	require.NoError(t, err)
	after, err := recovered.MarshalBinary()
	require.NoError(t, err)
	assert.Equal(t, before, after)
	for range 2 {
		_, err := client.StartPrepared(t.Context(), recovered)
		require.NoError(t, err)
		assert.Equal(t, definition.route.WorkflowName, eng.last.Workflow)
		assert.Equal(t, definition.route.DefaultTaskQueue, eng.last.TaskQueue)
		assert.Equal(t, "independent", decodePreparedMemo[string](t, eng.last.Memo, "origin"))
		actual := eng.last.Input.Continuation.Response.ToolResults
		assert.Equal(t, response.ToolResults.ID, actual.ID)
		require.Len(t, actual.Results, 1)
		assert.Equal(t, "private.answer", string(actual.Results[0].Name))
		assert.Equal(t, "call-1", actual.Results[0].ToolCallID)
		assert.Equal(t, response.ToolResults.Results[0].Success.Result, actual.Results[0].Success.Result)
		assert.Empty(t, actual.Results[0].Blocks)
	}
	assert.Equal(t, 2, eng.startCalls)
	_, err = decodeWorkflowCheckpoint(suspension, definition)
	assert.ErrorContains(t, err, "private.answer")
}

func TestDirectConsumerContinuationRejectsChangedIdentityAndResponse(t *testing.T) {
	for _, variation := range []string{"workflow", "queue", "executor", "tool", "arguments", "parent", "publication", "response", "session", "queue override"} {
		t.Run(variation, func(t *testing.T) {
			suspension, definition := directConsumerSuspension(t)
			response := directConsumerResponse(rawjson.Message(`1`))
			input := &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent(definition.route.ID))}, SessionID: "session-1", RunID: "run-2", TurnID: "turn-2",
				Continuation: &api.RunContinuationInput{Suspension: suspension, Response: response}}
			launch := workflowLaunchSettings{}
			switch variation {
			case "queue override":
				launch.taskQueue = "foreign.queue"
			case "response":
				response.ToolResults.Results[0].Success.Result = rawjson.Message(`"wrong"`)
			case "session":
				input.SessionID = "other-session"
			default:
				rewriteSuspensionCheckpointAndPublic(t, suspension, func(saved *workflowCheckpoint) {
					switch variation {
					case "workflow":
						saved.Context.Operation.ConsumerContract.Agent.Workflow = "foreign.workflow"
					case "queue":
						saved.Context.Operation.ConsumerContract.Agent.Queue = "foreign.queue"
					case "executor":
						saved.Context.Operation.ConsumerContract.Agent.Executor = "foreign.agent"
					case "tool":
						saved.Context.Tool = "foreign.operation"
					case "arguments":
						saved.Context.ToolArgs = rawjson.Message(`{"value":"wrong"}`)
					case "parent":
						saved.Context.ParentRunID = "foreign-run"
					case "publication":
						saved.Context.ToolRegistry = testNativeRegistryCall(t, "revision/1").Registry
					}
				})
			}
			_, err := prepareRunWithDefinition(input, launch, definition, true)
			assert.ErrorIs(t, err, ErrContinuationRejected)
		})
	}
}

// directConsumerSuspension retains a private answer schema and completion policy
// while returning only the operation's worker route to the independent caller.
func directConsumerSuspension(t *testing.T) (*api.RunSuspension, AgentDefinition) {
	t.Helper()
	declaration := testNativeRegistryResolution("revision/1").Toolset.Tools[0]
	target := declaration.ConsumerContract.Agent
	definition := testAgentDefinition("generic.agent", target.Workflow, target.Queue, nil, nil)
	suspension := externalResultsSuspensionFixture(t, "private.answer")
	rewriteSuspensionCheckpointAndPublic(t, suspension, func(saved *workflowCheckpoint) {
		saved.AgentID = string(definition.route.ID)
		saved.Context.Operation = declaration
		saved.Context.Tool = tools.Ident(declaration.Name)
		saved.Context.ToolArgs = rawjson.Message(`{"value":1}`)
		saved.Context.Labels = map[string]string{"tenant_id": "tenant-1"}
		saved.Policy = &PolicyOverrides{CompletionTool: "private.finish"}
		saved.Pending[0].ResponseContracts = map[tools.Ident]checkpointResponseContract{
			"private.answer": {ResultSchema: rawjson.Message(`{"type":"integer"}`)},
		}
		saved.RequiredTools = requiredCheckpointToolNames(saved)
	})
	return suspension, definition
}

// directConsumerResponse supplies an exact result for the saved private request.
// The retained integer schema rejects answers with a different JSON shape.
func directConsumerResponse(result rawjson.Message) *api.PendingInputResponse {
	return &api.PendingInputResponse{ToolResults: &api.ToolResultsSet{
		ID: "external-1", Results: []*api.ProvidedToolResult{{
			Name: "private.answer", ToolCallID: "call-1", Success: &api.ProvidedToolSuccess{Result: result},
		}},
	}}
}
