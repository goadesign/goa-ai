// Package runtime tests static native registration binding with synthetic providers
// and concrete generated record codecs. Saved selections must retain their
// configuration and generated contracts through replacement and continuation.
package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

// staticAgentCatalog uses concrete generated record codecs with a synthetic
// native provider route, so binding cannot silently replace typed values.
func staticAgentCatalog(t *testing.T) (*Runtime, *RegistryCatalog, []*genregistry.ToolSchema, *genregistry.ResolvedToolset) {
	t.Helper()
	selected := &genregistry.ResolvedToolset{Toolset: genrecords.Toolset(), RegistrationToken: strings.Repeat("a", 64)}
	var operation *genregistry.ToolSchema
	for _, candidate := range selected.Toolset.Tools {
		if candidate.Name == string(genrecords.Read) {
			operation = candidate
		}
	}
	require.NotNil(t, operation)
	operation.ConsumerContract.Kind = "agent"
	expected := *operation
	expectedContract := *operation.ConsumerContract
	expected.ConsumerContract = &expectedContract
	operation.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "generic.agent", Workflow: "generic.workflow", Queue: "generic.queue", Configuration: "revision/1"}
	selected.Toolset.Tools = []*genregistry.ToolSchema{operation}
	selected.Toolset.RegisteredAt = "2026-09-17T00:00:00Z"
	spec := genrecords.SpecRead()
	spec.IsAgentTool = true
	spec.AgentID = "generic.agent"
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, []tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, []AgentDefinition{child}, nil).WithRegistryTools(testRegistrySources{whole: true})
	rt := New(newTestStore())
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	rt.toolSpecs[spec.Name] = spec
	var err error
	rt.toolDefinitions[spec.Name], err = model.NewToolDefinitionFromSpec(spec)
	require.NoError(t, err)
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	return rt, rt.newRegistryCatalog(definition), []*genregistry.ToolSchema{&expected}, selected
}

func TestStaticAgentBindingRetainsTypedCodecsAndSelectedConfiguration(t *testing.T) {
	_, catalog, expected, selected := staticAgentCatalog(t)
	selected.Toolset.Description = new("Registration discovery description")
	selected.Toolset.Tags = []string{"discovery"}
	require.NoError(t, catalog.BindStaticAgent(testRegistryName, expected, selected, genrecords.Read))
	original := catalog.definition.specByName[genrecords.Read]
	bound := original
	bound.PrepareInput = func(value any) (rawjson.Message, error) {
		payload := value.(*genrecords.ReadPayload)
		payload.Query = "authorized:" + payload.Query
		return original.ExecutionPayloadCodec.ToJSON(payload)
	}
	require.NoError(t, catalog.BindInput(original, bound))
	payload, err := prepareConsumerInput(catalog.spec, genrecords.Read, rawjson.Message(`{"query":"selected"}`))
	require.NoError(t, err)
	assert.Contains(t, string(payload), "authorized:selected")
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	selected.Toolset.Tools[0].ConsumerContract.Agent.Configuration = "revision/2"
	selected.RegistrationToken = strings.Repeat("b", 64)
	call := ToolCall{Name: genrecords.Read, Registry: binding}
	spec, exists, err := lookupOwnedCallSpec(call, catalog.definition.spec)
	require.NoError(t, err)
	require.True(t, exists)
	result, err := spec.Result.Codec.FromJSON([]byte(`{"count":7}`))
	require.NoError(t, err)
	assert.IsType(t, &genrecords.ReadResult{}, result)
	resolved, err := validateRegistrySource(catalog.definition, call)
	require.NoError(t, err)
	assert.Equal(t, "revision/1", resolved.Registered.Toolset.Tools[0].ConsumerContract.Agent.Configuration)
	assert.Equal(t, strings.Repeat("a", 64), resolved.Registered.RegistrationToken)
	replacement := catalog.runtime.newRegistryCatalog(catalog.definition)
	require.NoError(t, replacement.BindStaticAgent(testRegistryName, expected, selected, genrecords.Read))
	direct, err := childDefinitionForCall(ToolCall{Name: genrecords.Read}, catalog.definition)
	require.NoError(t, err)
	assert.Equal(t, "generic.workflow", direct.route.WorkflowName)
}

