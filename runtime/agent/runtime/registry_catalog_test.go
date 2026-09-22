package runtime

// These tests use generated registry clients to change the catalog between
// planning activities. No test installs discovered tools in the static runtime.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulsec "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	streamopts "goa.design/pulse/streaming/options"
)

type (
	testRegistrySources struct {
		whole bool
	}
	unusedRegistryPulse struct{}
)

func (s testRegistrySources) Resolve(ctx context.Context, catalog *RegistryCatalog) error {
	if s.whole {
		return catalog.IncludeRegistry(ctx, "company", true)
	}
	return catalog.IncludeToolset(ctx, "company", "provider.records", "1.0.0", true)
}

func (s testRegistrySources) Allows(registry, toolset, version string) bool {
	return registry == "company" && (s.whole || toolset == "provider.records" && version == "1.0.0")
}

func (unusedRegistryPulse) Stream(string, ...streamopts.Stream) (pulsec.Stream, error) {
	return nil, errors.New("catalog resolution must not open result streams")
}

func (unusedRegistryPulse) Close(context.Context) error {
	return nil
}

func TestRegistryCatalogChangesOnlyAtNextResolution(t *testing.T) {
	rt := New(newTestStore())
	current := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	client := &genregistry.Client{ResolveToolsetEndpoint: func(context.Context, any) (any, error) {
		return current, nil
	}}
	require.NoError(t, rt.RegisterRegistry("company", client, unusedRegistryPulse{}))
	definition := testRegistryAgentDefinition(testRegistrySources{})
	first, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	require.Len(t, first.selections, 1)
	assert.True(t, first.definitions["records.find"].Deferred)
	selection := first.selections["records.find"]
	binding, err := selection.resolution.Select(selection.registry, "records.find")
	require.NoError(t, err)

	current = testRuntimeRegistryResolution("records.replace", strings.Repeat("b", 64))
	second, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	assert.Contains(t, first.definitions, tools.Ident("records.find"))
	assert.NotContains(t, first.definitions, tools.Ident("records.replace"))
	assert.Contains(t, second.definitions, tools.Ident("records.replace"))
	assert.NotContains(t, second.definitions, tools.Ident("records.find"))
	restored, err := registrycontract.Read(binding)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 64), restored.Registered.RegistrationToken)
	_, exists := rt.toolSpec("records.find")
	assert.False(t, exists)
}

func TestWholeRegistryDistinguishesEmptyFromFailedResolution(t *testing.T) {
	rt := New(newTestStore())
	listFailure := false
	client := &genregistry.Client{ListToolsetsEndpoint: func(context.Context, any) (any, error) {
		if listFailure {
			return nil, errors.New("registry unavailable")
		}
		return &genregistry.ListToolsetsResult{}, nil
	}}
	require.NoError(t, rt.RegisterRegistry("company", client, unusedRegistryPulse{}))
	definition := testRegistryAgentDefinition(testRegistrySources{whole: true})
	catalog, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	assert.Empty(t, catalog.specs)
	listFailure = true
	_, err = rt.resolveRegistryCatalog(t.Context(), definition)
	require.ErrorContains(t, err, "registry unavailable")
}

func TestRegistryPlanningAdvertisesCurrentToolsAndSavesOnlySelections(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(testRegistrySources{})
	pl := &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		visible := input.Agent.AdvertisedToolDefinitions()
		require.Len(t, visible, 1)
		assert.Equal(t, "records.find", visible[0].Name)
		assert.True(t, visible[0].Deferred)
		return &planner.PlanResult{
			ToolCalls: []planner.ToolRequest{{
				Name: "records.find", Payload: rawjson.Message(`{"value":9007199254740993}`),
			}},
			SynthesizeAfterTools: true,
		}, nil
	}}
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: pl}
	reads := 0
	client := &genregistry.Client{ResolveToolsetEndpoint: func(context.Context, any) (any, error) {
		reads++
		return testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)), nil
	}}
	require.NoError(t, rt.RegisterRegistry("company", client, unusedRegistryPulse{}))
	input := &PlanActivityInput{
		AgentID: definition.route.ID, RunID: "run-1", RunContext: run.Context{RunID: "run-1"},
	}
	output, err := rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.NotNil(t, output.Result)
	require.Len(t, output.Result.ToolCalls, 1)
	call := output.Result.ToolCalls[0]
	require.NotNil(t, call.Registry)
	saved, err := registrycontract.Read(call.Registry)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 64), saved.Registered.RegistrationToken)
	assert.Len(t, saved.Specs, 1)
	assert.Equal(t, 1, reads)

	pl.start = func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		assert.Empty(t, input.Agent.AdvertisedToolDefinitions())
		return &planner.PlanResult{FinalResponse: &planner.FinalResponse{Message: &model.Message{
			Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Done."}},
		}}}, nil
	}
	input.SynthesisOnly = true
	output, err = rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	require.NotNil(t, output.Result.FinalResponse)
	assert.Equal(t, 1, reads, "a tool-free final answer must not read the registry")
}

