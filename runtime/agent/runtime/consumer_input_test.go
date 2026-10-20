// These tests exercise consumer-local input assembly and original resource
// retention. Codec generation is tested separately; the callback here is a typed
// synthetic application adapter using the same runtime contract.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/engine"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

type (
	consumerScopeRef struct {
		ID string `json:"id"`
	}
	consumerModelInput struct {
		Query string `json:"query"`
	}
	consumerExecutionInput struct {
		Scope consumerScopeRef `json:"scope"`
		Query string           `json:"query"`
	}
)

const (
	consumerFullSchema  = `{"type":"object","properties":{"scope":{"type":"object","properties":{"id":{"type":"string","minLength":1}},"required":["id"],"additionalProperties":false},"query":{"type":"string","minLength":1}},"required":["scope","query"],"additionalProperties":false}`
	consumerModelSchema = `{"type":"object","properties":{"query":{"type":"string","minLength":1}},"required":["query"],"additionalProperties":false}`
)

func TestConsumerActivityAcceptsCompleteInputs(t *testing.T) {
	for _, static := range []bool{false, true} {
		for _, textOnly := range []bool{false, true} {
			name := "registered"
			if static {
				name = "static"
			}
			if textOnly {
				name += "/text-only"
			}
			t.Run(name, func(t *testing.T) {
				rt := New(newTestStore())
				resource := consumerResource("catalog.search")
				original, err := contract.Compile(resource.Resolved.Toolset.Tools[0])
				require.NoError(t, err)
				sources := registeredTestSource{}
				bound := consumerBoundSpec(t, original, consumerScopeRef{ID: "accepted"})
				definition := testRegistryAgentDefinition(sources)
				calledName := registeredTestName(t, resource, "records.find")
				if static {
					seedTestToolSpecs(rt, original)
					seedTestToolDefinitions(rt, original)
					rt.toolsetNames[original.Name] = "provider.records"
					definition = testAgentDefinition("records.agent", "records.workflow", "records.queue", []tools.ToolSpec{original}, nil).WithRegistryTools(sources)
					calledName = original.Name
					rt.agentToolSpecs[definition.route.ID] = []tools.ToolSpec{original}
				} else {
					sources.resources = []*genregistry.RegisteredToolsetSelection{resource}
					definition = testRegistryAgentDefinition(sources)
					require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
				}
				var required []tools.Ident
				if static {
					required = []tools.Ident{original.Name}
				}
				definition = definition.WithInputBinding(required, func(_ context.Context, catalog *RegistryCatalog) error {
					if static {
						return catalog.BindInput(original, bound)
					}
					return catalog.BindSourceInput(testRegistryName, resource.Source, original, bound)
				})
				modelInput := rawjson.Message(`{ "query": "active" }`)
				rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
					visible := input.Agent.AdvertisedToolDefinitions()
					require.Len(t, visible, 1)
					assert.NotContains(t, string(visible[0].Input.Contract().Schema), `"scope"`)
					return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: calledName, Payload: modelInput}}}, nil
				}}}
				input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: "bound-run", RunContext: run.Context{RunID: "bound-run", TextOnly: textOnly}, Policy: &PolicyOverrides{TextOnly: textOnly}}, nil)
				output, err := rt.PlanStartActivity(t.Context(), input)
				require.NoError(t, err)
				require.Nil(t, output.OutputContractFailure)
				require.Len(t, output.Result.ToolCalls, 1)
				require.NoError(t, rt.normalizePlanResultForExecution(output.Result, run.Context{RunID: "bound-run", TextOnly: textOnly}, definition.route.ID))
				call := output.Result.ToolCalls[0]
				assert.Equal(t, modelInput, call.ModelPayload)
				assert.JSONEq(t, `{"scope":{"id":"accepted"},"query":"active"}`, string(call.Payload))
			})
		}
	}
}