func TestStaticAgentBindingRejectsChangedContracts(t *testing.T) {
	for _, change := range []string{"schema", "policy", "executor", "workflow", "queue", "configuration", "duplicate", "missing", "extra", "bound declaration", "service", "consumption"} {
		t.Run(change, func(t *testing.T) {
			_, catalog, expected, selected := staticAgentCatalog(t)
			names := []tools.Ident{genrecords.Read}
			target := selected.Toolset.Tools[0].ConsumerContract.Agent
			switch change {
			case "schema":
				selected.Toolset.Tools[0].PayloadSchema = []byte(`{"type":"string"}`)
			case "policy":
				selected.Toolset.Tools[0].ConsumerContract.RequiresUI = true
			case "executor":
				target.Executor = "other.agent"
			case "workflow":
				target.Workflow = "other.workflow"
			case "queue":
				target.Queue = "other.queue"
			case "configuration":
				target.Configuration = ""
			case "duplicate":
				expected = append(expected, expected[0])
				names = append(names, names[0])
			case "missing":
				names = nil
			case "extra":
				selected.Toolset.Tools = append(selected.Toolset.Tools, selected.Toolset.Tools[0])
			case "bound declaration":
				expected[0].ConsumerContract.Agent = target
			case "service":
				selected.Toolset.Tools[0].ConsumerContract.Kind = "service"
				selected.Toolset.Tools[0].ConsumerContract.Agent = nil
			case "consumption":
				catalog.sources = nil
			}
			require.Error(t, catalog.BindStaticAgent(testRegistryName, expected, selected, names...))
			assert.Empty(t, catalog.selections)
		})
	}
}

func TestStaticAgentSavedSourceRejectsChangedRouteAndForeignSource(t *testing.T) {
	for _, change := range []string{"workflow", "queue", "executor", "foreign source"} {
		t.Run(change, func(t *testing.T) {
			_, catalog, _, selected := staticAgentCatalog(t)
			target := selected.Toolset.Tools[0].ConsumerContract.Agent
			switch change {
			case "workflow":
				target.Workflow = "other.workflow"
			case "queue":
				target.Queue = "other.queue"
			case "executor":
				target.Executor = "other.agent"
			}
			resolved, err := registrycontract.Resolve(selected)
			require.NoError(t, err)
			if change == "foreign source" {
				resolved.Source = &genregistry.CatalogSource{Scope: "owner", Name: "resource", Target: genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: selected.Toolset.Name})}
				require.Error(t, validateStaticRegistrySpec(catalog.definition.specByName[genrecords.Read], resolved))
				return
			}
			binding, err := resolved.Select(testRegistryName, genrecords.Read)
			require.NoError(t, err)
			_, err = validateRegistrySource(catalog.definition, ToolCall{Name: genrecords.Read, Registry: binding})
			require.Error(t, err)
		})
	}
}

func TestStaticAgentPagingRetainsOwnedContinuationCodecAndRevision(t *testing.T) {
	selected := testRegistryPagingResolution(strings.Repeat("a", 64))
	expected := make([]*genregistry.ToolSchema, len(selected.Toolset.Tools))
	for index, operation := range selected.Toolset.Tools {
		operation.ConsumerContract.Kind = "agent"
		copy := *operation
		contract := *operation.ConsumerContract
		copy.ConsumerContract = &contract
		expected[index] = &copy
		operation.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "generic.agent", Workflow: "generic.workflow", Queue: "generic.queue", Configuration: "revision/1"}
	}
	resolved, err := registrycontract.Resolve(selected)
	require.NoError(t, err)
	specs := []tools.ToolSpec{resolved.Specs["records.find"], resolved.Specs["records.continue"]}
	decoded := false
	codec := specs[1].ExecutionPayloadCodec.FromJSON
	specs[1].ExecutionPayloadCodec.FromJSON = func(data []byte) (any, error) { decoded = true; return codec(data) }
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, specs, nil, nil, []tools.Ident{"records.find", "records.continue"}, []AgentDefinition{child}, nil).WithRegistryTools(testRegistrySources{})
	rt := New(newTestStore())
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	catalog := rt.newRegistryCatalog(definition)
	require.NoError(t, catalog.BindStaticAgent(testRegistryName, expected, selected, "records.find", "records.continue"))
	binding, err := catalog.selections["records.find"].resolution.Select(testRegistryName, "records.find")
	require.NoError(t, err)
	output := sourceContinuationOutput("records.find", "query", `{"value":1}`, "cursor")
	output.Registry = binding
	actions, err := rt.availableContinuationActions(definition.route.ID, []*planner.ToolOutput{output}, false)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.True(t, decoded)
	retained, err := registrycontract.Read(actions[0].registry)
	require.NoError(t, err)
	assert.Equal(t, "revision/1", retained.Registered.Toolset.Tools[0].ConsumerContract.Agent.Configuration)
}

