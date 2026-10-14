// These tests keep application input attached to one selected source and prove
// static finalization prepares the same typed input without remote discovery.
package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

type (
	finalizerConsumerSources   struct{ registeredTestSource }
	consumerTwoRegistrySources struct{ registeredTestSource }
)

func (consumerTwoRegistrySources) Allows(registry, name, version string) bool {
	return (registry == testRegistryName || registry == "another-registry") && name == "provider.records" && version == "1.0.0"
}

func (finalizerConsumerSources) Resolve(context.Context, *RegistryCatalog) error {
	return fmt.Errorf("finalization must not discover sources")
}

func TestConsumerFinalizerPreparesStaticInput(t *testing.T) {
	rt := New(newTestStore())
	original, err := contract.Compile(consumerResource("unused.alias").Resolved.Toolset.Tools[0])
	require.NoError(t, err)
	original.TerminalRun = true
	original.Bookkeeping = true
	seedTestToolSpecs(rt, original)
	seedTestToolDefinitions(rt, original)
	definition := testAgentDefinition("records.agent", "records.workflow", "records.queue", []tools.ToolSpec{original}, nil).WithRegistryTools(finalizerConsumerSources{})
	preparations := 0
	definition = definition.WithInputBinding([]tools.Ident{original.Name}, func(_ context.Context, catalog *RegistryCatalog) error {
		preparations++
		assert.Equal(t, "finalizer-run", catalog.runID)
		return catalog.BindInput(original, consumerBoundSpec(t, original, consumerScopeRef{ID: "accepted"}))
	})
	rt.agentToolSpecs[definition.route.ID] = []tools.ToolSpec{original}
	modelInput := rawjson.Message(`{ "query": "done" }`)
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		visible := input.Agent.AdvertisedToolDefinitions()
		require.Len(t, visible, 1)
		assert.NotContains(t, string(visible[0].Input.Contract().Schema), `"scope"`)
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: original.Name, Payload: modelInput}}}, nil
	}}}
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: "finalizer-run", RunContext: run.Context{RunID: "finalizer-run", TextOnly: true}, Policy: &PolicyOverrides{TextOnly: true}, Finalize: &planner.Termination{Reason: planner.TerminationReasonToolFailure}}, nil)
	output, err := rt.PlanResumeActivity(t.Context(), input)
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.Len(t, output.Result.ToolCalls, 1)
	require.NoError(t, rt.normalizePlanResultForExecution(output.Result, run.Context{RunID: "finalizer-run", TextOnly: true}, definition.route.ID))
	assert.Equal(t, 1, preparations)
	assert.Equal(t, modelInput, output.Result.ToolCalls[0].ModelPayload)
	assert.JSONEq(t, `{"scope":{"id":"accepted"},"query":"done"}`, string(output.Result.ToolCalls[0].Payload))
}

