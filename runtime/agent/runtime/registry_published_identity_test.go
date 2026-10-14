// Registered Agent names select caller contracts while provider callbacks select
// the original operations. These tests keep complete consumer input and child
// identity intact across configuration preparation and planner execution.
package runtime

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestRegisteredAgentIdentityWithConsumerInput(t *testing.T) {
	resource := consumerResource("catalog.search")
	second := consumerResource("catalog.count")
	second.Resolved.Toolset.Tools[0].Name = "records.count"
	resource.Resolved.Toolset.Tools = append(resource.Resolved.Toolset.Tools, second.Resolved.Toolset.Tools[0])

	resource.Source.Target = genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: resource.Resolved.Toolset.Name})
	originals := make([]tools.ToolSpec, 0, len(resource.Resolved.Toolset.Tools))
	for _, declaration := range resource.Resolved.Toolset.Tools {
		declaration.ConsumerContract.Kind = registryAgentKind
		declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{
			Executor: "generic.agent", Configuration: "revision/1", Workflow: "generic.workflow", Queue: "generic.queue",
		}
		spec, err := contract.Compile(declaration)
		require.NoError(t, err)
		originals = append(originals, spec)
	}

	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	parent := testRegistryAgentDefinition(registeredTestSource{[]*genregistry.RegisteredToolsetSelection{resource}})
	parent = parent.WithInputBinding(nil, func(_ context.Context, catalog *RegistryCatalog) error {
		for _, original := range originals {
			bound := consumerBoundSpec(t, original, consumerScopeRef{ID: "accepted"})
			if err := catalog.BindSourceInput(testRegistryName, resource.Source, original, bound); err != nil {
				return err
			}
		}
		return nil
	})
	modelInput := rawjson.Message(`{ "query": "active" }`)
	rt.agents[parent.route.ID] = AgentRegistration{Definition: parent, Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		assert.Len(t, input.Agent.AdvertisedToolDefinitions(), 2)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{
			{Name: registeredTestName(t, resource, "records.find"), Payload: modelInput},
			{Name: registeredTestName(t, resource, "records.count"), Payload: modelInput},
		}}, nil
	}}}
	parentRun := run.Context{RunID: "registered-parent", SessionID: "registered-session", TurnID: "registered-turn"}
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: parent.route.ID, RunID: parentRun.RunID, RunContext: parentRun}, nil)
	planned, err := rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	require.Nil(t, planned.OutputContractFailure)
	require.Len(t, planned.Result.ToolCalls, 2)
	require.NoError(t, rt.normalizePlanResultForExecution(planned.Result, parentRun, parent.route.ID))

	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", originals, nil)
	rt.agents[child.route.ID] = AgentRegistration{Definition: child}
	var received []tools.Ident
	var expected ToolCall
	var operation tools.Ident
	require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(_ context.Context, origin genregistry.ExecutionInvocation, revision string, call *ToolCall) (*AgentToolConfiguration, error) {
		received = append(received, call.Name)
		assert.Equal(t, operation, call.Name)
		assert.Equal(t, expected.TranscriptName(), call.TranscriptName())
		assert.Equal(t, expected.Payload, call.Payload)
		assert.Equal(t, modelInput, call.TranscriptPayload())
		assert.JSONEq(t, `{"scope":{"id":"accepted"},"query":"active"}`, string(call.Payload))
		assert.Equal(t, expected.Registry, call.Registry)
		assert.Equal(t, expected.ToolCallID, call.ToolCallID)
		assert.Equal(t, "revision/1", revision)
		tool, ok := origin.Invocation.AsTool()
		require.True(t, ok)
		assert.Equal(t, expected.RunID, tool.RunID)
		assert.Equal(t, expected.ToolCallID, tool.ToolCallID)
		return &AgentToolConfiguration{History: LiteralAgentHistory(nil)}, nil
	}))

	batch := toolBatchExec{agentID: parent.route.ID, runID: parentRun.RunID, sessionID: parentRun.SessionID, turnID: parentRun.TurnID, runCtx: &parentRun}
	for index, plannedCall := range planned.Result.ToolCalls {
		call := batch.normalizeToolCall(plannedCall)
		t.Run(string(call.Name), func(t *testing.T) {
			expected = cloneToolCall(call)
			operation = originals[index].Name
			nested := agentChildRunContext(&call)
			assert.Equal(t, NestedRunIDForToolCall(parentRun.RunID, call.Name, call.ToolCallID), nested.RunID)
			assert.Equal(t, call.Name, nested.Tool)
			command := &api.AgentChildActivityInput{
				Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})},
				Call:   call, ParentRun: parentRun, HistoryEndID: input.HistoryEndID,
			}
			prepared, err := rt.prepareAgentChildActivity(t.Context(), command)
			require.NoError(t, err)
			require.NotNil(t, prepared.Success)
			replayed, err := rt.prepareAgentChildActivity(t.Context(), command)
			require.NoError(t, err)
			assert.Equal(t, prepared, replayed)
			assert.Equal(t, expected, command.Call)
			assert.Equal(t, nested, agentChildRunContext(&command.Call))

			planning := New(newTestStore())
			planning.agents[child.route.ID] = AgentRegistration{Definition: child, Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
				require.NotNil(t, input.ParentTool)
				assert.Equal(t, operation, input.ParentTool.Name)
				assert.Equal(t, call.Name, input.RunContext.Tool)
				assert.Equal(t, nested.RunID, input.RunContext.RunID)
				assert.Equal(t, call.Registry, input.RunContext.ToolRegistry)
				assert.Equal(t, call.Payload, input.RunContext.ToolArgs)
				return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":7}`)}}, nil
			}}}
			childInput := seedTestPlanInput(t, planning, PlanActivityInput{AgentID: child.route.ID, RunID: nested.RunID, RunContext: nested}, nil)
			output, err := planning.PlanStartActivity(t.Context(), childInput)
			require.NoError(t, err)
			require.Nil(t, output.OutputContractFailure)
			require.NotNil(t, output.Result.FinalToolResult)
		})
	}
	assert.Equal(t, []tools.Ident{"records.find", "records.count"}, received)
}