func TestStaticAgentCheckpointRetainsConcreteResultsAfterReplacement(t *testing.T) {
	rt, catalog, expected, selected := staticAgentCatalog(t)
	require.NoError(t, catalog.BindStaticAgent(testRegistryName, expected, selected, genrecords.Read))
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	selected.Toolset.Tools[0].ConsumerContract.Agent.Configuration = "revision/2"
	selected.RegistrationToken = strings.Repeat("b", 64)
	replacement := rt.newRegistryCatalog(catalog.definition)
	require.NoError(t, replacement.BindStaticAgent(testRegistryName, expected, selected, genrecords.Read))
	call := ToolCall{Name: genrecords.Read, Registry: binding, Payload: rawjson.Message(`{"query":"all"}`)}
	state := checkpointRunState{ToolOutputs: []*planner.ToolOutput{{Name: call.Name, ToolCallID: "call-1", Registry: binding}}, ToolEvents: []*api.ToolEvent{{Name: call.Name, ToolCallID: "call-1", Result: rawjson.Message(`{"count":4}`)}}}
	restored, err := rt.restoreCheckpointState(state, catalog.definition.spec)
	require.NoError(t, err)
	assert.Equal(t, &genrecords.ReadResult{Count: 4}, restored.ToolEvents[0].Result)
	require.NoError(t, validateCheckpointToolRequest(call, catalog.definition))
	child, err := childDefinitionForCall(call, catalog.definition)
	require.NoError(t, err)
	assert.Equal(t, "generic.workflow", child.route.WorkflowName)
	retained, err := registrycontract.Read(restored.ToolOutputs[0].Registry)
	require.NoError(t, err)
	assert.Equal(t, "revision/1", retained.Registered.Toolset.Tools[0].ConsumerContract.Agent.Configuration)
}

func TestStaticAgentBindingDoesNotCaptureAnotherAgentsDynamicCall(t *testing.T) {
	rt, catalog, expected, selected := staticAgentCatalog(t)
	require.NoError(t, catalog.BindStaticAgent(testRegistryName, expected, selected, genrecords.Read))
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	other := testAgentDefinition("other.agent", "other.workflow", "other.queue", nil, nil).WithRegistryTools(testRegistrySources{whole: true})
	rt.agents[other.route.ID] = AgentRegistration{Definition: other}
	call := ToolCall{Name: genrecords.Read, Registry: binding, Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("other.agent")}}
	assert.True(t, rt.isDynamicRegistryCall(call))
	spec, exists, err := rt.callSpec(call)
	require.NoError(t, err)
	require.True(t, exists)
	value, err := spec.Result.Codec.FromJSON([]byte(`{"count":3}`))
	require.NoError(t, err)
	assert.IsType(t, map[string]any{}, value)
	require.NoError(t, validateCheckpointToolRequest(ToolCall{Name: genrecords.Read, Registry: binding, Payload: rawjson.Message(`{"query":"all"}`)}, other))
}

func TestStaticAgentPreparationRecoveryAndTypedChildCompletion(t *testing.T) {
	rt, catalog, expected, selected := staticAgentCatalog(t)
	require.NoError(t, catalog.BindStaticAgent(testRegistryName, expected, selected, genrecords.Read))
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	spec := catalog.definition.specByName[genrecords.Read]
	provider := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", []tools.ToolSpec{spec}, nil)
	rt.agents[provider.route.ID] = AgentRegistration{Definition: provider}
	resolverCalls := 0
	require.NoError(t, rt.RegisterAgentToolResolver(provider.route.ID, func(_ context.Context, _ genregistry.ExecutionInvocation, revision string, call *ToolCall) (*AgentToolConfiguration, error) {
		resolverCalls++
		assert.Equal(t, "revision/1", revision)
		assert.Equal(t, genrecords.Read, call.Name)
		return &AgentToolConfiguration{History: LiteralAgentHistory([]*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Read the selected records"}}}})}, nil
	}))
	call := ToolCall{Name: genrecords.Read, Registry: binding, ToolCallID: "call-1", RunID: "parent-run", SessionID: "session-1", TurnID: "turn-1", Payload: rawjson.Message(`{"query":"all"}`), Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("records.agent")}}
	parentRun := run.Context{RunID: call.RunID, SessionID: call.SessionID, TurnID: call.TurnID}
	position := testToolHistory(t, rt, catalog.definition.route.ID, parentRun, nil)
	input := &api.AgentChildActivityInput{Origin: genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindTool(&genregistry.ToolInvocation{RunID: call.RunID, ToolCallID: call.ToolCallID})}, Call: call, ParentRun: parentRun, HistoryEndID: position}
	first, err := rt.prepareAgentChildActivity(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, first.Success)
	selected.Toolset.Tools[0].ConsumerContract.Agent.Configuration = "revision/2"
	// Repeating an accepted preparation models a lost activity reply. The saved
	// preparation must return unchanged without resolving the provider again.
	recovered, err := rt.prepareAgentChildActivity(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, first, recovered)
	assert.Equal(t, 1, resolverCalls)
	cfg, err := rt.selectedAgentToolConfig(call)
	require.NoError(t, err)
	result, err := rt.adaptAgentChildOutput(cfg, &call, agentChildRunContext(&call), &RunOutput{FinalToolResult: &api.ToolEvent{Name: genrecords.Read, Result: rawjson.Message(`{"count":9}`)}})
	require.NoError(t, err)
	assert.Equal(t, &genrecords.ReadResult{Count: 9}, result.Result)
	assert.Equal(t, call.ToolCallID, result.ToolCallID)
	require.NotNil(t, result.RunLink)
	assert.Equal(t, agentChildRunContext(&call).RunID, result.RunLink.RunID)
}