func TestRegistryCatalogRejectsDuplicatesAndWrongVersion(t *testing.T) {
	rt := New(newTestStore())
	current := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	client := &genregistry.Client{ResolveToolsetEndpoint: func(context.Context, any) (any, error) {
		return current, nil
	}}
	require.NoError(t, rt.RegisterRegistry("company", client, unusedRegistryPulse{}))
	definition := testRegistryAgentDefinition(testRegistrySources{})
	catalog, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	err = catalog.IncludeToolset(t.Context(), "company", "provider.records", "1.0.0", true)
	require.ErrorContains(t, err, "repeats tool")
	version := genregistry.SemVer("2.0.0")
	current.Toolset.Version = &version
	_, err = rt.resolveRegistryCatalog(t.Context(), definition)
	require.ErrorContains(t, err, `requires version "1.0.0"`)
}

func TestSavedRegistryContractOwnsConfirmationAndResultValidation(t *testing.T) {
	rt := New(newTestStore())
	resolved, err := registrycontract.Resolve(testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64)))
	require.NoError(t, err)
	binding, err := resolved.Select("company", "records.find")
	require.NoError(t, err)
	call := ToolCall{
		Name: "records.find", Registry: binding,
		Payload: rawjson.Message(`{"value":9007199254740993}`),
	}
	plan, needed, err := rt.confirmationPlan(t.Context(), &call)
	require.NoError(t, err)
	require.True(t, needed)
	assert.Equal(t, "Change value 9007199254740993?", plan.Prompt)
	spec, exists, err := lookupCallSpec(call, rt.toolSpec)
	require.NoError(t, err)
	require.True(t, exists)
	denied, err := spec.Result.Codec.ToJSON(plan.DeniedResult)
	require.NoError(t, err)
	assert.JSONEq(t, `{"value":0}`, string(denied))

	copy := cloneToolCall(call)
	copy.Registry.Resolution[0] = '!'
	_, _, err = lookupCallSpec(call, rt.toolSpec)
	require.NoError(t, err)
	_, _, err = lookupCallSpec(copy, rt.toolSpec)
	require.Error(t, err)
}

func testRegistryAgentDefinition(sources RegistryTools) AgentDefinition {
	return NewAgentDefinition(AgentRoute{
		ID: agent.Ident("records.agent"), WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue",
	}, nil, nil, nil, nil, nil, nil).WithRegistryTools(sources)
}

func testRuntimeRegistryResolution(name, token string) *genregistry.ResolvedToolset {
	schema := []byte(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)
	version := genregistry.SemVer("1.0.0")
	return &genregistry.ResolvedToolset{
		RegistrationToken: token,
		Toolset: &genregistry.Toolset{
			Name: "provider.records", Version: &version, RegisteredAt: "2026-09-17T00:00:00Z",
			Tools: []*genregistry.ToolSchema{{
				Name: name, PayloadSchema: schema, ExecutionPayloadSchema: schema, ResultSchema: schema,
				ConsumerContract: &genregistry.ConsumerContract{
					Kind: "service", Title: "Find records",
					Search:  &genregistry.ToolSearchDocument{Length: 2, Terms: map[string]int{"find": 1, "records": 1}},
					Payload: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: schema},
					Result:  &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: schema},
					Confirmation: &genregistry.ToolConfirmation{
						PromptTemplate: "Change value {{ json .value }}?", DeniedResultTemplate: `{"value":0}`,
					},
				},
			}},
		},
	}
}

// scopedRegistrySources is application logic: namespace labels choose which
// registered toolset this activity may advertise.
type scopedRegistrySources struct{}

func (scopedRegistrySources) Resolve(ctx context.Context, catalog *RegistryCatalog) error {
	labels := catalog.RunLabels()
	namespace := labels["namespace"]
	labels["namespace"] = "local-copy"
	return catalog.IncludeToolset(ctx, "company", namespace, "", false)
}

func (scopedRegistrySources) Allows(registry, toolset, _ string) bool {
	return registry == "company" && (toolset == "support" || toolset == "operations")
}

func TestRegistryCatalogUsesRunScopeWithoutSharingLabels(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(scopedRegistrySources{})
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	client := &genregistry.Client{ResolveToolsetEndpoint: func(_ context.Context, value any) (any, error) {
		payload := value.(*genregistry.GetToolsetPayload)
		result := testRuntimeRegistryResolution(payload.Name+".find", strings.Repeat("a", 64))
		result.Toolset.Name = payload.Name
		return result, nil
	}}
	require.NoError(t, rt.RegisterRegistry("company", client, unusedRegistryPulse{}))
	for _, namespace := range []string{"support", "operations"} {
		t.Run(namespace, func(t *testing.T) {
			t.Parallel()
			labels := map[string]string{"namespace": namespace}
			catalog, err := rt.planningCatalog(t.Context(), &PlanActivityInput{
				AgentID: definition.route.ID, RunContext: run.Context{Labels: labels},
			})
			require.NoError(t, err)
			assert.Len(t, catalog.selections, 1)
			assert.Contains(t, catalog.selections, tools.Ident(namespace+".find"))
			assert.Equal(t, namespace, labels["namespace"])
		})
	}
}
