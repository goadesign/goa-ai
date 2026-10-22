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

func staticServiceCatalog(t *testing.T) (*Runtime, *RegistryCatalog, *genregistry.ResolvedToolset) {
	t.Helper()
	rt := New(newTestStore())
	definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, genrecords.Specs(), nil, nil, []tools.Ident{genrecords.Read, genrecords.Erase, genrecords.Show}, nil, []tools.Ident{genrecords.Read}).WithRegistryTools(testRegistrySources{whole: true}).WithRegistryDeclarations(genrecords.ToolSchema)
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	for _, spec := range genrecords.Specs() {
		rt.toolSpecs[spec.Name] = spec
		var err error
		rt.toolDefinitions[spec.Name], err = model.NewToolDefinitionFromSpec(spec)
		require.NoError(t, err)
	}
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	selected := &genregistry.ResolvedToolset{Toolset: genrecords.Toolset(), RegistrationToken: strings.Repeat("a", 64)}
	selected.Toolset.Name = "tenant.records"
	selected.Toolset.RegisteredAt = "2026-09-17T00:00:00Z"
	return rt, rt.newRegistryCatalog(definition), selected
}

func TestStaticServiceBindingRetainsGeneratedCodecsAndInputPreparation(t *testing.T) {
	rt, catalog, selected := staticServiceCatalog(t)
	require.NoError(t, catalog.BindStaticService(testRegistryName, genrecords.Toolset(), selected, genrecords.Read))
	original := genrecords.SpecRead()
	bound := genrecords.SpecRead()
	bound.PrepareInput = func(value any) (rawjson.Message, error) {
		payload := value.(*genrecords.ReadPayload)
		payload.Query = "authorized:" + payload.Query
		return original.ExecutionPayloadCodec.ToJSON(payload)
	}
	require.NoError(t, catalog.BindInput(original, bound))
	payload, err := prepareConsumerInput(catalog, genrecords.Read, rawjson.Message(`{"query":"selected"}`))
	require.NoError(t, err)
	assert.Contains(t, string(payload), "authorized:selected")
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	selected.Toolset.Tools[1].Name = "changed.after.selection"
	call := ToolCall{Name: genrecords.Read, Registry: binding, Payload: payload, ModelPayload: rawjson.Message(`{"query":"selected"}`), Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("records.agent")}}
	spec, exists, err := rt.callSpec(call)
	require.NoError(t, err)
	require.True(t, exists)
	decoded, err := spec.Result.Codec.FromJSON([]byte(`{"count":7}`))
	require.NoError(t, err)
	assert.IsType(t, &genrecords.ReadResult{}, decoded)
	assert.Equal(t, rawjson.Message(`{"query":"selected"}`), call.TranscriptPayload())
	assert.False(t, rt.isDynamicRegistryCall(call))
	ctx := newAgentContext(agentContextOptions{runtime: rt, agentID: catalog.definition.route.ID, catalog: catalog, advertisedSpecs: []tools.ToolSpec{catalog.specs[genrecords.Read]}})
	definitions := ctx.AdvertisedToolDefinitions()
	require.Len(t, definitions, 1)
	assert.True(t, definitions[0].Deferred)
}

