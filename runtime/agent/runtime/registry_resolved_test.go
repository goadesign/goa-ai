package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type resolvedTestSource struct {
	registered *genregistry.ResolvedToolset
	registry   string
	allow      bool
}

func (s resolvedTestSource) Resolve(_ context.Context, catalog *RegistryCatalog) error {
	return catalog.IncludeResolved(s.registry, s.registered, true)
}

func (s resolvedTestSource) Allows(registry, _, _ string) bool {
	return s.allow && registry == testRegistryName
}

func TestRegistryIncludeResolvedRetainsCompleteContractWithoutRemoteRead(t *testing.T) {
	rt := New(newTestStore())
	selected := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	selected.Toolset.Tools[0].ConsumerContract.RequiredLabels = []string{"tenant"}
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	definition := testRegistryAgentDefinition(resolvedTestSource{selected, testRegistryName, true})
	catalog, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	require.True(t, catalog.definitions["records.find"].Deferred)
	require.Equal(t, []string{"tenant"}, catalog.labels["records.find"])
	selection := catalog.selections["records.find"]
	binding, err := selection.resolution.Select(selection.registry, "records.find")
	require.NoError(t, err)
	retained, err := registrycontract.Read(binding)
	require.NoError(t, err)
	require.Equal(t, selected, retained.Registered)
	_, static := rt.toolSpec("records.find")
	require.False(t, static)

	// The existing planning boundary checks required labels and binds the
	// selected registration. A client with no read endpoints proves no re-read.
	calls := 0
	pl := &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		calls++
		require.Len(t, input.Agent.AdvertisedToolDefinitions(), 1)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{
			Name: "records.find", Payload: rawjson.Message(`{"value":7}`),
		}}}, nil
	}}
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: pl}
	input := PlanActivityInput{AgentID: definition.route.ID, RunID: "selected-run",
		RunContext: run.Context{RunID: "selected-run", Labels: map[string]string{"tenant": "one"}}}
	output, err := rt.PlanStartActivity(t.Context(), seedTestPlanInput(t, rt, input, nil))
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.Len(t, output.Result.ToolCalls, 1)
	require.Equal(t, binding, output.Result.ToolCalls[0].Registry)
	require.Equal(t, 1, calls)
}

func TestRegistryIncludeResolvedRejectsAtExistingContractBoundary(t *testing.T) {
	for _, name := range []string{"connection", "consumption", "invalid registration", "unsupported execution", "duplicate", "static collision", "native executor", "reserved name"} {
		t.Run(name, func(t *testing.T) {
			rt := New(newTestStore())
			selected := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
			source := resolvedTestSource{selected, testRegistryName, true}
			want := ""
			switch name {
			case "connection":
				source.registry, want = "unknown", "not registered"
			case "consumption":
				source.allow, want = false, "outside this agent"
			case "invalid registration":
				selected.RegistrationToken, want = "", "resolve registry"
			case "unsupported execution":
				selected.Toolset.Tools[0].ConsumerContract.Kind, want = "control", "requires compiled control execution"
			case "native executor":
				selected.Toolset.Tools[0].ConsumerContract.Kind = registryAgentKind
				selected.Toolset.Tools[0].ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "unconfigured.worker", Configuration: "revision/7"}
				want = "unconfigured Agent executor"
			case "reserved name":
				selected.Toolset.Tools[0].Name, want = string(continuationActionName("records.more", "selection-1")), "must be qualified"
			case "duplicate", "static collision":
				want = "already supplied"
			}
			require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
			catalog := rt.newRegistryCatalog(testRegistryAgentDefinition(source))
			if name == "duplicate" {
				require.NoError(t, source.Resolve(t.Context(), catalog))
			}
			if name == "static collision" {
				catalog.specs[tools.Ident("records.find")] = tools.ToolSpec{Name: "records.find"}
			}
			err := source.Resolve(t.Context(), catalog)
			require.ErrorContains(t, err, want)
		})
	}
}

func TestRegistryIncludeResolvedNativeUsesConfiguredTarget(t *testing.T) {
	rt := New(newTestStore())
	selected := testRuntimeRegistryResolution("specialist.inspect", strings.Repeat("b", 64))
	selected.Toolset.Tools[0].ConsumerContract.Kind = registryAgentKind
	selected.Toolset.Tools[0].ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "records.agent", Configuration: "revision/7"}
	source := resolvedTestSource{selected, testRegistryName, true}
	definition := testRegistryAgentDefinition(source)
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	catalog, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	spec := catalog.specs["specialist.inspect"]
	require.True(t, spec.IsAgentTool)
	require.Equal(t, "records.agent", spec.AgentID)
	binding, err := catalog.selections["specialist.inspect"].resolution.Select(testRegistryName, "specialist.inspect")
	require.NoError(t, err)
	retained, err := registrycontract.Read(binding)
	require.NoError(t, err)
	require.Equal(t, selected, retained.Registered)
}
