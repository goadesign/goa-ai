package runtime

// Native Agent calls keep their selected configuration and result contract
// while later planning activities discover replacements.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestWithAgentExecutorsCopiesDefinitions(t *testing.T) {
	parent := testRegistryAgentDefinition(testRegistrySources{})
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	originalRoute := child.Route()
	composed := parent.WithAgentExecutors(&child)

	_, exists := parent.ChildDefinition(originalRoute.ID)
	assert.False(t, exists)
	child = testAgentDefinition("another.agent", "another.workflow", "another.queue", nil, nil)
	selected, exists := composed.ChildDefinition(originalRoute.ID)
	require.True(t, exists)
	assert.Equal(t, originalRoute, selected.Route())
	_, exists = composed.ChildDefinition(child.Route().ID)
	assert.False(t, exists)
}

func TestRegistryAgentPreparationRetainsSelectedConfiguration(t *testing.T) {
	rt := New(newTestStore(), WithEngine(&stubEngine{}))
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, []string{"facility"})
	parent := testRegistryAgentDefinition(testRegistrySources{}).WithAgentExecutors(&child)
	rt.agents[parent.route.ID] = AgentRegistration{Definition: parent}
	call := testNativeRegistryCall(t, "revision/1")
	config := &AgentToolConfiguration{
		Messages: []*model.Message{{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "Installer revision 1"}}}},
		Labels:   map[string]string{"model": "reasoning"},
	}
	require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(_ context.Context, revision string, received *ToolCall) (*AgentToolConfiguration, error) {
		assert.Equal(t, "revision/1", revision)
		assert.Equal(t, "facility-1", received.Labels["facility"])
		return config, nil
	}))
	require.NoError(t, rt.Seal(t.Context()))
	require.ErrorIs(t, rt.RegisterAgentToolResolver("other", func(context.Context, string, *ToolCall) (*AgentToolConfiguration, error) {
		return nil, errors.New("unused")
	}), ErrRegistrationClosed)

	output, err := rt.prepareAgentChildActivity(t.Context(), &api.AgentChildActivityInput{Call: call})
	require.NoError(t, err)
	assert.Equal(t, "facility-1", output.Success.Labels["facility"])
	assert.NotContains(t, config.Labels, "facility")
	config.Messages[0].Parts[0] = model.TextPart{Text: "changed"}
	assert.Equal(t, "Installer revision 1", output.Success.Messages[0].Text())

	// A replay uses the recorded activity value even when storage is unavailable.
	rt.agentToolResolvers[child.route.ID] = func(context.Context, string, *ToolCall) (*AgentToolConfiguration, error) {
		t.Fatal("replay must not resolve the application configuration")
		return nil, nil
	}
	wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, agentChildOutput: output}
	request, err := rt.prepareAgentChild(wf, call, nil, run.Context{})
	require.NoError(t, err)
	input, err := agentChildRunInput(child, request)
	require.NoError(t, err)
	assert.Equal(t, call.SessionID, input.SessionID)
	assert.Equal(t, call.RunID, input.ParentRunID)
	assert.Equal(t, call.ToolCallID, input.ParentToolCallID)
	assert.Equal(t, "reasoning", input.Labels["model"])
	assert.Equal(t, call.Registry, input.ToolRegistry)

	// The boundary also rejects targets outside the parent's allowed workers.
	other := testRegistryAgentDefinition(testRegistrySources{})
	_, err = childDefinitionForCall(call, other)
	require.ErrorContains(t, err, "not in the current definition")
}

func TestRegistryAgentPlannerReceivesOriginalResultContract(t *testing.T) {
	rt := New(newTestStore())
	call := testNativeRegistryCall(t, "revision/1")
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	seen := false
	rt.agents[child.route.ID] = AgentRegistration{Definition: child, Planner: &stubPlanner{
		start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
			seen = true
			require.NotNil(t, input.ParentTool)
			assert.Equal(t, call.Name, input.ParentTool.Name)
			assert.Empty(t, input.Agent.AdvertisedToolDefinitions())
			value, err := input.ParentTool.Result.Codec.FromJSON([]byte(`{"value":9007199254740993}`))
			require.NoError(t, err)
			assert.Equal(t, json.Number("9007199254740993"), value.(map[string]any)["value"])
			return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{
				Result: rawjson.Message(`{"value":9007199254740993}`),
			}}, nil
		},
	}}
	nested := agentChildRunContext(&call)
	output, err := rt.PlanStartActivity(t.Context(), &PlanActivityInput{
		AgentID: child.route.ID, RunID: nested.RunID, RunContext: nested,
	})
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.NotNil(t, output.Result)
	assert.True(t, seen)
	assert.JSONEq(t, `{"value":9007199254740993}`, string(output.Result.FinalToolResult.Result))

	result, err := rt.adaptAgentChildOutput(&AgentToolConfig{Definition: child}, &call, nested, &RunOutput{
		FinalToolResult: &api.ToolEvent{Name: call.Name, Result: output.Result.FinalToolResult.Result},
	})
	require.NoError(t, err)
	assert.Equal(t, json.Number("9007199254740993"), result.Result.(map[string]any)["value"])
	require.NotNil(t, result.RunLink)
	_, err = rt.adaptAgentChildOutput(&AgentToolConfig{Definition: child}, &call, nested, &RunOutput{
		Final: &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}},
	})
	require.ErrorContains(t, err, "without its typed result")
	_, err = rt.decodeAgentChildFinalToolResult(&call, &api.ToolEvent{Result: rawjson.Message(`{"value":"wrong"}`)})
	require.Error(t, err)
}