func TestStaticServiceBindingRejectsMismatches(t *testing.T) {
	for _, kind := range []string{"schema", "version", "token", "registry", "membership", "empty", "duplicate", "native", "local policy", "complete declaration"} {
		t.Run(kind, func(t *testing.T) {
			_, catalog, selected := staticServiceCatalog(t)
			expected := genrecords.Toolset()
			registry := testRegistryName
			names := []tools.Ident{genrecords.Read}
			switch kind {
			case "schema":
				selected.Toolset.Tools[1].Description = new("changed")
			case "version":
				selected.Toolset.Version = new(genregistry.SemVer("2.0.0"))
			case "token":
				selected.RegistrationToken = "invalid"
			case "registry":
				registry = "unregistered"
			case "membership":
				delete(catalog.definition.specByName, genrecords.Read)
			case "empty":
				names = nil
			case "duplicate":
				names = []tools.Ident{genrecords.Read, genrecords.Read}
			case "native":
				selected = testNativeRegistryResolution("revision/1")
				expected = selected.Toolset
			case "local policy":
				spec := catalog.definition.specByName[genrecords.Read]
				spec.Bookkeeping = true
				catalog.definition.specByName[genrecords.Read] = spec
			case "complete declaration":
				selected.Toolset.Tools = selected.Toolset.Tools[:2]
			}
			require.Error(t, catalog.BindStaticService(registry, expected, selected, names...))
			assert.Empty(t, catalog.selections)
		})
	}
}

func TestBoundStaticAndUnboundStaticDoNotCaptureOtherAgentDynamicCalls(t *testing.T) {
	rt, catalog, selected := staticServiceCatalog(t)
	require.ErrorContains(t, catalog.IncludeResolved(testRegistryName, selected, false), "repeats tool")
	require.NoError(t, catalog.BindStaticService(testRegistryName, genrecords.Toolset(), selected, genrecords.Read))
	compiled, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	other := testRegistryAgentDefinition(testRegistrySources{whole: true})
	other.route.ID = "other.agent"
	rt.agents[other.route.ID] = AgentRegistration{Definition: other}
	// An agent that does not compile the tool cannot interpret a compiled
	// selection; a full saved declaration stays a dynamic call for it.
	_, _, err = rt.callSpec(ToolCall{Name: genrecords.Read, Registry: compiled, Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("other.agent")}})
	require.ErrorIs(t, err, registrycontract.ErrCompiledContractChanged)
	binding := testDirectSelection(t, selected, genrecords.Read)
	call := ToolCall{Name: genrecords.Read, Registry: binding, Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("other.agent")}}
	assert.True(t, rt.isDynamicRegistryCall(call))
	spec, _, err := rt.callSpec(call)
	require.NoError(t, err)
	value, err := spec.Result.Codec.FromJSON([]byte(`{"count":3}`))
	require.NoError(t, err)
	assert.IsType(t, map[string]any{}, value)
	call.Registry = nil
	spec, _, err = rt.callSpec(call)
	require.NoError(t, err)
	value, err = spec.Result.Codec.FromJSON([]byte(`{"count":3}`))
	require.NoError(t, err)
	assert.IsType(t, &genrecords.ReadResult{}, value)
}

func TestBoundServiceCheckpointUsesGeneratedCodecWithoutDiscovery(t *testing.T) {
	rt, catalog, selected := staticServiceCatalog(t)
	require.NoError(t, catalog.BindStaticService(testRegistryName, genrecords.Toolset(), selected, genrecords.Read))
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(t, err)
	call := ToolCall{Name: genrecords.Read, Registry: binding, Payload: rawjson.Message(`{"query":"all"}`)}
	state := checkpointRunState{ToolOutputs: []*planner.ToolOutput{{Name: call.Name, ToolCallID: "call-1", Registry: binding}}, ToolEvents: []*api.ToolEvent{{Name: call.Name, ToolCallID: "call-1", Result: rawjson.Message(`{"count":4}`)}}}
	restored, err := rt.restoreCheckpointState(state)
	require.NoError(t, err)
	assert.Equal(t, historicalToolResult{result: rawjson.Message(`{"count":4}`)}, restored.ToolEvents[0].Result)
	require.NoError(t, validateCheckpointToolRequest(call, catalog.definition))
	changed := catalog.definition
	changed.specByName = map[tools.Ident]tools.ToolSpec{genrecords.Read: genrecords.SpecRead()}
	spec := changed.specByName[genrecords.Read]
	spec.Result.Schema = []byte(`{"type":"string"}`)
	changed.specByName[genrecords.Read] = spec
	require.ErrorContains(t, validateCheckpointToolRequest(call, changed), "differs")
	assert.Empty(t, requiredCheckpointToolNames(&workflowCheckpoint{State: state}))
}

