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

	genregistry "goa.design/goa-ai/registry/gen/registry"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/storage"
	"goa.design/goa-ai/runtime/agent/tools"
)

const registryAgentKind = "agent"

func TestRegistryAgentPreparationRetainsSelectedConfiguration(t *testing.T) {
	rt := New(newTestStore(), WithEngine(&stubEngine{}))
	child := testNativeProviderDefinition(t, []string{"facility"})
	parent := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[parent.route.ID] = AgentRegistration{Definition: parent}
	rt.agents[child.route.ID] = AgentRegistration{Definition: child}
	call := testNativeRegistryCall(t, "revision/1")
	config := &AgentToolConfiguration{
		History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "Installer revision 1"}}}}),
		Labels:  map[string]string{"model": "reasoning"},
	}
	require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, revision string, received *ToolCall) (*AgentToolConfiguration, error) {
		assert.Equal(t, "revision/1", revision)
		assert.Equal(t, "facility-1", received.Labels["facility"])
		return config, nil
	}))
	require.NoError(t, rt.Seal(t.Context()))
	require.ErrorIs(t, rt.RegisterAgentToolResolver("other", func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		return nil, errors.New("unused")
	}), ErrRegistrationClosed)

	parentRun := run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
	position := testToolHistory(t, rt, testSubjectAgent(t, call.Subject), parentRun, nil)
	output, err := rt.prepareAgentChildActivity(t.Context(), &api.AgentChildActivityInput{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, Call: call, ParentRun: parentRun, HistoryEndID: position})
	require.NoError(t, err)
	assert.Equal(t, "facility-1", output.Success.Labels["facility"])
	assert.NotContains(t, config.Labels, "facility")
	config.History.messages[0].Parts[0] = model.TextPart{Text: "changed"}
	page, err := rt.Store.ListRunSeedRecords(t.Context(), agentChildRunContext(&call).RunID, output.Success.SeedEndID, "", 512)
	require.NoError(t, err)
	require.Len(t, page.Records, 1)
	assert.Contains(t, string(page.Records[0].Messages), "Installer revision 1")
	assert.NotContains(t, string(page.Records[0].Messages), "changed")

	// A replay uses the recorded activity value even when storage is unavailable.
	rt.agentToolResolvers[child.route.ID] = func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		t.Fatal("replay must not resolve the application configuration")
		return nil, nil
	}
	wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, agentChildOutput: output}
	request, stopped, err := rt.prepareAgentChild(wf, call, position, parentRun)
	require.Nil(t, stopped)
	require.NoError(t, err)
	input, err := agentChildRunInput(child, request)
	require.NoError(t, err)
	assert.Equal(t, call.SessionID, input.SessionID)
	assert.Equal(t, call.RunID, input.ParentRunID)
	assert.Equal(t, call.ToolCallID, input.ParentToolCallID)
	assert.Equal(t, "reasoning", input.Labels["model"])
	assert.Equal(t, call.Registry, input.ToolRegistry)

	// An admitted provider needs no private child definition in the consumer.
	other := testRegistryAgentDefinition(testRegistrySources{})
	selected, err := childDefinitionForCall(call, other)
	require.NoError(t, err)
	assert.Equal(t, child.route, selected.route)
	assert.Empty(t, selected.specs)
}