func TestConsumerBindingIsolatesAliasesAndActivities(t *testing.T) {
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	first := consumerResource("first.search")
	second := consumerResource("second.lookup")
	second.Resolved = first.Resolved
	original, err := contract.Compile(first.Resolved.Toolset.Tools[0])
	require.NoError(t, err)
	providerHash := sha256.Sum256(first.Resolved.Toolset.Tools[0].PayloadSchema)
	definition := testRegistryAgentDefinition(registeredTestSource{[]*genregistry.RegisteredToolsetSelection{first, second}})
	for _, scope := range []string{"north", "south"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			catalog, err := rt.resolveRegistryCatalog(t.Context(), definition)
			require.NoError(t, err)
			bound := consumerBoundSpec(t, original, consumerScopeRef{ID: scope})
			require.NoError(t, catalog.BindSourceInput(testRegistryName, first.Source, original, bound))
			for _, resource := range []*genregistry.RegisteredToolsetSelection{first, second} {
				alias := registeredTestName(t, resource, "records.find")
				if resource == second {
					require.NoError(t, catalog.BindSourceInput(testRegistryName, second.Source, original, bound))
				}
				modelInput := rawjson.Message(`{ "query": "active" }`)
				complete, err := prepareConsumerInput(catalog, alias, modelInput)
				require.NoError(t, err)
				assert.JSONEq(t, `{"scope":{"id":"`+scope+`"},"query":"active"}`, string(complete))
				assert.Equal(t, []byte(`{ "query": "active" }`), []byte(modelInput)) //nolint:testifylint // Binding must preserve the original model input bytes, including spaces.
				_, err = original.Payload.Codec.FromJSON(complete)
				require.NoError(t, err)
				selected, err := catalog.selections[alias].resolution.Select(testRegistryName, alias)
				require.NoError(t, err)
				restored, err := registrycontract.Read(selected, nil)
				require.NoError(t, err)
				declaration, err := restored.Declaration(alias)
				require.NoError(t, err)
				assert.Equal(t, "records.find", declaration.Name)
				assert.Equal(t, providerHash, sha256.Sum256(declaration.PayloadSchema))
				_, err = prepareConsumerInput(catalog, alias, rawjson.Message(`{"query":"active","scope":{"id":"other"}}`))
				require.Error(t, err)
			}
			assert.Nil(t, original.PrepareInput)
			assert.Equal(t, providerHash, sha256.Sum256(first.Resolved.Toolset.Tools[0].PayloadSchema))
		})
	}
}

func TestConsumerBindingRejectsSchemaDriftAndInvalidOwnerInput(t *testing.T) {
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	resource := consumerResource("first.search")
	original, err := contract.Compile(resource.Resolved.Toolset.Tools[0])
	require.NoError(t, err)
	catalog, err := rt.resolveRegistryCatalog(t.Context(), testRegistryAgentDefinition(registeredTestSource{[]*genregistry.RegisteredToolsetSelection{resource}}))
	require.NoError(t, err)
	bound := consumerBoundSpec(t, original, consumerScopeRef{})
	require.NoError(t, catalog.BindSourceInput(testRegistryName, resource.Source, original, bound))
	_, err = prepareConsumerInput(catalog, registeredTestName(t, resource, "records.find"), rawjson.Message(`{"query":"active"}`))
	require.ErrorContains(t, err, "execution input")
	assert.True(t, engine.IsActivityErrorNonRetryable(err))
	var modelFailure *planner.OutputContractError
	assert.NotErrorAs(t, err, &modelFailure)
	_, err = prepareConsumerInput(catalog, registeredTestName(t, resource, "records.find"), rawjson.Message(`{"query":"active","scope":{"id":"model"}}`))
	require.ErrorAs(t, err, &modelFailure)
	require.ErrorContains(t, catalog.BindSourceInput(testRegistryName, resource.Source, original, bound), "already bound")
	changed := cloneToolSpec(original)
	changed.ExecutionPayloadSchema = rawjson.Message(`{"type":"object"}`)
	require.ErrorContains(t, catalog.BindSourceInput(testRegistryName, resource.Source, changed, bound), "retain its source")
	_, err = original.Payload.Codec.FromJSON(rawjson.Message(`{"scope":{"id":"direct"},"query":"active"}`))
	require.NoError(t, err)
	_, err = original.Payload.Codec.FromJSON(rawjson.Message(`{"query":"active"}`))
	require.Error(t, err)
}

