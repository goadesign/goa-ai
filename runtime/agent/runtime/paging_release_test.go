package runtime

// These tests resume real saved workflows across three provider selections.
// Each new page uses the current provider, while prior calls and results remain
// unchanged and the original query owns the whole ordered page sequence.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/api"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type (
	pagingReleaseSources struct {
		selected *genregistry.ResolvedToolset
		verify   func(context.Context, string, string) error
	}
)

func TestPausedPagingSelectsCurrentProviderAcrossReleases(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	store := newTestStore()
	calls := make(map[string]int)
	var root string
	newRuntime := func(version string) *Runtime {
		rt := New(store, WithEngine(engineinmem.New()))
		selected := pagingReleaseRegistration(version)
		resolved, err := registrycontract.Resolve(selected)
		require.NoError(t, err)
		specs := []tools.ToolSpec{resolved.Specs["records.find"], resolved.Specs["records.continue"]}
		sources := pagingReleaseSources{selected: selected, verify: func(_ context.Context, route, token string) error {
			for _, previous := range []string{"a", "b", "c"} {
				if route == "private.records."+previous && token == strings.Repeat(previous, 64) {
					return nil
				}
			}
			return errors.New("registration belongs to another resource")
		}}
		definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "test"}, specs, nil, nil, []tools.Ident{"records.find", "records.continue"}, nil, nil).
			WithRegistryTools(sources).WithRegistryDeclarations(testDeclarations(selected.Toolset.Tools))
		require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
		require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: specs, Execute: func(_ context.Context, call *ToolCall) (*ToolExecutionResult, error) {
			token, route, err := registrycontract.ServiceRoute(call.Registry, call.Name)
			require.NoError(t, err)
			require.Equal(t, strings.Repeat(version, 64), token)
			require.Equal(t, "private.records."+version, route)
			calls[version]++
			if version == "a" {
				root = call.ToolCallID
				require.Equal(t, rawjson.Message(`{"value":1}`), call.Payload)
			} else {
				previous := map[string]string{"b": "a", "c": "b"}[version]
				require.JSONEq(t, fmt.Sprintf(`{"cursor":"cursor-%s"}`, previous), string(call.Payload))
			}
			result := &planner.ToolResult{Name: call.Name, ToolCallID: call.ToolCallID, Result: map[string]any{"value": 1}, Bounds: &agent.Bounds{Returned: 1}}
			if version == "c" {
				return &ToolExecutionResult{ToolResult: result}, nil
			}
			result.Bounds.Truncated, result.Bounds.NextCursor = true, new("cursor-"+version)
			return &ToolExecutionResult{ToolResult: result, Clarification: &ToolClarification{ID: version, Question: "Which group?"}}, nil
		}}))
		var requested model.ToolCall
		require.NoError(t, rt.RegisterModel("local", mustTestModelClient(stubModelClient{complete: func(context.Context, *model.Request) (*model.Response, error) {
			return testModelResponse(nil, requested), nil
		}})))
		require.NoError(t, rt.RegisterAgent(ctx, AgentRegistration{Definition: definition, WorkflowHandler: rt.ExecuteWorkflow,
			PlanActivityName: "records.plan", ResumeActivityName: "records.resume", ExecuteToolActivity: "records.execute",
			Planner: &stubPlanner{
				start: func(context.Context, *planner.PlanInput) (*planner.PlanResult, error) {
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: "records.find", Payload: rawjson.Message(`{"value":1}`)}}}, nil
				},
				resume: func(ctx context.Context, input *planner.PlanResumeInput) (*planner.PlanResult, error) {
					actions := historicalActions(input.Agent)
					if len(actions) == 0 {
						require.Equal(t, "c", version)
						require.Len(t, input.ToolOutputs, 3)
						return finalPlannerResult("All pages received."), nil
					}
					require.Len(t, actions, 1)
					requested = model.ToolCall{ID: "page", Name: actions[0], Payload: rawjson.Message(`{}`)}
					client, ok := input.Agent.PlannerModelClient("local")
					require.True(t, ok)
					response, err := client.Complete(ctx, &model.Request{Model: "local", Tools: input.Agent.AdvertisedToolDefinitions()})
					if err != nil {
						return nil, err
					}
					call, err := planner.ToolRequestFromModelCall(response.ToolCalls()[0])
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{call}}, err
				},
			}}))
		return rt
	}
	old := newRuntime("a")
	_, err := createSessionForTest(ctx, store, "paging-release")
	require.NoError(t, err)
	first, err := old.MustClient("records.agent").Run(ctx, "paging-release", nil, WithRunID("a"), WithTurnID("a"))
	require.NoError(t, err)
	require.NotNil(t, first.Suspension)
	before, err := json.Marshal(storedToolResults(t, old, "a"))
	require.NoError(t, err)
	previous := "a"
	for _, version := range []string{"b", "c"} {
		current := newRuntime(version)
		prepared, err := current.MustClient("records.agent").PrepareContinuation(ctx, "paging-release", previous, version, version,
			&api.PendingInputResponse{Clarification: &api.ClarificationAnswer{ID: previous, Answer: "selected"}}, WorkflowOptions{})
		require.NoError(t, err)
		handle, err := current.MustClient("records.agent").StartPrepared(ctx, prepared)
		require.NoError(t, err)
		out, err := handle.Wait(ctx)
		require.NoError(t, err)
		if version == "b" {
			require.NotNil(t, out.Suspension)
			checkpoint, err := decodeWorkflowCheckpointState(out.Suspension)
			require.NoError(t, err)
			require.Len(t, checkpoint.State.ToolOutputs, 2)
			assert.Equal(t, root, checkpoint.State.ToolOutputs[1].ContinuationRootToolCallID)
			chains, _, err := historicalPagingChains(checkpoint.State.ToolOutputs)
			require.NoError(t, err)
			assert.Len(t, chains, 1)
		} else {
			require.Nil(t, out.Suspension)
		}
		previous = version
	}
	after, err := json.Marshal(storedToolResults(t, old, "a"))
	require.NoError(t, err)
	assert.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
	assert.Equal(t, map[string]int{"a": 1, "b": 1, "c": 1}, calls)
}