func TestBoundStaticConfirmationAndProvidedResultKeepGeneratedTypes(t *testing.T) {
	rt, catalog, selected := staticServiceCatalog(t)
	require.NoError(t, catalog.BindStaticService(testRegistryName, genrecords.Toolset(), selected, genrecords.Erase))
	binding, err := catalog.selections[genrecords.Erase].resolution.Select(testRegistryName, genrecords.Erase)
	require.NoError(t, err)
	call := ToolCall{Name: genrecords.Erase, ToolCallID: "erase-call", RunID: "original-run", Registry: binding, Payload: rawjson.Message(`{}`), Subject: genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("records.agent")}}
	plan, needed, err := rt.confirmationPlan(t.Context(), &call)
	require.NoError(t, err)
	require.True(t, needed)
	assert.Equal(t, "Erase the records?", plan.Prompt)
	assert.Equal(t, &genrecords.EraseResult{Erased: false}, plan.DeniedResult)
	materialized := false
	delete(rt.toolSpecs, genrecords.Erase)
	delete(rt.toolDefinitions, genrecords.Erase)
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{
		Name: "records", Specs: []tools.ToolSpec{genrecords.SpecErase()},
		Execute: wrapExecute(func(context.Context, *ToolCall) (*planner.ToolResult, error) {
			t.Fatal("provided result must not execute the service again")
			return nil, nil
		}),
		ResultMaterializer: func(_ context.Context, meta ToolCallMeta, saved *ToolCall, result *planner.ToolResult) error {
			materialized = true
			assert.Equal(t, "original-run", meta.RunID)
			assert.Equal(t, binding, saved.Registry)
			assert.Equal(t, &genrecords.EraseResult{Erased: true}, result.Result)
			return nil
		},
	}))
	records, err := rt.decodeProvidedToolRecords(t.Context(), []ToolCall{call}, map[string]*api.ProvidedToolResult{
		call.ToolCallID: {Name: call.Name, ToolCallID: call.ToolCallID, Success: &api.ProvidedToolSuccess{Result: rawjson.Message(`{"erased":true}`)}},
	})
	require.NoError(t, err)
	require.Len(t, records, 1)
	require.Nil(t, records[0].result.Failure)
	assert.True(t, materialized)
	assert.Equal(t, &genrecords.EraseResult{Erased: true}, records[0].result.Result)
	assert.JSONEq(t, `{"erased":true}`, string(records[0].resultJSON))
	assert.Equal(t, binding, records[0].call.Registry)
}

func TestSelectedOperationCopiesOriginalDirectAndRegisteredDeclaration(t *testing.T) {
	for _, direct := range []bool{true, false} {
		rt := New(newTestStore())
		definition := testNativeProviderDefinition(t, nil)
		catalog := rt.newRegistryCatalog(definition)
		selected := testNativeRegistryResolution("revision/original")
		catalog.runContext = run.Context{Tool: "records.find"}
		if direct {
			catalog.runContext.Operation = selected.Toolset.Tools[0]
		} else {
			resolved, err := registrycontract.Resolve(selected)
			require.NoError(t, err)
			catalog.runContext.ToolRegistry, err = resolved.Select(testRegistryName, "records.find")
			require.NoError(t, err)
		}
		operation, err := catalog.SelectedOperation()
		require.NoError(t, err)
		assert.Equal(t, "revision/original", operation.ConsumerContract.Agent.Configuration)
		operation.ConsumerContract.Agent.Configuration = "changed"
		again, err := catalog.SelectedOperation()
		require.NoError(t, err)
		assert.Equal(t, "revision/original", again.ConsumerContract.Agent.Configuration)
	}
}

