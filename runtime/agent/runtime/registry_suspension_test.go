package runtime

// These tests resume saved registry calls without a registry client. The saved
// definition owns decoding, while the current agent design owns permission.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRegistrySuspensionUsesSavedContractAndCurrentSource(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	resolved, err := registrycontract.Resolve(testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)))
	require.NoError(t, err)
	binding, err := resolved.Select("company", "records.find")
	require.NoError(t, err)
	suspension := suspensionContractFixtureWithContext(t, "records.find", string(definition.route.ID), "run-1", nil, nil)
	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		call := ToolCall{Name: "records.find", ToolCallID: "call-1",
			Payload: rawjson.Message(`{"value":1}`), Registry: binding}
		checkpoint.Batch.Result.ToolCalls = []ToolCall{call}
		checkpoint.Batch.Calls = []ToolCall{call}
		checkpoint.State.ToolOutputs = []*planner.ToolOutput{{
			Name: "records.find", ToolCallID: "previous", Registry: binding,
			CallRunID: "run-1", ResultRunID: "run-1", Payload: rawjson.Message(`{"value":1}`),
			Result: rawjson.Message(`{"value":9007199254740993}`),
		}}
		checkpoint.State.ToolEvents = []*api.ToolEvent{{
			Name: "records.find", ToolCallID: "previous", Result: rawjson.Message(`{"value":9007199254740993}`),
		}}
		checkpoint.Pending = []checkpointPendingInput{{Confirmation: &checkpointConfirmation{
			ID: "confirm", Call: call, Title: "Confirm change", Prompt: "Change value 1?",
			DeniedResult: rawjson.Message(`{"value":0}`),
		}}}
		checkpoint.RequiredTools = requiredCheckpointToolNames(checkpoint)
		suspension.RequiredTools = checkpoint.RequiredTools
		suspension.Pending, err = publicPendingInputs(checkpoint.Pending)
		require.NoError(t, err)
	})
	checkpoint, err := decodeWorkflowCheckpoint(suspension, definition)
	require.NoError(t, err)
	assert.Empty(t, checkpoint.RequiredTools, "dynamic contracts are already retained with their calls")
	state, err := rt.restoreCheckpointState(checkpoint.State)
	require.NoError(t, err)
	require.Len(t, state.ToolEvents, 1)
	call := ToolCall{Name: "records.find", ToolCallID: "previous", Registry: binding}
	encoded, err := encodeToolEvent(state.ToolEvents[0], call, rt.toolSpec)
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":9007199254740993}`, string(encoded.Result))
	plan, required, err := rt.confirmationPlan(t.Context(), &checkpoint.Pending[0].Confirmation.Call)
	require.NoError(t, err)
	require.True(t, required)
	require.NotNil(t, plan)
	assert.Equal(t, "Change value 1?", plan.Prompt)

	removed := definition
	removed.registryTools = nil
	_, err = decodeWorkflowCheckpoint(suspension, removed)
	require.ErrorContains(t, err, "does not consume registry tools")

	rewriteSuspensionCheckpoint(t, suspension, func(checkpoint *workflowCheckpoint) {
		checkpoint.State.ToolEvents[0].Name = "other.find"
	})
	_, err = decodeWorkflowCheckpoint(suspension, definition)
	require.ErrorContains(t, err, "instead of")
}

func TestUnavailableRegistryCallDropsExecutableSelection(t *testing.T) {
	rt := New(newTestStore())
	call, err := rt.rewriteToolCallUnavailable(ToolCall{
		Name: "records.find", Payload: rawjson.Message(`{"value":1}`),
		Registry: &tools.RegistryBinding{Registry: "company"},
	})
	require.NoError(t, err)
	assert.Equal(t, tools.ToolUnavailable, call.Name)
	assert.Nil(t, call.Registry)
	assert.Equal(t, tools.Ident("records.find"), call.TranscriptName())
}