func TestConsumerAwaitPreservesBothInputs(t *testing.T) {
	original, err := contract.Compile(consumerResource("unused.alias").Resolved.Toolset.Tools[0])
	require.NoError(t, err)
	bound := consumerBoundSpec(t, original, consumerScopeRef{ID: "north"})
	lookup := specLookupFunc(func(name tools.Ident) (tools.ToolSpec, bool) { return bound, name == bound.Name })
	modelInput := rawjson.Message(`{ "query": "active" }`)
	for _, item := range []planner.AwaitItem{
		planner.AwaitToolClarificationItem(&planner.AwaitToolClarification{ID: "clarify", ToolName: original.Name, ModelToolCallID: "model", Payload: modelInput, Question: "Continue?"}),
		planner.AwaitQuestionsItem(&planner.AwaitQuestions{ID: "questions", ToolName: original.Name, ModelToolCallID: "model", Payload: modelInput, Questions: []planner.AwaitQuestion{{ID: "q", Prompt: "Continue?"}}}),
		planner.AwaitExternalToolsItem(&planner.AwaitExternalTools{ID: "external", Items: []planner.AwaitToolItem{{Name: original.Name, ModelToolCallID: "model", Payload: modelInput}}}),
	} {
		t.Run(string(item.Kind), func(t *testing.T) {
			request := planner.NewAwait(item)
			require.NoError(t, validatePlannerToolCallIDs(&planner.PlanResult{Await: request}))
			prepared, err := prepareConsumerAwait(lookup, request)
			require.NoError(t, err)
			require.ErrorContains(t, validatePlannerToolCallIDs(&planner.PlanResult{Await: prepared}), "runtime model payload")
			compiled, err := compilePlannerAwaitForRun(run.Context{RunID: "run", TurnID: "turn", Attempt: 1}, 0, prepared)
			require.NoError(t, err)
			calls := awaitToolRequests(compiled.Items)
			require.Len(t, calls, 1)
			assert.Equal(t, modelInput, calls[0].ModelPayload)
			assert.JSONEq(t, `{"scope":{"id":"north"},"query":"active"}`, string(calls[0].Payload))
			visible := runtimePlanResultModelToolCalls(&PlanResult{Await: compiled})
			require.Len(t, visible, 1)
			assert.Equal(t, modelInput, visible[0].payload)
			assert.Equal(t, modelInput, planResultModelToolCalls(&planner.PlanResult{Await: request})[0].payload)
			calls[0].ModelPayload[0] = '!'
			assert.Equal(t, byte('{'), runtimePlanResultModelToolCalls(&PlanResult{Await: compiled})[0].payload[0])
		})
	}
}

func consumerResource(alias string) *genregistry.RegisteredToolsetSelection {
	resource := runtimeTestResource(alias)
	declaration := resource.Resolved.Toolset.Tools[0]
	declaration.PayloadSchema = []byte(consumerFullSchema)
	declaration.ExecutionPayloadSchema = []byte(consumerFullSchema)
	declaration.ConsumerContract.Payload.SchemaWithoutRootExample = []byte(consumerFullSchema)
	declaration.ConsumerContract.Confirmation = nil
	declaration.ConsumerContract.RequiredLabels = nil
	declaration.ConsumerContract.TextOnly = &genregistry.TextOnlyToolContract{
		Description:     "Read records.",
		Search:          &genregistry.ToolSearchDocument{Length: 1, Terms: map[string]int{"read": 1}},
		PayloadSchema:   []byte(consumerFullSchema),
		Payload:         &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: []byte(consumerFullSchema)},
		ExecutionSchema: []byte(consumerFullSchema),
	}
	return resource
}

func consumerBoundSpec(t *testing.T, original tools.ToolSpec, scope consumerScopeRef) tools.ToolSpec {
	t.Helper()
	declaration := consumerResource("unused.alias").Resolved.Toolset.Tools[0]
	declaration.PayloadSchema = []byte(consumerModelSchema)
	declaration.ConsumerContract.Payload.SchemaWithoutRootExample = []byte(consumerModelSchema)
	declaration.ConsumerContract.TextOnly.PayloadSchema = []byte(consumerModelSchema)
	declaration.ConsumerContract.TextOnly.Payload.SchemaWithoutRootExample = []byte(consumerModelSchema)
	modelSpec, err := contract.Compile(declaration)
	require.NoError(t, err)
	bound := cloneToolSpec(original)
	bound.Payload = modelSpec.Payload
	bound.TextOnly.Payload = modelSpec.TextOnly.Payload
	// The synthetic codec returns a typed value, as the generated consumer codec does.
	decode := bound.Payload.Codec.FromJSON
	typedDecode := func(input []byte) (any, error) {
		if _, err := decode(input); err != nil {
			return nil, err
		}
		var model consumerModelInput
		if err := json.Unmarshal(input, &model); err != nil {
			return nil, err
		}
		return &model, nil
	}
	bound.Payload.Codec.FromJSON = typedDecode
	bound.TextOnly.Payload.Codec.FromJSON = typedDecode
	bound.PrepareInput = func(input any) (rawjson.Message, error) {
		model, ok := input.(*consumerModelInput)
		if !ok {
			return nil, fmt.Errorf("unexpected model input %T", input)
		}
		return json.Marshal(consumerExecutionInput{Scope: scope, Query: model.Query})
	}
	bound.ExecutionPayloadSchema = slices.Clone(original.ExecutionPayloadSchema)
	return bound
}
