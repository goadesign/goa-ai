// Registered imports keep distinct consumer names and unchanged provider operations
// through planning, saved bindings, and concurrent catalog reads.
package runtime

import (
	"context"
	"errors"
	"goa.design/goa-ai/internal/registrycall"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type registeredTestSource struct {
	resources []*genregistry.RegisteredToolsetSelection
}

func (s registeredTestSource) Resolve(_ context.Context, catalog *RegistryCatalog) error {
	for _, resource := range s.resources {
		if err := catalog.IncludeRegistered(testRegistryName, resource.Source, resource.Resolved, true); err != nil {
			return err
		}
	}
	return nil
}

func (s registeredTestSource) Allows(registry, name, version string) bool {
	return registry == testRegistryName && name == "provider.records" && version == "1.0.0"
}

func TestRegisteredCatalogSharesOriginalWithoutChangingIdentity(t *testing.T) {
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	first := runtimeTestResource("first-owner")
	firstName := registeredTestName(t, first, "records.find")
	second := runtimeTestResource("second-owner")
	secondName := registeredTestName(t, second, "records.find")
	second.Resolved = first.Resolved
	definition := testRegistryAgentDefinition(registeredTestSource{[]*genregistry.RegisteredToolsetSelection{first, second}})
	catalog, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	assert.Len(t, catalog.specs, 2)
	assert.Equal(t, "records.find", first.Resolved.Toolset.Tools[0].Name)
	assert.Equal(t, []string{"tenant"}, catalog.labels[firstName])
	binding, err := catalog.selections[firstName].resolution.Select(testRegistryName, firstName)
	require.NoError(t, err)
	call := ToolCall{Name: firstName, Registry: binding}
	metadata, err := registryCallMetadata(call, noStaticTools{})
	require.NoError(t, err)
	assert.Equal(t, first.Resolved.Toolset.Tools[0].ConsumerContract.Title, metadata.Title)
	assert.Equal(t, firstName, metadata.ID)
	retained, err := registrycontract.Read(binding, nil)
	require.NoError(t, err)
	declaration, err := retained.Declaration(call.Name)
	require.NoError(t, err)
	assert.Equal(t, "records.find", declaration.Name)

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			own, err := rt.resolveRegistryCatalog(t.Context(), definition)
			assert.NoError(t, err) //nolint:testifylint // The result check below runs only on success; this concurrent callback must not stop the test goroutine.
			if err == nil {
				assert.Equal(t, secondName, own.specs[secondName].Name)
			}
		})
	}
	wg.Wait()
}

func TestRegisteredCatalogRejectsNameCollisions(t *testing.T) {
	for _, static := range []bool{false, true} {
		rt := New(newTestStore())
		require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
		resource := runtimeTestResource("owner")
		name := registeredTestName(t, resource, "records.find")
		catalog := rt.newRegistryCatalog(testRegistryAgentDefinition(registeredTestSource{}))
		if static {
			catalog.specs[name] = tools.ToolSpec{Name: name}
		} else {
			require.NoError(t, catalog.IncludeRegistered(testRegistryName, resource.Source, resource.Resolved, true))
		}
		require.ErrorContains(t, catalog.IncludeRegistered(testRegistryName, resource.Source, resource.Resolved, true), "already supplied")
	}
}

func TestRegisteredNativeDeclarationKeepsProviderOperation(t *testing.T) {
	resource := runtimeTestResource("owner")
	declaration := resource.Resolved.Toolset.Tools[0]
	declaration.ConsumerContract.Kind = registryAgentKind
	declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "records.agent", Configuration: "revision/7", Workflow: "records.workflow", Queue: "records.queue"}
	resource.Source.Target = genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: resource.Resolved.Toolset.Name})
	name := registeredTestName(t, resource, "records.find")
	resolved, err := registrycontract.ResolveRegistered(testRegistryName, resource.Source, resource.Resolved)
	require.NoError(t, err)
	binding, err := resolved.Select(testRegistryName, name)
	require.NoError(t, err)
	original, err := registryAgentDeclaration(ToolCall{Name: name, Registry: binding}, noStaticTools{})
	require.NoError(t, err)
	assert.Equal(t, "records.find", original.Name)
	assert.Equal(t, "records.agent", original.ConsumerContract.Agent.Executor)
	parentTool, err := selectedParentTool(name, binding, "records.agent", noStaticTools{})
	require.NoError(t, err)
	assert.Equal(t, tools.Ident("records.find"), parentTool.Name)
}