func (s pagingReleaseSources) Resolve(_ context.Context, catalog *RegistryCatalog) error {
	return catalog.BindStaticService(testRegistryName, s.selected.Toolset, s.selected, s.verify, "records.find", "records.continue")
}

func (pagingReleaseSources) Allows(registry, _, _ string) bool {
	return registry == testRegistryName
}

func pagingReleaseRegistration(version string) *genregistry.ResolvedToolset {
	selected := testRegistryPagingResolution(strings.Repeat(version, 64))
	selected.Toolset.Name = "private.records." + version
	for _, declaration := range selected.Toolset.Tools {
		declaration.ResultSchema = []byte(`{"type":"object","properties":{"value":{"type":"integer"},"returned":{"type":"integer"},"truncated":{"type":"boolean"}},"required":["value","returned","truncated"],"additionalProperties":false}`)
		declaration.ConsumerContract.Result.SchemaWithoutRootExample = declaration.ResultSchema
	}
	return selected
}

func TestPagingOwnerVerificationAndIndependentRoots(t *testing.T) {
	for _, scenario := range []string{"current", "owner failure", "missing selection", "incompatible arguments", "missing verifier"} {
		t.Run(scenario, func(t *testing.T) {
			rt := New(newTestStore())
			old := pagingReleaseRegistration("a")
			resolved, err := registrycontract.ResolveCompiled(old)
			require.NoError(t, err)
			binding, err := resolved.Select(testRegistryName, "records.find")
			require.NoError(t, err)
			current := pagingReleaseRegistration("b")
			owned, err := registrycontract.Resolve(current)
			require.NoError(t, err)
			specs := []tools.ToolSpec{owned.Specs["records.find"], owned.Specs["records.continue"]}
			definition := testAgentDefinition("records.agent", "records.workflow", "test", specs, nil).
				WithRegistryTools(pagingReleaseSources{}).WithRegistryDeclarations(testDeclarations(current.Toolset.Tools))
			rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
			require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
			catalog := rt.newRegistryCatalog(definition)
			ownerFailure := errors.New("owner read unavailable")
			verified := 0
			verify := func(ctx context.Context, route, token string) error {
				assert.Equal(t, t.Context(), ctx)
				assert.Equal(t, old.Toolset.Name, route)
				assert.Equal(t, old.RegistrationToken, token)
				verified++
				if scenario == "owner failure" {
					return ownerFailure
				}
				return nil
			}
			if scenario == "missing verifier" {
				verify = nil
			}
			err = catalog.BindStaticService(testRegistryName, current.Toolset, current, verify, "records.find", "records.continue")
			if scenario == "missing verifier" {
				require.ErrorContains(t, err, "verification")
				assert.Empty(t, catalog.selections)
				return
			}
			require.NoError(t, err)
			outputs := []*planner.ToolOutput{
				sourceContinuationOutput("records.find", "root-one", `{"value":1}`, "same-cursor"),
				sourceContinuationOutput("records.find", "root-two", `{"value":1}`, "same-cursor"),
			}
			for _, output := range outputs {
				output.Registry = binding
			}
			before, err := json.Marshal(outputs)
			require.NoError(t, err)
			actions, err := rt.availableContinuationActions(definition.route.ID, outputs, false)
			require.NoError(t, err)
			require.Len(t, actions, 2)
			if scenario == "missing selection" {
				delete(catalog.selections, "records.find")
			}
			if scenario == "incompatible arguments" {
				spec := catalog.specs["records.continue"]
				spec.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) { return nil, errors.New("cursor no longer accepted") }
				catalog.specs[spec.Name] = spec
			}
			selected, err := catalog.bindContinuationActions(t.Context(), actions)
			if scenario == "owner failure" {
				require.ErrorIs(t, err, ownerFailure)
				assert.Empty(t, selected)
			} else {
				require.NoError(t, err)
				if scenario != "current" {
					assert.Empty(t, selected)
				} else {
					require.Len(t, selected, 2)
					assert.NotEqual(t, selected[0].modelName, selected[1].modelName)
					assert.NotEqual(t, selected[0].state.rootToolCallID, selected[1].state.rootToolCallID)
					for _, action := range selected {
						token, route, err := registrycontract.ServiceRoute(action.registry, action.spec.Name)
						require.NoError(t, err)
						assert.Equal(t, current.RegistrationToken, token)
						assert.Equal(t, current.Toolset.Name, route)
						assert.JSONEq(t, `{"cursor":"same-cursor"}`, string(action.executablePayload))
					}
					assert.Equal(t, 2, verified)
				}
			}
			after, err := json.Marshal(outputs)
			require.NoError(t, err)
			assert.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
			// An accepted call still supplies its own exact route after catalog
			// selection. Admission replay does not consult the current catalog.
			token, route, err := registrycontract.ServiceRoute(binding, "records.find")
			require.NoError(t, err)
			assert.Equal(t, old.RegistrationToken, token)
			assert.Equal(t, old.Toolset.Name, route)
		})
	}
}

