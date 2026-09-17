package runtime

// Registry pagination retains the registration selected by each query. Equal
// tool names or cursors cannot mix two queries started under different tokens.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestRegistryContinuationsKeepIndependentRegistrations(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	outputs := make([]*planner.ToolOutput, 0, 2)
	for index, token := range []string{strings.Repeat("a", 64), strings.Repeat("b", 64)} {
		resolved, err := registrycontract.Resolve(testRegistryPagingResolution(token))
		require.NoError(t, err)
		binding, err := resolved.Select("company", "records.find")
		require.NoError(t, err)
		id := []string{"first-query", "second-query"}[index]
		output := sourceContinuationOutput("records.find", id, `{"value":1}`, "same-cursor")
		output.Registry = binding
		outputs = append(outputs, output)
	}
	actions, err := rt.availableContinuationActions(definition.route.ID, outputs)
	require.NoError(t, err)
	require.Len(t, actions, 2)
	assert.NotEqual(t, actions[0].modelName, actions[1].modelName)
	for index, action := range actions {
		assert.NotContains(t, action.description, "same-cursor")
		assert.Contains(t, action.description, `{"value":1}`)
		calls, err := rt.compilePlannerToolCalls([]planner.ToolRequest{{
			Name: action.modelName, ModelToolCallID: "model", Payload: rawjson.Message(`{}`),
		}}, actions, map[string]model.ToolCall{
			"model": {ID: "model", Name: action.modelName, Payload: rawjson.Message(`{}`)},
		})
		require.NoError(t, err)
		require.Len(t, calls, 1)
		retained, err := registrycontract.Read(calls[0].Registry)
		require.NoError(t, err)
		assert.Equal(t, []string{strings.Repeat("a", 64), strings.Repeat("b", 64)}[index],
			retained.Registered.RegistrationToken)
		assert.JSONEq(t, `{"cursor":"same-cursor"}`, string(calls[0].Payload))
		assert.Equal(t, outputs[index].ToolCallID, calls[0].ContinuationRootToolCallID)
	}
	actions[0].state.returned = 0
	automatic, ok := rt.automaticContinuationPlan(run.Context{RunID: "run"}, actions[:1])
	require.True(t, ok)
	require.NotNil(t, automatic.ToolCalls[0].Registry)
	assert.Equal(t, actions[0].registry.Resolution, automatic.ToolCalls[0].Registry.Resolution)

	registration := rt.agents[definition.route.ID]
	registration.Definition.registryTools = nil
	rt.agents[definition.route.ID] = registration
	actions, err = rt.availableContinuationActions(definition.route.ID, outputs)
	require.NoError(t, err)
	assert.Empty(t, actions, "historical contracts cannot grant current consumption")
}

func TestRegistrySelfPagingDoesNotCreateDedicatedContinuation(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	registered := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	declaration := registered.Toolset.Tools[0]
	declaration.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
		ContinueTool: &declaration.Name, CursorField: "cursor", NextCursorField: "next_cursor",
	}}
	resolved, err := registrycontract.Resolve(registered)
	require.NoError(t, err)
	binding, err := resolved.Select("company", "records.find")
	require.NoError(t, err)
	output := sourceContinuationOutput("records.find", "query", `{"value":1}`, "next")
	output.Registry = binding
	actions, err := rt.availableContinuationActions(definition.route.ID, []*planner.ToolOutput{output})
	require.NoError(t, err)
	assert.Empty(t, actions)
}

func testRegistryPagingResolution(token string) *genregistry.ResolvedToolset {
	registered := testRuntimeRegistryResolution("records.find", token)
	source := registered.Toolset.Tools[0]
	source.ConsumerContract.Confirmation = nil
	source.ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{
		Path: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.NewToolFieldSegmentField("value")}},
	}}
	target := testRuntimeRegistryResolution("records.continue", token).Toolset.Tools[0]
	target.ConsumerContract.Confirmation = nil
	target.PayloadSchema = []byte(`{"type":"object","additionalProperties":false}`)
	target.ConsumerContract.Payload.SchemaWithoutRootExample = target.PayloadSchema
	target.ExecutionPayloadSchema = []byte(`{"type":"object","properties":{"cursor":{"type":"string"}},"required":["cursor"],"additionalProperties":false}`)
	source.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
		ContinueTool: &target.Name, CursorField: "cursor", NextCursorField: "next_cursor",
	}}
	target.ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
		ContinueTool: &target.Name, SourceTool: &source.Name, CursorField: "cursor", NextCursorField: "next_cursor",
	}}
	registered.Toolset.Tools = append(registered.Toolset.Tools, target)
	return registered
}
