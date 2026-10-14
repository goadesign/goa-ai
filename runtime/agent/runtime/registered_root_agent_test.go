// Native registered-root tests run provider configuration without a Registry
// permission request and retain the original operation in the Agent context.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRegisteredRootNativePreparesWithoutPermissionRequest(t *testing.T) {
	resolution := testNativeRegistryResolution("revision/1")
	_, invocation := registeredRootFixture()
	invocation.Target.Target = genregistry.NewCatalogTargetNative(&genregistry.NativeCatalogTarget{Toolset: resolution.Toolset.Name, Tool: resolution.Toolset.Tools[0].Name})
	compiled, err := registrycontract.Resolve(resolution)
	require.NoError(t, err)
	spec := compiled.Specs[tools.Ident(resolution.Toolset.Tools[0].Name)]
	runtime := New(newTestStore(), WithEngine(engineinmem.New()))
	definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
	require.NoError(t, runtime.RegisterAgent(t.Context(), AgentRegistration{Definition: definition, WorkflowHandler: runtime.ExecuteWorkflow, PlanActivityName: "native.plan", ResumeActivityName: "native.resume", ExecuteToolActivity: "native.execute", Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		assert.Equal(t, invocation, input.RunContext.RegisteredInvocation)
		assert.Empty(t, input.RunContext.ParentRunID)
		return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":2}`)}}, nil
	}}}))
	require.NoError(t, runtime.RegisterAgentToolResolver(definition.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, _ string, _ *ToolCall) (*AgentToolConfiguration, error) {
		return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Find the record."}}}})}, nil
	}))
	handle, err := runtime.StartOneShotToolOperation(t.Context(), "registry", resolution, invocation, nil, nil)
	require.NoError(t, err)
	output, err := handle.Wait(t.Context())
	require.NoError(t, err)
	require.NotNil(t, output.FinalToolResult)
	assert.JSONEq(t, `{"value":2}`, string(output.FinalToolResult.Result))
}