func TestRegisteredPagingSelectsSamePermanentResource(t *testing.T) {
	for _, scenario := range []string{"same resource", "another owner", "another resource", "retained fields removed", "cursor field changed", "changed result"} {
		t.Run(scenario, func(t *testing.T) {
			rt := New(newTestStore())
			definition := testRegistryAgentDefinition(testRegistrySources{whole: true})
			rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
			require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
			old := pagingReleaseRegistration("a")
			if scenario == "retained fields removed" || scenario == "cursor field changed" {
				makePermissivePagingArguments(old)
			}
			source := &genregistry.CatalogSource{Scope: "owner-one", Name: "records", Target: genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: old.Toolset.Name, RegistrationToken: old.RegistrationToken})}
			resolved, err := registrycontract.ResolveRegistered(testRegistryName, source, old)
			require.NoError(t, err)
			var sourceName tools.Ident
			for name, spec := range resolved.Specs {
				if spec.Bounds.Paging.SourceTool == "" {
					sourceName = name
				}
			}
			binding, err := resolved.Select(testRegistryName, sourceName)
			require.NoError(t, err)
			output := sourceContinuationOutput(sourceName, "query", `{"value":1}`, "cursor")
			output.Registry = binding
			actions, err := rt.availableContinuationActions(definition.route.ID, []*planner.ToolOutput{output}, false)
			require.NoError(t, err)
			require.Len(t, actions, 1)
			current := pagingReleaseRegistration("b")
			if scenario == "retained fields removed" || scenario == "cursor field changed" {
				makePermissivePagingArguments(current)
				for _, declaration := range current.Toolset.Tools {
					if scenario == "retained fields removed" {
						declaration.ConsumerContract.Bounds.Paging.RetainedFields = []string{}
					} else {
						declaration.ConsumerContract.Bounds.Paging.CursorField = "another_cursor"
					}
				}
			}
			if scenario == "changed result" {
				for _, declaration := range current.Toolset.Tools {
					declaration.ResultSchema = []byte(`{"type":"object","properties":{"current_value":{"type":"string"}},"required":["current_value"]}`)
					declaration.ConsumerContract.Result.SchemaWithoutRootExample = declaration.ResultSchema
				}
			}
			source.Target = genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: current.Toolset.Name, RegistrationToken: current.RegistrationToken})
			if scenario == "another owner" {
				source.Scope = "owner-two"
			}
			if scenario == "another resource" {
				source.Name = "other-records"
			}
			catalog := rt.newRegistryCatalog(definition)
			require.NoError(t, catalog.IncludeRegistered(testRegistryName, source, current, false))
			selected, err := catalog.bindContinuationActions(t.Context(), actions)
			require.NoError(t, err)
			if scenario != "same resource" && scenario != "changed result" {
				assert.Empty(t, selected)
				return
			}
			require.Len(t, selected, 1)
			selection, err := registrycontract.Read(selected[0].registry, nil)
			require.NoError(t, err)
			assert.Equal(t, current.RegistrationToken, selection.Registered.RegistrationToken)
			assert.Equal(t, source.Scope, selection.Source.Scope)
			assert.Equal(t, source.Name, selection.Source.Name)
		})
	}
}

