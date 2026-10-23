// These tests offer a new page after a selected operation description changes.
// The old owner declaration proves saved identity; current input and policy
// govern the new call. No provider, model, network or application data is used.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
)

type priorPageActorKey struct{}

func TestNewStaticPageUsesPriorDeclarationAndCurrentInputs(t *testing.T) {
	for _, kind := range []string{"service", "agent"} {
		for _, scenario := range []string{"description change", "closed chain", "missing current pair", "incompatible input", "owner denied", "wrong token", "wrong target", "independent roots"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				old := testRegistryPagingResolution(strings.Repeat("a", 64))
				current := testRegistryPagingResolution(strings.Repeat("b", 64))
				old.Toolset.Name = "private.records.old"
				current.Toolset.Name = "private.records.current"
				current.Toolset.Tools[0].Description = new("Read records with a clearer explanation.")
				var children []AgentDefinition
				if kind == "agent" {
					for _, selected := range []*genregistry.ResolvedToolset{old, current} {
						for _, declaration := range selected.Toolset.Tools {
							declaration.ConsumerContract.Kind = "agent"
							declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "generic.agent", Workflow: "generic.workflow", Queue: "generic.queue", Configuration: selected.RegistrationToken}
						}
					}
					children = []AgentDefinition{testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)}
				}
				saved, err := registrycontract.ResolveCompiled(old)
				require.NoError(t, err)
				binding, err := saved.Select(testRegistryName, "records.find")
				require.NoError(t, err)
				resolved, err := registrycontract.Resolve(current)
				require.NoError(t, err)
				specs := []tools.ToolSpec{resolved.Specs["records.find"], resolved.Specs["records.continue"]}
				expected := make([]*genregistry.ToolSchema, len(current.Toolset.Tools))
				for index, declaration := range current.Toolset.Tools {
					copy := *declaration
					contract := *declaration.ConsumerContract
					contract.Agent = nil
					copy.ConsumerContract = &contract
					expected[index] = &copy
				}
				definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, specs, nil, nil, []tools.Ident{"records.find", "records.continue"}, children, nil).WithRegistryTools(testRegistrySources{whole: true}).WithRegistryDeclarations(testDeclarations(expected))
				rt := New(newTestStore())
				rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
				rt.registryDeclarations = definition.declarations
				require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
				catalog := rt.newRegistryCatalog(definition)
				reads := 0
				ctx := context.WithValue(t.Context(), priorPageActorKey{}, "actor-one")
				read := func(ctx context.Context, route, token string) (*genregistry.ResolvedToolset, error) {
					reads++
					assert.Equal(t, "actor-one", ctx.Value(priorPageActorKey{}))
					assert.Equal(t, old.Toolset.Name, route)
					assert.Equal(t, old.RegistrationToken, token)
					if scenario == "owner denied" {
						return nil, errors.New("owner denied this resource")
					}
					if scenario == "wrong token" {
						copy := *old
						copy.RegistrationToken = strings.Repeat("f", 64)
						return &copy, nil
					}
					return old, nil
				}
				if kind == "agent" {
					require.NoError(t, catalog.BindStaticAgent(testRegistryName, expected, current, func(ctx context.Context, route, token string, target *genregistry.AgentToolTarget) (*genregistry.ResolvedToolset, error) {
						assert.Equal(t, old.Toolset.Tools[0].ConsumerContract.Agent, target)
						result, err := read(ctx, route, token)
						if scenario == "wrong target" {
							result = testRegistryPagingResolution(old.RegistrationToken)
							result.Toolset.Name = old.Toolset.Name
						}
						return result, err
					}, "records.find", "records.continue"))
				} else {
					require.NoError(t, catalog.BindStaticService(testRegistryName, current.Toolset, current, read, "records.find", "records.continue"))
				}
				output := sourceContinuationOutput("records.find", "root-one", `{"value":1}`, "same-cursor")
				output.Registry = binding
				outputs := []*planner.ToolOutput{output}
				if scenario == "independent roots" {
					second := *output
					second.ToolCallID = "root-two"
					outputs = append(outputs, &second)
				}
				if scenario == "closed chain" {
					output.Bounds.NextCursor = nil
					output.Bounds.Truncated = false
				}
				if scenario == "missing current pair" {
					delete(catalog.selections["records.find"].resolution.Specs, "records.continue")
				}
				if scenario == "incompatible input" {
					spec := catalog.specs["records.continue"]
					spec.ExecutionPayloadCodec.FromJSON = func([]byte) (any, error) { return nil, errors.New("old cursor input is no longer accepted") }
					catalog.specs[spec.Name] = spec
				}
				before, err := json.Marshal(outputs)
				require.NoError(t, err)
				input := &resolvedPlanActivityInput{PlanActivityInput: &PlanActivityInput{AgentID: definition.route.ID}}
				actions, err := catalog.continuationActions(ctx, input, outputs)
				if scenario == "owner denied" || scenario == "wrong token" || kind == "agent" && scenario == "wrong target" {
					require.Error(t, err)
					return
				}
				require.NoError(t, err)
				switch scenario {
				case "closed chain", "missing current pair":
					assert.Empty(t, actions)
					assert.Zero(t, reads)
				case "incompatible input":
					assert.Empty(t, actions)
				default:
					require.Len(t, actions, len(outputs))
					assert.Equal(t, 1, reads)
					for index, action := range actions {
						assert.Equal(t, outputs[index].ToolCallID, action.state.rootToolCallID)
						assert.JSONEq(t, `{"cursor":"same-cursor"}`, string(action.executablePayload))
						token, err := registrycontract.RegistrationToken(action.registry)
						require.NoError(t, err)
						assert.Equal(t, current.RegistrationToken, token)
					}
				}
				after, err := json.Marshal(outputs)
				require.NoError(t, err)
				assert.Equal(t, sha256.Sum256(before), sha256.Sum256(after))
			})
		}
	}
}

