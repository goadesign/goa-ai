package runtime

// Static bindings retain complete validation contracts without adding planner
// choices. These tests enter initial and resumed planning activities and check
// both advertised tools and acceptance of the planner's returned calls.

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/api"
	"goa.design/goa-ai/runtime/agent/hooks"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	staticBindingPlannerSource struct {
		selected *genregistry.ResolvedToolset
		native   []*genregistry.ToolSchema
		names    []tools.Ident
		foreign  *genregistry.RegisteredToolsetSelection
	}
)

func TestStaticBindingPreservesExecutablePlannerTools(t *testing.T) {
	for _, kind := range []string{"service", "native"} {
		for _, phase := range []string{"start", "resume"} {
			for _, choice := range []string{"permitted", "omitted", "foreign"} {
				t.Run(kind+"/"+phase+"/"+choice, func(t *testing.T) {
					rt, definition, source := staticBindingPlannerRuntime(t, kind)
					foreignName := registeredTestName(t, source.foreign, string(genrecords.Read))
					chosen, payload := genrecords.Read, rawjson.Message(`{"query":"selected"}`)
					switch choice {
					case "omitted":
						chosen, payload = genrecords.Erase, rawjson.Message(`{}`)
					case "foreign":
						chosen = foreignName
					}
					plan := func(ctx planner.PlannerContext) *planner.PlanResult {
						assert.ElementsMatch(t, []tools.Ident{genrecords.Read, foreignName}, toolDefinitionNames(ctx.AdvertisedToolDefinitions()))
						return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: chosen, Payload: payload}}}
					}
					rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{
						start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
							return plan(input.Agent), nil
						},
						resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
							return plan(input.Agent), nil
						},
					}}
					input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: "binding-run", RunContext: run.Context{RunID: "binding-run"}}, nil)
					activity := rt.PlanStartActivity
					if phase == "resume" {
						activity = rt.PlanResumeActivity
					}
					output, err := activity(t.Context(), input)
					require.NoError(t, err)
					if choice == "omitted" {
						require.NotNil(t, output.OutputContractFailure)
						assert.Contains(t, output.OutputContractFailure.Reason, "outside its allowed catalog")
						assert.Nil(t, output.Result)
						return
					}
					require.Nil(t, output.OutputContractFailure)
					require.NotNil(t, output.Result)
					require.Len(t, output.Result.ToolCalls, 1)
					call := output.Result.ToolCalls[0]
					assert.Equal(t, chosen, call.Name)
					require.NotNil(t, call.Registry)
					retained, err := registrycontract.Read(call.Registry, definition.registryDeclaration)
					require.NoError(t, err)
					declaration, err := retained.Declaration(call.Name)
					require.NoError(t, err)
					assert.Equal(t, string(genrecords.Read), declaration.Name)
					if choice == "foreign" {
						require.NotNil(t, retained.Source)
						assert.Equal(t, source.foreign.Source.Scope, retained.Source.Scope)
					} else {
						assert.Nil(t, retained.Source)
						assert.Len(t, retained.Specs, 1)
					}
				})
			}
		}
	}
}

func TestStaticBindingCorrectCallPreservesExecutablePlannerTools(t *testing.T) {
	rt, definition, source := staticBindingPlannerRuntime(t, "service")
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: genrecords.Specs(), Execute: func(context.Context, *ToolCall) (*ToolExecutionResult, error) {
		t.Fatal("planning must not execute a corrected call")
		return nil, nil
	}}))
	foreignName := registeredTestName(t, source.foreign, string(genrecords.Read))
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{resume: func(_ context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
		assert.ElementsMatch(t, []tools.Ident{genrecords.Read, foreignName}, toolDefinitionNames(input.Agent.AdvertisedToolDefinitions()))
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: genrecords.Read, Payload: rawjson.Message(`{"query":"corrected"}`)}}}, nil
	}}}
	subject := genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent(genrunidentity.RunExecutorBranchAgent(definition.route.ID))}
	call := ToolCall{Name: genrecords.Read, ToolCallID: "failed", Payload: rawjson.Message(`{"query":"invalid"}`)}
	require.NoError(t, rt.publishHookErr(t.Context(), newToolCallScheduledEvent("recovery-run", subject, "session", call, "", "", 0), "turn"))
	require.NoError(t, rt.publishHookErr(t.Context(), hooks.NewToolResultReceivedEvent("recovery-run", subject, "session", "recovery-run", call.Name, call.ToolCallID, "", nil, nil, nil, "", nil, 0, nil, testToolFailure(planner.FailureInvalidCall, planner.RecoveryCorrectCall, "choose another query")), "turn"))
	input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: "recovery-run", RunContext: run.Context{RunID: "recovery-run", SessionID: "session"}, RecoveryToolCallIDs: []string{"failed"}, ToolOutputs: []*api.ToolOutputRef{{CallRunID: "recovery-run", ResultRunID: "recovery-run", ToolCallID: "failed"}}}, nil)
	output, err := rt.PlanResumeActivity(t.Context(), input)
	require.NoError(t, err)
	require.Nil(t, output.OutputContractFailure)
	require.NotNil(t, output.Result)
	require.Len(t, output.Result.ToolCalls, 1)
	assert.Equal(t, genrecords.Read, output.Result.ToolCalls[0].Name)
	assert.NotNil(t, output.Result.ToolCalls[0].Registry)
	require.NotNil(t, output.RecoveryCatalog)
	assert.ElementsMatch(t, []tools.Ident{genrecords.Read, foreignName}, output.RecoveryCatalog.Tools)
}