func TestRegistryAgentTextOnlyChildInvocationContract(t *testing.T) {
	cases := []struct {
		name       string
		requiresUI bool
		confirm    bool
		contract   bool
	}{
		{name: "compatible", contract: true},
		{name: "requires UI", requiresUI: true},
		{name: "requires confirmation", confirm: true},
		{name: "missing text-only contract"},
	}
	for _, tc := range cases {
		for _, textOnly := range []bool{false, true} {
			name := tc.name + "/ordinary child"
			if textOnly {
				name = tc.name + "/text-only child"
			}
			t.Run(name, func(t *testing.T) {
				resolution := testNativeRegistryResolution("revision/1")
				declaration := resolution.Toolset.Tools[0]
				description := "Read values. Display a card."
				declaration.Description = &description
				consumer := declaration.ConsumerContract
				consumer.RequiresUI = tc.requiresUI
				if tc.confirm {
					consumer.Confirmation = &genregistry.ToolConfirmation{
						PromptTemplate: "Read {{ json .value }}?", DeniedResultTemplate: `{"value":0}`,
					}
				}
				if tc.contract {
					consumer.TextOnly = &genregistry.TextOnlyToolContract{
						Description: "Read values.", Search: consumer.Search,
						PayloadSchema: declaration.PayloadSchema, Payload: consumer.Payload,
						ExecutionSchema: declaration.ExecutionPayloadSchema,
					}
				}
				resolved, err := registrycontract.Resolve(resolution)
				require.NoError(t, err)
				call := testNativeRegistryCall(t, "revision/1")
				call.Registry, err = resolved.Select(testRegistryName, call.Name)
				require.NoError(t, err)

				child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{resolved.Specs[call.Name]}, nil)
				parent := testRegistryAgentDefinition(testRegistrySources{})
				rt := New(newTestStore(), WithEngine(&stubEngine{}))
				rt.agents[parent.route.ID] = AgentRegistration{Definition: parent}
				rt.agents[child.route.ID] = AgentRegistration{Definition: child}
				config := &AgentToolConfiguration{
					History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read this value."}}}}),
					Policy:  &PolicyOverrides{TextOnly: textOnly},
				}
				require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
					return config, nil
				}))
				parentRun := run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
				position := testToolHistory(t, rt, testSubjectAgent(t, call.Subject), parentRun, nil)
				output, err := rt.prepareAgentChildActivity(t.Context(), &api.AgentChildActivityInput{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, Call: call, ParentRun: parentRun, HistoryEndID: position})
				rejected := textOnly && !tc.contract
				if rejected {
					assert.Nil(t, output)
					require.ErrorContains(t, err, "incompatible with text-only child execution")
					assert.True(t, engine.IsActivityErrorNonRetryable(err))
					_, seedErr := rt.Store.ListRunSeedRecords(t.Context(), agentChildRunContext(&call).RunID, storage.EmptySeedEndID, "", 1)
					require.ErrorIs(t, seedErr, storage.ErrSeedNotFound)
				} else {
					require.NoError(t, err)
					require.NotNil(t, output.Success)
					assert.Equal(t, textOnly, output.Success.Policy.TextOnly)
				}
				assert.Equal(t, textOnly, config.Policy.TextOnly)
				assert.False(t, call.TextOnly)

				// A resumed child's saved policy must be checked before the planner
				// receives its parent tool, even if preparation was already accepted.
				planning := New(newTestStore())
				seen := false
				planning.agents[child.route.ID] = AgentRegistration{Definition: child, Planner: &stubPlanner{
					start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
						seen = true
						if textOnly {
							assert.Equal(t, "Read values.", input.ParentTool.Description)
						} else {
							assert.Contains(t, input.ParentTool.Description, "card")
						}
						return &planner.PlanResult{FinalToolResult: &planner.FinalToolResult{Result: rawjson.Message(`{"value":1}`)}}, nil
					},
				}}
				nested := agentChildRunContext(&call)
				_, err = planning.PlanStartActivity(t.Context(), seedTestPlanInput(t, planning, PlanActivityInput{
					AgentID: child.route.ID, RunID: nested.RunID, RunContext: nested, Policy: &PolicyOverrides{TextOnly: textOnly},
				}, nil))
				if rejected {
					require.ErrorContains(t, err, "incompatible with text-only child execution")
					assert.True(t, engine.IsActivityErrorNonRetryable(err))
					assert.False(t, seen)
				} else {
					require.NoError(t, err)
					assert.True(t, seen)
				}
			})
		}
	}
}

func TestRegistryAgentPlannerReceivesOriginalResultContract(t *testing.T) {
	rt := New(newTestStore())
	call := testNativeRegistryCall(t, "revision/1")
	child := testNativeProviderDefinition(t, nil)
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
	output, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, PlanActivityInput{
		AgentID: child.route.ID, RunID: nested.RunID, RunContext: nested,
	}, nil))
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.NotNil(t, output.Result)
	assert.True(t, seen)
	assert.JSONEq(t, `{"value":9007199254740993}`, string(output.Result.FinalToolResult.Result))

	result, err := rt.adaptAgentChildOutput(&AgentToolConfig{Definition: child}, &call, nested, &RunOutput{
		Subject:         genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(child.route.ID))},
		FinalToolResult: &api.ToolEvent{Name: call.Name, Result: output.Result.FinalToolResult.Result},
	})
	require.NoError(t, err)
	assert.Equal(t, json.Number("9007199254740993"), result.Result.(map[string]any)["value"])
	require.NotNil(t, result.RunLink)
	_, err = rt.adaptAgentChildOutput(&AgentToolConfig{Definition: child}, &call, nested, &RunOutput{
		Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(child.route.ID))},
		Final:   &model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "done"}}},
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
	restored := restoreCheckpointRunContext(saved, &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("generic.agent"))}, RunID: "continued", SessionID: call.SessionID, ParentRunID: nested.ParentRunID})
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
	binding, err := resolved.Select(testRegistryName, "records.find")
	require.NoError(t, err)
	return ToolCall{
		Name: "records.find", Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent("records.agent"))}, RunID: "parent-run",
		SessionID: "session-1", TurnID: "turn-1", ToolCallID: "call-child",
		Payload: rawjson.Message(`{"value":1}`), Labels: map[string]string{"facility": "facility-1"},
		Registry: binding,
	}
}

