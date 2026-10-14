// Registry consumers and Agent providers use separate runtime registrations.
// These checks prove that preparation stays on the provider and that the
// consumer retains only the public operation contract and worker route.
package runtime

import (
	"context"
	"testing"

	genregistry "goa.design/goa-ai/registry/gen/registry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/run"
)

func TestRegistryAgentProviderOwnsPreparation(t *testing.T) {
	store := newTestStore()
	consumer := New(store, WithEngine(&stubEngine{}))
	provider := New(store, WithEngine(&stubEngine{}))
	parent := testRegistryAgentDefinition(testRegistrySources{})
	child := testNativeProviderDefinition(t, []string{"facility"})
	consumer.agents[parent.route.ID] = AgentRegistration{Definition: parent}
	provider.agents[child.route.ID] = AgentRegistration{Definition: child}
	call := testNativeRegistryCall(t, "revision/1")
	prepared := 0
	require.NoError(t, provider.RegisterAgentToolResolver(child.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, revision string, input *ToolCall) (*AgentToolConfiguration, error) {
		prepared++
		assert.Equal(t, "revision/1", revision)
		assert.Equal(t, call.ToolCallID, input.ToolCallID)
		return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "Provider-owned preparation"}},
		}})}, nil
	}))
	selected, err := consumer.selectedAgentToolConfig(call)
	require.NoError(t, err)
	assert.Equal(t, child.route, selected.Definition.route)
	assert.Empty(t, selected.Definition.specs)
	assert.Empty(t, consumer.agentToolResolvers)
	_, loaded := consumer.agentByID(child.route.ID)
	assert.False(t, loaded)
	parentRun := run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
	position := testToolHistory(t, consumer, parent.route.ID, parentRun, nil)
	command := &api.AgentChildActivityInput{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, Call: call, ParentRun: parentRun, HistoryEndID: position}
	output, err := provider.prepareAgentChildActivity(t.Context(), command)
	require.NoError(t, err)
	require.NotNil(t, output.Success)
	assert.Equal(t, 1, prepared)
	workflow := &testWorkflowContext{ctx: t.Context(), hookRuntime: consumer, agentChildOutput: output}
	request, stopped, err := consumer.prepareAgentChild(workflow, call, position, parentRun)
	require.Nil(t, stopped)
	require.NoError(t, err)
	assert.Equal(t, child.route.DefaultTaskQueue, workflow.lastAgentChildCall.Options.Queue)
	input, err := agentChildRunInput(selected.Definition, request)
	require.NoError(t, err)
	assert.Equal(t, call.RunID, input.ParentRunID)
	assert.Equal(t, call.ToolCallID, input.ParentToolCallID)
	assert.Equal(t, call.Registry, input.ToolRegistry)
	replayed, err := provider.prepareAgentChildActivity(t.Context(), command)
	require.NoError(t, err)
	assert.Equal(t, output, replayed)
	assert.Equal(t, 1, prepared)
}

func TestRegistryAgentProviderRejectsWrongWorkerRoute(t *testing.T) {
	for _, route := range []AgentRoute{
		{ID: "generic.agent", WorkflowName: "wrong.workflow", DefaultTaskQueue: "generic.queue"},
		{ID: "generic.agent", WorkflowName: "generic.workflow", DefaultTaskQueue: "wrong.queue"},
	} {
		t.Run(route.WorkflowName+"/"+route.DefaultTaskQueue, func(t *testing.T) {
			provider := New(newTestStore())
			definition := NewAgentDefinition(route, nil, nil, nil, nil, nil, nil)
			provider.agents[route.ID] = AgentRegistration{Definition: definition}
			_, err := provider.providerAgentToolConfig(testNativeRegistryCall(t, "revision/1"))
			assert.ErrorContains(t, err, "does not match this provider")
		})
	}
	provider := New(newTestStore())
	_, err := provider.providerAgentToolConfig(testNativeRegistryCall(t, "revision/1"))
	assert.ErrorContains(t, err, "not registered on this worker")
}