func TestRegistryAgentContextSurvivesCheckpointRoundTrip(t *testing.T) {
	call := testNativeRegistryCall(t, "revision/1")
	nested := agentChildRunContext(&call)
	encoded, err := json.Marshal(checkpointContextFromRun(nested))
	require.NoError(t, err)
	var saved checkpointRunContext
	require.NoError(t, json.Unmarshal(encoded, &saved))
	restored := restoreCheckpointRunContext(saved, &RunInput{RunID: "continued", SessionID: call.SessionID})
	assert.Equal(t, call.Registry.Registry, restored.ToolRegistry.Registry)
	assert.JSONEq(t, string(call.Registry.Resolution), string(restored.ToolRegistry.Resolution))
	spec, err := selectedParentTool(restored.Tool, restored.ToolRegistry, "generic.agent", func(tools.Ident) (tools.ToolSpec, bool) {
		t.Fatal("restoring a registry contract must not consult static tools")
		return tools.ToolSpec{}, false
	})
	require.NoError(t, err)
	assert.Equal(t, call.Name, spec.Name)
	_, err = selectedParentTool(restored.Tool, restored.ToolRegistry, "wrong.agent", nil)
	require.ErrorContains(t, err, "does not target")
}

func testNativeRegistryCall(t *testing.T, revision string) ToolCall {
	t.Helper()
	resolution := testNativeRegistryResolution(revision)
	resolved, err := registrycontract.Resolve(resolution)
	require.NoError(t, err)
	binding, err := resolved.Select("company", "records.find")
	require.NoError(t, err)
	return ToolCall{
		Name: "records.find", AgentID: "records.agent", RunID: "parent-run",
		SessionID: "session-1", TurnID: "turn-1", ToolCallID: "call-child",
		Payload: rawjson.Message(`{"value":1}`), Labels: map[string]string{"facility": "facility-1"},
		Registry: binding,
	}
}

func testNativeRegistryResolution(revision string) *genregistry.ResolvedToolset {
	resolution := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	contract := resolution.Toolset.Tools[0].ConsumerContract
	contract.Kind = "agent"
	contract.Agent = &genregistry.AgentToolTarget{Executor: "generic.agent", Configuration: revision}
	contract.Confirmation = nil
	return resolution
}

func TestRegistryAgentConcurrentChildrenKeepIndependentConfigurations(t *testing.T) {
	rt := New(newTestStore())
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	parent := testRegistryAgentDefinition(testRegistrySources{}).WithAgentExecutors(&child)
	rt.agents[parent.route.ID] = AgentRegistration{Definition: parent}
	require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(_ context.Context, revision string, _ *ToolCall) (*AgentToolConfiguration, error) {
		return &AgentToolConfiguration{Labels: map[string]string{"revision": revision}}, nil
	}))
	calls := []ToolCall{testNativeRegistryCall(t, "revision/1"), testNativeRegistryCall(t, "revision/2")}
	calls[1].ToolCallID = "call-other"
	children := make(chan *controlledChildHandle, 2)
	wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, controlledChildHandles: children}
	parentRun := &run.Context{RunID: calls[0].RunID, SessionID: calls[0].SessionID, TurnID: calls[0].TurnID}
	seedRunMeta(t, rt, &RunInput{AgentID: parent.route.ID, RunID: parentRun.RunID, SessionID: parentRun.SessionID, TurnID: parentRun.TurnID})
	type outcome struct {
		results []*ToolExecutionResult
		err     error
	}
	done := make(chan outcome, 1)
	go func() {
		results, _, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{}, parent.route.ID, parentRun, nil, calls, 0, nil, time.Time{})
		done <- outcome{results, err}
	}()
	first := waitForChildHandle(t, children, "first native child")
	second := waitForChildHandle(t, children, "second native child")
	assert.NotEqual(t, wf.childRequests[0].Input.RunID, wf.childRequests[1].Input.RunID)
	assert.Equal(t, "revision/1", wf.childRequests[0].Input.Labels["revision"])
	assert.Equal(t, "revision/2", wf.childRequests[1].Input.Labels["revision"])
	second.out = &RunOutput{AgentID: child.route.ID, RunID: wf.childRequests[1].Input.RunID,
		FinalToolResult: &api.ToolEvent{Name: calls[1].Name, Result: rawjson.Message(`{"value":2}`)}}
	close(second.ready)
	first.out = &RunOutput{AgentID: child.route.ID, RunID: wf.childRequests[0].Input.RunID,
		FinalToolResult: &api.ToolEvent{Name: calls[0].Name, Result: rawjson.Message(`{"value":1}`)}}
	close(first.ready)
	result := <-done
	require.NoError(t, result.err)
	require.Len(t, result.results, 2)
	for i, expected := range []string{"1", "2"} {
		require.Nil(t, result.results[i].ToolResult.Failure)
		assert.Equal(t, calls[i].ToolCallID, result.results[i].ToolResult.ToolCallID)
		assert.Equal(t, json.Number(expected), result.results[i].ToolResult.Result.(map[string]any)["value"])
	}
}