func testNativeRegistryResolution(revision string) *genregistry.ResolvedToolset {
	resolution := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	contract := resolution.Toolset.Tools[0].ConsumerContract
	contract.Kind = registryAgentKind
	contract.Agent = &genregistry.AgentToolTarget{Executor: "generic.agent", Configuration: revision, Workflow: "generic.workflow", Queue: "generic.queue"}
	contract.Confirmation = nil
	return resolution
}

func TestRegistryAgentConcurrentChildrenKeepIndependentConfigurations(t *testing.T) {
	rt := New(newTestStore())
	child := testNativeProviderDefinition(t, nil)
	parent := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[parent.route.ID] = AgentRegistration{Definition: parent}
	rt.agents[child.route.ID] = AgentRegistration{Definition: child}
	require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, revision string, _ *ToolCall) (*AgentToolConfiguration, error) {
		return &AgentToolConfiguration{Labels: map[string]string{"revision": revision}, History: LiteralAgentHistory(nil)}, nil
	}))
	calls := []ToolCall{testNativeRegistryCall(t, "revision/1"), testNativeRegistryCall(t, "revision/2")}
	calls[1].ToolCallID = "call-other"
	children := make(chan *controlledChildHandle, 2)
	wf := &testWorkflowContext{ctx: t.Context(), hookRuntime: rt, controlledChildHandles: children}
	parentRun := &run.Context{RunID: calls[0].RunID, SessionID: calls[0].SessionID, TurnID: calls[0].TurnID}
	seedRunMeta(t, rt, &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(parent.route.ID))}, RunID: parentRun.RunID, SessionID: parentRun.SessionID, TurnID: parentRun.TurnID})
	type outcome struct {
		results []*ToolExecutionResult
		err     error
	}
	done := make(chan outcome, 1)
	historyEndID := testToolHistory(t, rt, parent.route.ID, *(parentRun), nil)
	go func() {
		results, _, err := rt.executeToolCalls(nil, wf, "execute", engine.ActivityOptions{}, parent.route.ID, parentRun, historyEndID, calls, 0, nil, time.Time{}, nil)
		done <- outcome{results, err}
	}()
	first := waitForChildHandle(t, children, "first native child")
	second := waitForChildHandle(t, children, "second native child")
	assert.NotEqual(t, wf.childRequests[0].Input.RunID, wf.childRequests[1].Input.RunID)
	assert.Equal(t, "revision/1", wf.childRequests[0].Input.Labels["revision"])
	assert.Equal(t, "revision/2", wf.childRequests[1].Input.Labels["revision"])
	second.out = &RunOutput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(child.route.ID))}, RunID: wf.childRequests[1].Input.RunID,
		FinalToolResult: &api.ToolEvent{Name: calls[1].Name, Result: rawjson.Message(`{"value":2}`)}}
	close(second.ready)
	first.out = &RunOutput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(child.route.ID))}, RunID: wf.childRequests[0].Input.RunID,
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
	child := testNativeProviderDefinition(t, nil)
	parent := testRegistryAgentDefinition(testRegistrySources{})
	rt.agents[parent.route.ID] = AgentRegistration{Definition: parent}
	rt.agents[child.route.ID] = AgentRegistration{Definition: child}
	require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(context.Context, genregistry.ExecutionInvocation, string, *ToolCall) (*AgentToolConfiguration, error) {
		return &AgentToolConfiguration{History: LiteralAgentHistory(nil)}, nil
	}))
	call := testNativeRegistryCall(t, "revision/1")
	parentRun := &run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
	seedRunMeta(t, rt, &RunInput{Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(parent.route.ID))}, RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	children := make(chan *controlledChildHandle, 1)
	wf := &testWorkflowContext{ctx: ctx, hookRuntime: rt, controlledChildHandles: children}
	done := make(chan error, 1)
	historyEndID := testToolHistory(t, rt, parent.route.ID, *parentRun, nil)
	go func() {
		_, _, err := rt.executeToolCalls(nil, wf, "execute", engine.ActivityOptions{}, parent.route.ID, parentRun, historyEndID, []ToolCall{call}, 0, nil, time.Time{}, nil)
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
	parent := testRegistryAgentDefinition(testRegistrySources{})
	current := testNativeRegistryResolution("revision/1")
	client := &genregistry.Client{ResolveToolsetEndpoint: func(context.Context, any) (any, error) { return current, nil }}
	require.NoError(t, rt.RegisterRegistry(testRegistryName, client, unusedRegistryPulse{}))
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
	require.NoError(t, err)
}

