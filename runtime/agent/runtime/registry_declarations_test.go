package runtime

// These tests cover how generated registry declarations reach the runtime and
// why a consumer generated without them cannot bind compiled tools.

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestWithRegistryDeclarationsKeepsOnlyDefinitionTools(t *testing.T) {
	specs := []tools.ToolSpec{genrecords.SpecRead()}
	definition := testAgentDefinition("records.agent", "records.workflow", "records.queue", specs, nil).WithRegistryDeclarations(genrecords.ToolSchema)
	declaration, ok := definition.registryDeclaration(genrecords.Read)
	require.True(t, ok)
	assert.Equal(t, string(genrecords.Read), declaration.Name)
	_, ok = definition.registryDeclaration(genrecords.Erase)
	assert.False(t, ok, "a declaration for a tool outside the definition is not kept")
	self, ok := definition.ChildDefinition(definition.route.ID)
	require.True(t, ok)
	_, ok = self.registryDeclaration(genrecords.Read)
	assert.True(t, ok, "the definition graph sees the same declarations")
}

func TestWithRegistryDeclarationsRejectsInvalidGeneratedValues(t *testing.T) {
	specs := []tools.ToolSpec{genrecords.SpecRead()}
	for _, tc := range []struct {
		name   string
		lookup func(tools.Ident) (*genregistry.ToolSchema, bool)
		want   string
	}{
		{name: "provider target", want: "must not select a provider", lookup: func(name tools.Ident) (*genregistry.ToolSchema, bool) {
			declaration, ok := genrecords.ToolSchema(name)
			declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "a", Workflow: "w", Queue: "q", Configuration: "c"}
			return declaration, ok
		}},
		{name: "another tool", want: "is incomplete", lookup: func(tools.Ident) (*genregistry.ToolSchema, bool) {
			return genrecords.ToolSchema(genrecords.Erase)
		}},
		{name: "nil lookup", want: "exactly once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			message := panicMessage(t, func() {
				testAgentDefinition("records.agent", "records.workflow", "records.queue", specs, nil).WithRegistryDeclarations(tc.lookup)
			})
			assert.Contains(t, message, tc.want)
		})
	}
	assert.Panics(t, func() {
		testAgentDefinition("records.agent", "records.workflow", "records.queue", specs, nil).
			WithRegistryDeclarations(genrecords.ToolSchema).
			WithRegistryDeclarations(genrecords.ToolSchema)
	})
}

func TestRegistryDeclarationsRejectConflictingRegistrations(t *testing.T) {
	rt := New(newTestStore())
	specs := []tools.ToolSpec{genrecords.SpecRead()}
	first := testAgentDefinition("first.agent", "first.workflow", "first.queue", specs, nil).WithRegistryDeclarations(genrecords.ToolSchema)
	require.NoError(t, rt.validateRegistryDeclarations(first))
	seedTestRegistryDeclarations(rt, first)
	same := testAgentDefinition("same.agent", "same.workflow", "same.queue", specs, nil).WithRegistryDeclarations(genrecords.ToolSchema)
	require.NoError(t, rt.validateRegistryDeclarations(same))
	changed := testAgentDefinition("changed.agent", "changed.workflow", "changed.queue", specs, nil).WithRegistryDeclarations(func(name tools.Ident) (*genregistry.ToolSchema, bool) {
		declaration, ok := genrecords.ToolSchema(name)
		declaration.Description = new("A different generated description")
		return declaration, ok
	})
	require.ErrorIs(t, rt.validateRegistryDeclarations(changed), ErrInvalidConfig)
	declaration, ok := rt.registryDeclaration(genrecords.Read)
	require.True(t, ok)
	original, ok := genrecords.ToolSchema(genrecords.Read)
	require.True(t, ok)
	assert.Equal(t, original.Description, declaration.Description, "the first registration's declaration is kept")
}

func TestStaticBindingRequiresGeneratedRegistryDeclarations(t *testing.T) {
	rt, catalog, selected := staticServiceCatalog(t)
	withoutDeclarations := NewAgentDefinition(catalog.definition.route, genrecords.Specs(), nil, nil, []tools.Ident{genrecords.Read}, nil, nil).WithRegistryTools(testRegistrySources{whole: true})
	err := rt.newRegistryCatalog(withoutDeclarations).BindStaticService(testRegistryName, genrecords.Toolset(), selected, readTestStaticResource, genrecords.Read)
	require.ErrorContains(t, err, "regenerate the agent")
	require.NoError(t, catalog.BindStaticService(testRegistryName, genrecords.Toolset(), selected, readTestStaticResource, genrecords.Read))
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	assert.Less(t, len(binding.Resolution), 1024)
	assert.Contains(t, string(binding.Resolution), "compiled_service")
}

func TestCheckpointRestoresDirectAndCompiledStaticSelections(t *testing.T) {
	rt, catalog, selected := staticServiceCatalog(t)
	require.NoError(t, catalog.BindStaticService(testRegistryName, genrecords.Toolset(), selected, readTestStaticResource, genrecords.Read))
	compiled, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	for _, tc := range []struct {
		name    string
		binding *tools.RegistryBinding
	}{
		// A run paused by an earlier release saved the full declaration.
		{name: "direct", binding: testDirectSelection(t, selected, genrecords.Read)},
		{name: "compiled", binding: compiled},
	} {
		t.Run(tc.name, func(t *testing.T) {
			call := ToolCall{Name: genrecords.Read, ToolCallID: "call-1", Registry: tc.binding, Payload: rawjson.Message(`{"query":"all"}`)}
			state := checkpointRunState{
				ToolOutputs: []*planner.ToolOutput{{Name: call.Name, ToolCallID: call.ToolCallID, Registry: tc.binding}},
				ToolEvents:  []*api.ToolEvent{{Name: call.Name, ToolCallID: call.ToolCallID, Result: rawjson.Message(`{"count":4}`)}},
			}
			restored, err := rt.restoreCheckpointState(state)
			require.NoError(t, err)
			assert.Equal(t, historicalToolResult{result: rawjson.Message(`{"count":4}`)}, restored.ToolEvents[0].Result)
			require.NoError(t, validateCheckpointToolRequest(call, catalog.definition))
		})
	}
}

// panicMessage returns the value a function panics with, formatted as text.
func panicMessage(t *testing.T, fn func()) (message string) {
	t.Helper()
	defer func() {
		value := recover()
		require.NotNil(t, value, "function did not panic")
		message = fmt.Sprint(value)
	}()
	fn()
	return ""
}