// staticBindingPlannerRuntime builds complete service or native declarations,
// selects only Read for planning, and imports a separate owner's Read operation.
// The imported operation must remain available under its distinct consumer name.
func staticBindingPlannerRuntime(t *testing.T, kind string) (*Runtime, AgentDefinition, staticBindingPlannerSource) {
	t.Helper()
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	specs := genrecords.Specs()
	selected := &genregistry.ResolvedToolset{Toolset: genrecords.Toolset(), RegistrationToken: strings.Repeat("a", 64)}
	selected.Toolset.RegisteredAt = "2026-09-17T00:00:00Z"
	source := staticBindingPlannerSource{selected: selected}
	var children []AgentDefinition
	declarations := genrecords.ToolSchema
	if kind == "native" {
		specs = []tools.ToolSpec{genrecords.SpecRead(), genrecords.SpecErase()}
		children = []AgentDefinition{testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)}
		var operations []*genregistry.ToolSchema
		for _, operation := range selected.Toolset.Tools {
			if operation.Name == string(genrecords.Show) {
				continue
			}
			operation.ConsumerContract.Kind = "agent"
			expected := *operation
			contract := *operation.ConsumerContract
			expected.ConsumerContract = &contract
			source.native = append(source.native, &expected)
			operation.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "generic.agent", Workflow: "generic.workflow", Queue: "generic.queue", Configuration: "revision/1"}
			operations = append(operations, operation)
		}
		selected.Toolset.Tools = operations
		declarations = testDeclarations(source.native)
		for index := range specs {
			specs[index].IsAgentTool = true
			specs[index].AgentID = "generic.agent"
		}
	}
	for _, spec := range specs {
		source.names = append(source.names, spec.Name)
	}
	foreign := genrecords.Toolset()
	foreign.Name = "provider.records"
	foreign.RegisteredAt = "2026-09-17T00:00:00Z"
	for _, operation := range foreign.Tools {
		if operation.Name == string(genrecords.Read) {
			foreign.Tools = []*genregistry.ToolSchema{operation}
			break
		}
	}
	source.foreign = &genregistry.RegisteredToolsetSelection{
		Resolved: &genregistry.ResolvedToolset{Toolset: foreign, RegistrationToken: strings.Repeat("b", 64)},
		Source:   &genregistry.CatalogSource{Scope: "other-owner", Name: "records", Target: genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: foreign.Name, RegistrationToken: strings.Repeat("b", 64)})},
	}
	definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, specs, nil, nil, []tools.Ident{genrecords.Read}, children, nil).WithRegistryTools(source).WithRegistryDeclarations(declarations)
	seedTestToolSpecs(rt, specs...)
	seedTestToolDefinitions(rt, specs...)
	rt.agentToolSpecs[definition.route.ID] = []tools.ToolSpec{definition.specByName[genrecords.Read]}
	return rt, definition, source
}

func (s staticBindingPlannerSource) Resolve(_ context.Context, catalog *RegistryCatalog) error {
	if len(s.native) > 0 {
		if err := catalog.BindStaticAgent(testRegistryName, s.native, s.selected, readTestStaticAgentResource, s.names...); err != nil {
			return err
		}
	} else if err := catalog.BindStaticService(testRegistryName, genrecords.Toolset(), s.selected, readTestStaticResource, s.names...); err != nil {
		return err
	}
	for _, name := range s.names {
		if _, bound := catalog.selections[name]; !bound {
			return fmt.Errorf("static binding omitted requested tool %q", name)
		}
	}
	return catalog.IncludeRegistered(testRegistryName, s.foreign.Source, s.foreign.Resolved, false)
}

func (staticBindingPlannerSource) Allows(registry, toolset, version string) bool {
	return registry == testRegistryName && (toolset == "records.records" || toolset == "provider.records") && version == ""
}