func TestConsumerRegisteredBindingRequiresExactSource(t *testing.T) {
	for _, difference := range []string{"scope", "resource", "service token", "target kind", "registry"} {
		t.Run(difference, func(t *testing.T) {
			rt := New(newTestStore())
			require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
			expected := consumerResource("owner")
			foreign := consumerResource("owner")
			registry := testRegistryName
			switch difference {
			case "scope":
				foreign.Source.Scope = "another-owner"
			case "resource":
				foreign.Source.Name = "another-resource"
			case "service token":
				foreign.Resolved.RegistrationToken = strings.Repeat("c", 64)
				foreign.Source.Target = genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: foreign.Resolved.Toolset.Name, RegistrationToken: foreign.Resolved.RegistrationToken})
			case "target kind":
				foreign.Resolved.Toolset.Tools[0].ConsumerContract.Kind = registryAgentKind
				foreign.Resolved.Toolset.Tools[0].ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "records.agent", Configuration: "revision/7", Workflow: "records.workflow", Queue: "records.queue"}
				foreign.Source.Target = genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: foreign.Resolved.Toolset.Name})
			case "registry":
				registry = "another-registry"
				require.NoError(t, rt.RegisterRegistry(registry, &genregistry.Client{}, unusedRegistryPulse{}))
			}
			original, err := contract.Compile(expected.Resolved.Toolset.Tools[0])
			require.NoError(t, err)
			catalog := rt.newRegistryCatalog(testRegistryAgentDefinition(consumerTwoRegistrySources{}))
			require.NoError(t, catalog.IncludeRegistered(registry, foreign.Source, foreign.Resolved, true))
			require.ErrorContains(t, catalog.BindSourceInput(testRegistryName, expected.Source, original, consumerBoundSpec(t, original, consumerScopeRef{ID: "accepted"})), "not selected")
			for name, spec := range catalog.specs {
				assert.Nil(t, spec.PrepareInput)
				_, err = prepareConsumerInput(catalog.spec, name, rawjson.Message(`{"query":"active"}`))
				require.Error(t, err)
				_, err = prepareConsumerInput(catalog.spec, name, rawjson.Message(`{"scope":{"id":"chosen"},"query":"active"}`))
				require.NoError(t, err)
			}
			require.NoError(t, catalog.BindSourceInput(registry, foreign.Source, original, consumerBoundSpec(t, original, consumerScopeRef{ID: "accepted"})))
			for _, spec := range catalog.specs {
				assert.NotNil(t, spec.PrepareInput)
			}
		})
	}
}

func TestConsumerBindingRejectsOriginalModelSchemaDrift(t *testing.T) {
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	resource := consumerResource("catalog.find")
	original, err := contract.Compile(resource.Resolved.Toolset.Tools[0])
	require.NoError(t, err)
	for _, textOnly := range []bool{false, true} {
		catalog, err := rt.resolveRegistryCatalog(t.Context(), testRegistryAgentDefinition(registeredTestSource{[]*genregistry.RegisteredToolsetSelection{resource}}))
		require.NoError(t, err)
		changed := cloneToolSpec(original)
		if textOnly {
			changed.TextOnly.Payload.Schema = rawjson.Message(`{"type":"object"}`)
		} else {
			changed.Payload.Schema = rawjson.Message(`{"type":"object"}`)
		}
		require.ErrorContains(t, catalog.BindSourceInput(testRegistryName, resource.Source, changed, consumerBoundSpec(t, changed, consumerScopeRef{ID: "accepted"})), "provider schema")
		assert.Nil(t, catalog.specs[registeredTestName(t, resource, "records.find")].PrepareInput)
	}
}

func TestConsumerStaticBindingRejectsChangedExecutionContract(t *testing.T) {
	for _, mismatch := range []string{"agent", "kind", "schema"} {
		for _, boundary := range []string{"declaration", "provider"} {
			t.Run(mismatch+"/"+boundary, func(t *testing.T) {
				original, err := contract.Compile(consumerResource("owner").Resolved.Toolset.Tools[0])
				require.NoError(t, err)
				original.IsAgentTool, original.AgentID = true, "records.specialist"
				changed := cloneToolSpec(original)
				switch mismatch {
				case "agent":
					changed.AgentID = "records.other"
				case "kind":
					changed.IsAgentTool, changed.AgentID = false, ""
				case "schema":
					changed.Result.Schema = rawjson.Message(`{"type":"string"}`)
				}
				rt := New(newTestStore())
				seedTestToolSpecs(rt, original)
				seedTestToolDefinitions(rt, original)
				declared := original
				if boundary == "provider" {
					declared = changed
				}
				definition := testAgentDefinition("records.reader", "reader.workflow", "reader.queue", []tools.ToolSpec{declared}, nil)
				catalog := rt.newRegistryCatalog(definition)
				bound := consumerBoundSpec(t, changed, consumerScopeRef{ID: "accepted"})
				err = catalog.BindInput(changed, bound)
				want := "static declaration"
				if boundary == "provider" {
					want = "registered provider"
				}
				require.ErrorContains(t, err, want)
				assert.Nil(t, catalog.specs[original.Name].PrepareInput)
			})
		}
	}
}