func TestRegistryAgentDoesNotExposeProviderPrivateDefinitions(t *testing.T) {
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
	_, err = childDefinitionForCall(ToolCall{Name: grandchildTool.Name}, selected)
	require.ErrorContains(t, err, "not in the current agent definition")
	assert.Empty(t, selected.specs)
}

// testNativeProviderDefinition declares the exported operation that this worker
// owns. Consumers receive its declaration without this private definition.
func testNativeProviderDefinition(t *testing.T, labels []string) AgentDefinition {
	t.Helper()
	resolved, err := registrycontract.Resolve(testNativeRegistryResolution("revision/1"))
	require.NoError(t, err)
	return testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{resolved.Specs["records.find"]}, labels)
}

func TestRegistryAgentPreparedExecutionInput(t *testing.T) {
	for _, test := range []struct {
		name    string
		paging  bool
		payload rawjson.Message
		reject  bool
	}{
		{name: "fresh valid", payload: rawjson.Message(`{"value":1}`)},
		{name: "fresh missing execution field", payload: rawjson.Message(`{}`), reject: true},
		{name: "compiled native page", paging: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolution := testNativeRegistryResolution("revision/1")
			if test.paging {
				resolution = testRegistryPagingResolution(strings.Repeat("a", 64))
				for _, declaration := range resolution.Toolset.Tools {
					declaration.ConsumerContract.Kind = registryAgentKind
					declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "generic.agent", Configuration: "revision/1", Workflow: "generic.workflow", Queue: "generic.queue"}
				}
				target := resolution.Toolset.Tools[1]
				target.ExecutionPayloadSchema = []byte(`{"type":"object","properties":{"value":{"type":"integer"},"cursor":{"type":"string"}},"required":["value","cursor"],"additionalProperties":false}`)
				target.ConsumerContract.Bounds.Paging.RetainedFields = []string{"value"}
			}
			resolved, err := registrycontract.Resolve(resolution)
			require.NoError(t, err)
			specs := make([]tools.ToolSpec, 0, len(resolved.Specs))
			for _, spec := range resolved.Specs {
				specs = append(specs, spec)
			}
			child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", specs, nil)
			parent := testRegistryAgentDefinition(testRegistrySources{})
			rt := New(newTestStore())
			rt.agents[parent.route.ID] = AgentRegistration{Definition: parent}
			rt.agents[child.route.ID] = AgentRegistration{Definition: child}
			call := testNativeRegistryCall(t, "revision/1")
			call.Payload = test.payload
			call.Registry, err = resolved.Select(testRegistryName, "records.find")
			require.NoError(t, err)
			if test.paging {
				output := sourceContinuationOutput("records.find", "selection-1", `{"value":7}`, "page-2")
				output.Registry = call.Registry
				actions, err := rt.availableContinuationActions(parent.route.ID, []*planner.ToolOutput{output}, false)
				require.NoError(t, err)
				require.Len(t, actions, 1)
				calls, err := rt.compilePlannerToolCalls([]planner.ToolRequest{{Name: actions[0].modelName, ModelToolCallID: "page-call", Payload: rawjson.Message(`{}`)}}, actions, map[string]model.ToolCall{"page-call": {ID: "page-call", Name: actions[0].modelName, Payload: rawjson.Message(`{}`)}})
				require.NoError(t, err)
				require.Len(t, calls, 1)
				call.Name = calls[0].Name
				call.Payload = calls[0].Payload
				call.Registry = calls[0].Registry
				call.ModelPayload = calls[0].ModelPayload
				assert.JSONEq(t, `{}`, string(call.ModelPayload))
				assert.JSONEq(t, `{"value":7,"cursor":"page-2"}`, string(call.Payload))
				_, err = resolved.Specs[call.Name].Payload.Codec.FromJSON(call.Payload)
				require.Error(t, err)
			}
			received := false
			require.NoError(t, rt.RegisterAgentToolResolver(child.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, configuration string, prepared *ToolCall) (*AgentToolConfiguration, error) {
				received = true
				assert.Equal(t, "revision/1", configuration)
				assert.Equal(t, call.Payload, prepared.Payload)
				assert.Equal(t, call.ModelPayload, prepared.ModelPayload)
				return &AgentToolConfiguration{History: LiteralAgentHistory(nil)}, nil
			}))
			_, err = rt.prepareRegistryAgentChild(t.Context(), genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, call)
			if test.reject {
				assert.Error(t, err) //nolint:testifylint // The provider callback count must still be checked if preparation returns no error.
				assert.False(t, received)
			} else {
				require.NoError(t, err)
				assert.True(t, received)
			}
		})
	}
}