// makePermissivePagingArguments accepts both cursor fields so metadata alone
// determines whether the runtime would drop query values or change the cursor.
func makePermissivePagingArguments(selected *genregistry.ResolvedToolset) {
	for _, declaration := range selected.Toolset.Tools {
		declaration.ConsumerContract.Bounds.Paging.RetainedFields = []string{"value"}
		if declaration.Name == "records.continue" {
			declaration.ExecutionPayloadSchema = []byte(`{"type":"object","properties":{"value":{"type":"integer"},"cursor":{"type":"string"},"another_cursor":{"type":"string"}},"additionalProperties":false}`)
		}
	}
}

func TestRegisteredNativePagingSelectsCurrentRevision(t *testing.T) {
	for _, scenario := range []string{"same owner", "another owner", "another resource"} {
		t.Run(scenario, func(t *testing.T) {
			rt := New(newTestStore())
			definition := testRegistryAgentDefinition(testRegistrySources{whole: true})
			rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
			require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
			old := nativePagingReleaseRegistration("a")
			source := &genregistry.CatalogSource{Scope: "owner-one", Name: "records", Target: genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: old.Toolset.Name})}
			resolved, err := registrycontract.ResolveRegistered(testRegistryName, source, old)
			require.NoError(t, err)
			var sourceName tools.Ident
			for name, spec := range resolved.Specs {
				if spec.Bounds.Paging.SourceTool == "" {
					sourceName = name
				}
			}
			binding, err := resolved.Select(testRegistryName, sourceName)
			require.NoError(t, err)
			output := sourceContinuationOutput(sourceName, "query", `{"value":1}`, "cursor-a")
			output.Registry = binding
			actions, err := rt.availableContinuationActions(definition.route.ID, []*planner.ToolOutput{output}, false)
			require.NoError(t, err)
			require.Len(t, actions, 1)
			current := nativePagingReleaseRegistration("b")
			source.Target = genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: current.Toolset.Name})
			if scenario == "another owner" {
				source.Scope = "owner-two"
			}
			if scenario == "another resource" {
				source.Name = "other-records"
			}
			catalog := rt.newRegistryCatalog(definition)
			require.NoError(t, catalog.IncludeRegistered(testRegistryName, source, current, false))
			selected, err := catalog.bindContinuationActions(t.Context(), actions)
			require.NoError(t, err)
			if scenario != "same owner" {
				assert.Empty(t, selected)
				return
			}
			require.Len(t, selected, 1)
			bound, err := registrycontract.Read(selected[0].registry, nil)
			require.NoError(t, err)
			declaration, err := bound.Declaration(selected[0].spec.Name)
			require.NoError(t, err)
			assert.Equal(t, "revision/b", declaration.ConsumerContract.Agent.Configuration)
			assert.Equal(t, current.RegistrationToken, bound.Registered.RegistrationToken)
			assert.JSONEq(t, `{"cursor":"cursor-a"}`, string(selected[0].executablePayload))
			assert.Equal(t, binding, output.Registry)
		})
	}
}

// nativePagingReleaseRegistration supplies a complete synthetic native pair
// with a versioned configuration, preserving the operation and query contracts.
func nativePagingReleaseRegistration(version string) *genregistry.ResolvedToolset {
	selected := pagingReleaseRegistration(version)
	for _, declaration := range selected.Toolset.Tools {
		declaration.ConsumerContract.Kind = "agent"
		declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{
			Executor: "generic.agent", Workflow: "generic.workflow", Queue: "generic.queue", Configuration: "revision/" + version,
		}
	}
	return selected
}