func TestPreviousSelectionsStayInsideTheirPlanningActivity(t *testing.T) {
	old := testRegistryPagingResolution(strings.Repeat("a", 64))
	current := testRegistryPagingResolution(strings.Repeat("b", 64))
	current.Toolset.Tools[0].Description = new("Read the current records.")
	saved, err := registrycontract.ResolveCompiled(old)
	require.NoError(t, err)
	binding, err := saved.Select(testRegistryName, "records.find")
	require.NoError(t, err)
	resolved, err := registrycontract.Resolve(current)
	require.NoError(t, err)
	specs := []tools.ToolSpec{resolved.Specs["records.find"], resolved.Specs["records.continue"]}
	definition := testAgentDefinition("records.agent", "records.workflow", "records.queue", specs, nil).WithRegistryTools(testRegistrySources{whole: true}).WithRegistryDeclarations(testDeclarations(current.Toolset.Tools))
	rt := New(newTestStore())
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	var reads atomic.Int64
	var group sync.WaitGroup
	for _, actor := range []string{"first actor", "second actor"} {
		catalog := rt.newRegistryCatalog(definition)
		require.NoError(t, catalog.BindStaticService(testRegistryName, current.Toolset, current, func(ctx context.Context, route, token string) (*genregistry.ResolvedToolset, error) {
			assert.Equal(t, actor, ctx.Value(priorPageActorKey{}))
			assert.Equal(t, old.Toolset.Name, route)
			assert.Equal(t, old.RegistrationToken, token)
			reads.Add(1)
			return old, nil
		}, "records.find", "records.continue"))
		group.Go(func() {
			output := sourceContinuationOutput("records.find", actor, `{"value":1}`, "equal-cursor")
			output.Registry = binding
			ctx := context.WithValue(t.Context(), priorPageActorKey{}, actor)
			actions, err := catalog.continuationActions(ctx, &resolvedPlanActivityInput{PlanActivityInput: &PlanActivityInput{AgentID: definition.route.ID}}, []*planner.ToolOutput{output})
			if err != nil {
				t.Errorf("plan next page: %v", err)
				return
			}
			if assert.Len(t, actions, 1) {
				assert.Equal(t, actor, actions[0].state.rootToolCallID)
			}
		})
	}
	group.Wait()
	assert.Equal(t, int64(2), reads.Load(), "equal saved selections in different activities each require their own owner authorization")
}