func TestRegistryAgentCancellationWaitsForChild(t *testing.T) {
	rt := New(newTestStore())
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	parent := testRegistryAgentDefinition(testRegistrySources{}).WithAgentExecutors(&child)
	rt.agents[parent.route.ID] = AgentRegistration{Definition: parent}
	require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(context.Context, string, *ToolCall) (*AgentToolConfiguration, error) {
		return &AgentToolConfiguration{}, nil
	}))
	call := testNativeRegistryCall(t, "revision/1")
	parentRun := &run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
	seedRunMeta(t, rt, &RunInput{AgentID: parent.route.ID, RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	children := make(chan *controlledChildHandle, 1)
	wf := &testWorkflowContext{ctx: ctx, hookRuntime: rt, controlledChildHandles: children}
	done := make(chan error, 1)
	go func() {
		_, _, err := rt.executeToolCalls(wf, "execute", engine.ActivityOptions{}, parent.route.ID, parentRun, nil, []ToolCall{call}, 0, nil, time.Time{})
		done <- err
	}()
	handle := waitForChildHandle(t, children, "native child")
	cancel()
	require.Eventually(t, handle.wasCanceled, time.Second, time.Millisecond)
	select {
	case <-done:
		require.Fail(t, "parent returned before its native child finished cancellation")
	default:
	}
	close(handle.ready)
	require.ErrorIs(t, <-done, context.Canceled)
}

func TestRegistryAgentDiscoveryReadsReplacementWithoutRestart(t *testing.T) {
	rt := New(newTestStore())
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	parent := testRegistryAgentDefinition(testRegistrySources{}).WithAgentExecutors(&child)
	current := testNativeRegistryResolution("revision/1")
	client := &genregistry.Client{ResolveToolsetEndpoint: func(context.Context, any) (any, error) { return current, nil }}
	require.NoError(t, rt.RegisterRegistry("company", client, unusedRegistryPulse{}))
	first, err := rt.resolveRegistryCatalog(t.Context(), parent)
	require.NoError(t, err)
	selection := first.selections["records.find"]
	saved, err := selection.resolution.Select(selection.registry, "records.find")
	require.NoError(t, err)
	current = testNativeRegistryResolution("revision/2")
	second, err := rt.resolveRegistryCatalog(t.Context(), parent)
	require.NoError(t, err)
	assert.Equal(t, "revision/2", second.selections["records.find"].resolution.Registered.Toolset.Tools[0].ConsumerContract.Agent.Configuration)
	retained, err := registrycontract.Read(saved)
	require.NoError(t, err)
	assert.Equal(t, "revision/1", retained.Registered.Toolset.Tools[0].ConsumerContract.Agent.Configuration)
	_, err = rt.resolveRegistryCatalog(t.Context(), testRegistryAgentDefinition(testRegistrySources{}))
	require.ErrorContains(t, err, "unconfigured Agent executor")
}

func TestRegistryAgentRetainsCompiledDescendantDefinitions(t *testing.T) {
	grandchild := testAgentDefinition("grandchild.agent", "grandchild.workflow", "grandchild.queue", nil, nil)
	grandchildTool := newAnyJSONSpec("compiled.delegate")
	grandchildTool.IsAgentTool = true
	grandchildTool.AgentID = string(grandchild.route.ID)
	// Generated root definitions contain the complete compiled graph; each
	// child record contains that child's own specs and source declarations.
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{grandchildTool}, nil)
	parent := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "parent.workflow", DefaultTaskQueue: "parent.queue"},
		nil, nil, nil, nil, []AgentDefinition{child, grandchild}, nil).WithRegistryTools(testRegistrySources{})
	selected, err := childDefinitionForCall(testNativeRegistryCall(t, "revision/1"), parent)
	require.NoError(t, err)
	descendant, err := childDefinitionForCall(ToolCall{Name: grandchildTool.Name}, selected)
	require.NoError(t, err)
	assert.Equal(t, grandchild.route.ID, descendant.route.ID)
}
