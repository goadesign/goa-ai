// A registry consumer resumes provider-owned input without importing the
// provider's tools. These tests preserve validation before successor creation.
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRegistryContinuationValidatesPrivateChildWithoutConsumerDefinition(t *testing.T) {
	call := testNativeRegistryCall(t, "revision/1")
	call.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))}
	call.RunID = "parent-run"
	call.SessionID = "session-1"
	child := suspensionContractFixtureWithContext(t, "private.lookup", "generic.agent", "child-run", nil, nil)
	rewriteSuspensionCheckpoint(t, child, func(saved *workflowCheckpoint) {
		saved.Context.ParentRunID = call.RunID
		saved.Context.ParentAgentID = "parent.agent"
		saved.Context.ParentToolCallID = call.ToolCallID
		saved.Context.Tool = call.Name
		saved.Context.ToolArgs = call.Payload
		saved.Context.ToolRegistry = call.Registry.Clone()
	})
	checkpoint := &workflowCheckpoint{
		Pending: []checkpointPendingInput{{Child: &checkpointChildContinuation{
			ToolCallID: call.ToolCallID, Suspension: child,
		}}},
		Batch: checkpointStepBatch{Records: []checkpointToolRecord{{Call: call, ChildSuspension: child}}},
	}
	consumer := testAgentDefinition("parent.agent", "parent.workflow", "parent.queue", nil, nil).
		WithRegistryTools(testRegistrySources{})
	response := &api.PendingInputResponse{Clarification: &api.ClarificationAnswer{
		ID: "clarification-1", Answer: "Unit 7",
	}}
	require.NoError(t, validateContinuationResponse(checkpoint, response, consumer))
	found, err := checkpointChildContainsRun(call, child, consumer, "child-run")
	require.NoError(t, err)
	assert.True(t, found)
	found, err = checkpointChildContainsRun(call, child, consumer, "unrelated-run")
	require.NoError(t, err)
	assert.False(t, found)
	provider := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	_, err = decodeWorkflowCheckpoint(child, provider)
	require.ErrorContains(t, err, `requires tool "private.lookup"`)
	provider = testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{
		newAnyJSONSpec("private.lookup"),
	}, nil)
	_, err = decodeWorkflowCheckpoint(child, provider)
	require.NoError(t, err)
}

func TestProviderContinuationValidatesRetainedResultContract(t *testing.T) {
	for _, test := range []struct {
		name      string
		contract  checkpointResponseContract
		result    rawjson.Message
		bounds    *agent.Bounds
		wantError string
	}{
		{name: "exact integer", contract: checkpointResponseContract{ResultSchema: rawjson.Message(`{"type":"integer"}`)}, result: rawjson.Message(`9007199254740993`)},
		{name: "wrong type", contract: checkpointResponseContract{ResultSchema: rawjson.Message(`{"type":"integer"}`)}, result: rawjson.Message(`"wrong"`), wantError: "does not satisfy"},
		{name: "no result"},
		{name: "unexpected result", result: rawjson.Message(`1`), wantError: "does not define a result"},
		{name: "null result", contract: checkpointResponseContract{ResultSchema: rawjson.Message(`{}`)}, result: rawjson.Message(`null`), wantError: "decoded to nil"},
		{name: "missing bounds", contract: checkpointResponseContract{ResultSchema: rawjson.Message(`{"type":"integer"}`), Bounds: &tools.BoundsSpec{}}, result: rawjson.Message(`1`), wantError: "without bounds"},
		{name: "complete bounded result", contract: checkpointResponseContract{ResultSchema: rawjson.Message(`{"type":"integer"}`), Bounds: &tools.BoundsSpec{}}, result: rawjson.Message(`1`), bounds: &agent.Bounds{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			suspension := externalResultsSuspensionFixture(t, "private.answer")
			rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
				checkpoint.Pending[0].ResponseContracts = map[tools.Ident]checkpointResponseContract{"private.answer": test.contract}
			})
			checkpoint, err := decodeWorkflowCheckpointState(suspension)
			require.NoError(t, err)
			response := &api.PendingInputResponse{ToolResults: &api.ToolResultsSet{
				ID: "external-1", Results: []*api.ProvidedToolResult{{Name: "private.answer", ToolCallID: "call-1", Success: &api.ProvidedToolSuccess{Result: test.result, Bounds: test.bounds}}},
			}}
			err = validateProviderContinuationResponse(checkpoint, response)
			if test.wantError != "" {
				assert.ErrorContains(t, err, test.wantError)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestRetainPendingResponseContractsUsesOnlyRequestedTools(t *testing.T) {
	spec := newAnyJSONSpec("private.answer")
	spec.Result.Schema = rawjson.Message(`{"type":"integer"}`)
	definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec, newAnyJSONSpec("private.unrequested")}, nil)
	suspension := externalResultsSuspensionFixture(t, spec.Name)
	checkpoint, err := decodeWorkflowCheckpointState(suspension)
	require.NoError(t, err)
	require.NoError(t, retainPendingResponseContracts(checkpoint.Pending, definition.spec))
	assert.Len(t, checkpoint.Pending[0].ResponseContracts, 1)
	assert.Equal(t, spec.Result.Schema, checkpoint.Pending[0].ResponseContracts[spec.Name].ResultSchema)
	spec.Result.Schema[0] = 'X'
	assert.Equal(t, byte('{'), checkpoint.Pending[0].ResponseContracts[spec.Name].ResultSchema[0])
}

func TestRegistryCancellationFollowsProviderPrivateGrandchild(t *testing.T) {
	call := testNativeRegistryCall(t, "revision/1")
	call.Subject = genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("parent.agent"))}
	call.RunID, call.SessionID = "parent-run", "session-1"
	leaf := suspensionContractFixtureWithContext(t, "private.lookup", "leaf.agent", "leaf-run", nil, nil)
	operation := newAnyJSONSpec("private.delegate")
	operation.IsAgentTool, operation.AgentID = true, "leaf.agent"
	child := nestedChildSuspensionFixture(t, "generic.agent", "child-run", operation, leaf)
	rewriteSuspensionCheckpoint(t, child, func(saved *workflowCheckpoint) {
		saved.Context.ParentRunID = call.RunID
		saved.Context.ParentAgentID = "parent.agent"
		saved.Context.ParentToolCallID = call.ToolCallID
		saved.Context.Tool = call.Name
		saved.Context.ToolArgs = call.Payload
		saved.Context.ToolRegistry = call.Registry.Clone()
	})
	consumer := testAgentDefinition("parent.agent", "parent.workflow", "parent.queue", nil, nil).
		WithRegistryTools(testRegistrySources{})
	found, err := checkpointChildContainsRun(call, child, consumer, "leaf-run")
	require.NoError(t, err)
	assert.True(t, found)
	found, err = checkpointChildContainsRun(call, child, consumer, "other-session-run")
	require.NoError(t, err)
	assert.False(t, found)
	checkpoint, err := decodeProviderChildCheckpoint(call, child)
	require.NoError(t, err)
	assert.NoError(t, validateProviderContinuationResponse(checkpoint, &api.PendingInputResponse{
		Clarification: &api.ClarificationAnswer{ID: "clarification-1", Answer: "Unit 7"},
	}))
	rewriteSuspensionCheckpointAndPublic(t, child, func(saved *workflowCheckpoint) {
		saved.Batch.Records[0].Call.SessionID = "other-session"
	})
	_, err = checkpointChildContainsRun(call, child, consumer, "leaf-run")
	assert.ErrorContains(t, err, "does not match parent tool call")
}