func runtimeTestResource(owner string) *genregistry.RegisteredToolsetSelection {
	resolved := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	resolved.Toolset.Tools[0].ConsumerContract.RequiredLabels = []string{"tenant"}
	return &genregistry.RegisteredToolsetSelection{Resolved: resolved,
		Source: &genregistry.CatalogSource{Scope: owner, Name: "records", Target: genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: resolved.Toolset.Name, RegistrationToken: resolved.RegistrationToken})},
	}
}

// registeredTestName finds the generated identity through the retained original
// declaration, so integration tests select an operation without inventing an alias.
func registeredTestName(t *testing.T, resource *genregistry.RegisteredToolsetSelection, original string) tools.Ident {
	t.Helper()
	resolved, err := registrycontract.ResolveRegistered(testRegistryName, resource.Source, resource.Resolved)
	require.NoError(t, err)
	for name := range resolved.Specs {
		declaration, err := resolved.Declaration(name)
		require.NoError(t, err)
		if declaration.Name == original {
			return name
		}
	}
	t.Fatalf("resource has no original operation %q", original)
	return ""
}

func TestRegisteredPlanningRetainsDerivedNameAndOriginalBinding(t *testing.T) {
	rt := New(newTestStore())
	resource := runtimeTestResource("owner")
	name := registeredTestName(t, resource, "records.find")
	definition := testRegistryAgentDefinition(registeredTestSource{[]*genregistry.RegisteredToolsetSelection{resource}})
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		require.Len(t, input.Agent.AdvertisedToolDefinitions(), 1)
		assert.Equal(t, string(name), input.Agent.AdvertisedToolDefinitions()[0].Name)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: name, Payload: rawjson.Message(`{"value":7}`)}}}, nil
	}}}
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: "registered-run", RunContext: run.Context{RunID: "registered-run", Labels: map[string]string{"tenant": "one"}}}, nil)
	output, err := rt.PlanStartActivity(t.Context(), input)
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.Len(t, output.Result.ToolCalls, 1)
	call := output.Result.ToolCalls[0]
	assert.Equal(t, name, call.Name)
	resolved, err := registrycontract.Read(call.Registry, nil)
	require.NoError(t, err)
	declaration, err := resolved.Declaration(call.Name)
	require.NoError(t, err)
	assert.Equal(t, "records.find", declaration.Name)
}

func TestRegisteredServiceDispatchUsesOriginalOperation(t *testing.T) {
	called := false
	client := registrycall.NewResolvedClient(&genregistry.Client{CallResolvedToolEndpoint: func(_ context.Context, input any) (any, error) {
		called = true
		assert.Equal(t, "records.find", input.(*genregistry.CallResolvedToolPayload).Tool)
		return nil, errors.New("stop before provider")
	}}, strings.Repeat("a", 64), "records.find")
	_, err := client.CallTool(t.Context(), &genregistry.CallToolPayload{Toolset: "provider.records", Tool: "selected-model-name"})
	require.ErrorContains(t, err, "stop before provider")
	assert.True(t, called)
}

func TestCatalogExecutionInvocationUsesOwningRun(t *testing.T) {
	rt := New(newTestStore())
	definition := testRegistryAgentDefinition(registeredTestSource{})
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: "owner-run", RunContext: run.Context{RunID: "owner-run", ParentRunID: "unrelated-current-parent", ParentToolCallID: "not-the-origin"}}, nil)
	catalog := rt.newRegistryCatalog(definition)
	catalog.runID = input.RunContext.RunID
	invocation, err := catalog.ExecutionInvocation(t.Context())
	require.NoError(t, err)
	root, ok := invocation.Invocation.AsRoot()
	require.True(t, ok)
	assert.Equal(t, "owner-run", root.RunID)
	catalog.runID = "missing-run"
	_, err = catalog.ExecutionInvocation(t.Context())
	require.Error(t, err)
}
