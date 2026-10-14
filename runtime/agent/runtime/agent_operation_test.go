// Direct operation tests prove that preparation and result validation need no
// Registry publication or parent Agent, and that accepted retries preserve work.
package runtime

import (
	"context"
	"testing"

	genregistry "goa.design/goa-ai/registry/gen/registry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestDirectAgentOperationCompletesWithoutPublicationOrParent(t *testing.T) {
	resolution := testNativeRegistryResolution("revision/1")
	operation := resolution.Toolset.Tools[0]
	resolved, err := registrycontract.Resolve(resolution)
	require.NoError(t, err)
	spec := resolved.Specs[tools.Ident(operation.Name)]
	store := newTestStore()
	rt := New(store, WithEngine(engineinmem.New()))
	definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
	calls := 0
	registration := AgentRegistration{
		Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
		PlanActivityName: "direct.plan", ResumeActivityName: "direct.resume", ExecuteToolActivity: "direct.execute",
		Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			calls++
			assert.Empty(t, input.RunContext.ParentRunID)
			assert.Empty(t, input.RunContext.ParentToolCallID)
			assert.Empty(t, input.RunContext.ParentAgentID)
			require.NotNil(t, input.ParentTool)
			assert.Equal(t, spec.Name, input.ParentTool.Name)
			return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":9007199254740993}`)}}, nil
		}},
	}
	require.NoError(t, rt.RegisterAgent(t.Context(), registration))
	preparations := 0
	require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(_ context.Context, origin genregistry.ExecutionInvocation, revision string, call *ToolCall) (*AgentToolConfiguration, error) {
		preparations++
		root, ok := origin.Invocation.AsRoot()
		require.True(t, ok)
		assert.Equal(t, "direct-run", root.RunID)
		root.RunID = "provider-cannot-change-owner"
		assert.Equal(t, "revision/1", revision)
		assert.Equal(t, "direct-run", call.RunID)
		assert.Empty(t, call.ToolCallID)
		assert.Empty(t, call.ParentToolCallID)
		return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read the quantity."}}}})}, nil
	}))
	for range 2 {
		handle, err := rt.StartOneShotOperation(t.Context(), "direct-run", operation, rawjson.Message(`{"value":1}`), nil, nil)
		require.NoError(t, err)
		output, err := handle.Wait(t.Context())
		require.NoError(t, err)
		require.NotNil(t, output.FinalToolResult)
		assert.Equal(t, definition.route.ID, testSubjectAgent(t, output.Subject))
		assert.Equal(t, "direct-run", output.RunID)
		assert.Equal(t, spec.Name, output.FinalToolResult.Name)
		assert.JSONEq(t, `{"value":9007199254740993}`, string(output.FinalToolResult.Result))
	}
	assert.Equal(t, 1, calls)
	assert.Equal(t, 1, preparations)
	metadata, err := store.LoadRun(t.Context(), "direct-run")
	require.NoError(t, err)
	root, ok := metadata.Origin.Invocation.AsRoot()
	require.True(t, ok)
	assert.Equal(t, "direct-run", root.RunID)
	records, err := store.ListRunRecords(t.Context(), "direct-run", "", 100)
	require.NoError(t, err)
	completed := false
	for _, record := range records.Events {
		assert.NotEqual(t, string(hooks.ChildRunLinked), string(record.Type))
		assert.Empty(t, record.SessionID)
		completed = completed || string(record.Type) == string(hooks.RunCompleted)
	}
	assert.True(t, completed)
	_, err = rt.StartOneShotOperation(t.Context(), "direct-run", operation, rawjson.Message(`{"value":2}`), nil, nil)
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
	assert.Equal(t, 1, calls)
	_, err = rt.StartOneShotOperation(t.Context(), "direct-run", operation, rawjson.Message(`{"value":1}`), nil, map[string]any{"owner": "changed"})
	require.ErrorIs(t, err, engine.ErrWorkflowStartConflict)
}

func TestDirectAgentPreparationRejectsChangedExportBeforeResolver(t *testing.T) {
	for _, variation := range []string{"unknown operation", "different schema", "parent identity", "publication", "prepared history", "wrong route"} {
		t.Run(variation, func(t *testing.T) {
			resolution := testNativeRegistryResolution("revision/1")
			declaration := resolution.Toolset.Tools[0]
			resolved, err := registrycontract.Resolve(resolution)
			require.NoError(t, err)
			spec := resolved.Specs[tools.Ident(declaration.Name)]
			definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
			rt := New(newTestStore())
			rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
			require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
				t.Fatal("invalid operation must not prepare or execute")
				return nil, nil
			}))
			input := &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
				RunExecutorBranchAgent(definition.route.ID))}, RunID: "direct-run", Tool: spec.Name, ToolArgs: rawjson.Message(`{"value":1}`), Operation: declaration}
			switch variation {
			case "unknown operation":
				input.Operation.Name = "foreign.operation"
				input.Tool = "foreign.operation"
			case "different schema":
				input.Operation.ResultSchema = []byte(`{"type":"string"}`)
			case "parent identity":
				input.ParentRunID = "invented-parent"
			case "publication":
				input.ToolRegistry = testNativeRegistryCall(t, "revision/1").Registry
			case "prepared history":
				input.SeedEndID = "caller-history"
			case "wrong route":
				input.Operation.ConsumerContract.Agent.Queue = "wrong.queue"
			}
			_, err = rt.prepareAgentOperationActivity(t.Context(), input)
			require.Error(t, err)
			assert.True(t, engine.IsActivityErrorNonRetryable(err))
		})
	}
}

func TestDirectAgentPreparationRecoversOriginalInput(t *testing.T) {
	resolution := testNativeRegistryResolution("revision/1")
	declaration := resolution.Toolset.Tools[0]
	resolved, err := registrycontract.Resolve(resolution)
	require.NoError(t, err)
	spec := resolved.Specs[tools.Ident(declaration.Name)]
	rt := New(newTestStore())
	definition := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	calls := 0
	require.NoError(t, rt.RegisterAgentToolResolver(definition.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		calls++
		return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Original input."}}}}), Labels: map[string]string{"revision": "first"}, Metadata: map[string]any{"initial_progress": "ready"}}, nil
	}))
	input := &api.RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.
		RunExecutorBranchAgent(definition.route.ID))}, RunID: "recover-run", Tool: spec.Name, ToolArgs: rawjson.Message(`{"value":1}`), Operation: declaration}
	first, err := rt.prepareAgentOperationActivity(t.Context(), input)
	require.NoError(t, err)
	rt.agentToolResolvers[definition.route.ID] = func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		t.Fatal("accepted preparation must not resolve again")
		return nil, nil
	}
	second, err := rt.prepareAgentOperationActivity(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, first, second)
	assert.Equal(t, 1, calls)
	assert.Equal(t, "first", second.Prepared.Labels["revision"])
	assert.Equal(t, "ready", second.Prepared.Metadata["initial_progress"])
	context := run.Context{Tool: input.Tool, ToolArgs: input.ToolArgs, Operation: input.Operation}
	saved := checkpointContextFromRun(context)
	restored := restoreCheckpointRunContext(saved, &api.RunInput{Subject: input.Subject, RunID: "successor"})
	selected, err := selectedRunTool(restored, definition.route.ID, nil)
	require.NoError(t, err)
	assert.Equal(t, spec.Name, selected.Name)
	assert.Equal(t, input.Operation, restored.Operation)
}