func TestSelectedOperationRejectsMissingAndMalformedContext(t *testing.T) {
	rt := New(newTestStore())
	catalog := rt.newRegistryCatalog(testNativeProviderDefinition(t, nil))
	_, err := catalog.SelectedOperation()
	require.ErrorContains(t, err, "no selected")
	catalog.runContext.ToolRegistry = &tools.RegistryBinding{Registry: testRegistryName, Resolution: []byte(`{}`)}
	_, err = catalog.SelectedOperation()
	require.Error(t, err)
}

func TestStaticFinalizationSkipsRegistrySelection(t *testing.T) {
	rt, catalog, _ := staticServiceCatalog(t)
	called := false
	definition := catalog.definition
	definition.prepareInputs = func(context.Context, *RegistryCatalog) error { called = true; return nil }
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	result, err := rt.planningCatalog(t.Context(), &PlanActivityInput{AgentID: definition.route.ID, SynthesisOnly: true})
	require.NoError(t, err)
	assert.True(t, called)
	assert.Empty(t, result.selections)
}

func TestBoundStaticPagingKeepsSavedTokenAndOwnedContinuationCodec(t *testing.T) {
	rt := New(newTestStore())
	selected := testRegistryPagingResolution(strings.Repeat("a", 64))
	resolved, err := registrycontract.Resolve(selected)
	require.NoError(t, err)
	specs := []tools.ToolSpec{resolved.Specs["records.find"], resolved.Specs["records.continue"]}
	continuationDecoded := false
	originalCodec := specs[1].ExecutionPayloadCodec.FromJSON
	specs[1].ExecutionPayloadCodec.FromJSON = func(data []byte) (any, error) { continuationDecoded = true; return originalCodec(data) }
	definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, specs, nil, nil, []tools.Ident{"records.find", "records.continue"}, nil, nil).WithRegistryTools(testRegistrySources{}).WithRegistryDeclarations(testDeclarations(selected.Toolset.Tools))
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	catalog := rt.newRegistryCatalog(definition)
	require.NoError(t, catalog.BindStaticService(testRegistryName, selected.Toolset, selected, "records.find"))
	binding, err := catalog.selections["records.find"].resolution.Select(testRegistryName, "records.find")
	require.NoError(t, err)
	output := sourceContinuationOutput("records.find", "query", `{"value":1}`, "cursor")
	output.Registry = binding
	actions, err := rt.availableContinuationActions(definition.route.ID, []*planner.ToolOutput{output}, false)
	require.NoError(t, err)
	require.Len(t, actions, 1)
	assert.True(t, continuationDecoded)
	retained, err := registrycontract.Read(actions[0].registry, definition.registryDeclaration)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 64), retained.Registered.RegistrationToken)
	assert.Equal(t, tools.Ident("records.continue"), actions[0].spec.Name)
	assert.NotContains(t, catalog.selections, tools.Ident("records.continue"))
}

func TestSelectedOperationRestoresRegisteredAlias(t *testing.T) {
	rt := New(newTestStore())
	definition := testNativeProviderDefinition(t, nil)
	selected := testNativeRegistryResolution("revision/original")
	source := &genregistry.CatalogSource{Scope: "owner", Name: "resource", Target: genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: selected.Toolset.Name})}
	resolved, err := registrycontract.ResolveRegistered(testRegistryName, source, selected)
	require.NoError(t, err)
	var name tools.Ident
	for candidate := range resolved.Specs {
		name = candidate
	}
	binding, err := resolved.Select(testRegistryName, name)
	require.NoError(t, err)
	catalog := rt.newRegistryCatalog(definition)
	catalog.runContext = run.Context{Tool: name, ToolRegistry: binding, ParentRunID: "parent", ParentToolCallID: "parent-call"}
	operation, err := catalog.SelectedOperation()
	require.NoError(t, err)
	assert.Equal(t, "records.find", operation.Name)
	assert.Equal(t, "revision/original", operation.ConsumerContract.Agent.Configuration)
}
