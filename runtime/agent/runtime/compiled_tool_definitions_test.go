// These tests follow synthetic registered and prepared tool contracts through
// model advertising. Repeated reads must reuse validators without sharing
// caller-owned output or another activity's bound input.
package runtime

import (
	"context"
	"errors"
	"fmt"
	goruntime "runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/run"
	"goa.design/goa-ai/runtime/agent/tools"
	"goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestCompiledDefinitionsKeepExactCodecsAndFirstRegistration(t *testing.T) {
	var ordinary, textOnly, replacement atomic.Int64
	spec := countedCompiledSpec(&ordinary, &textOnly)
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{spec}, Execute: unusedCompiledExecution}))
	assert.EqualValues(t, 1, ordinary.Load())
	assert.EqualValues(t, 1, textOnly.Load())
	original := rt.toolDefinitions[spec.Name]
	for _, text := range []bool{false, true} {
		for range 8 {
			definitions := rt.advertisedToolDefinitions([]tools.ToolSpec{spec}, compiledToolPolicy{textOnly: text})
			require.Len(t, definitions, 1)
			definitions[0].Name = "changed"
			definitions[0].Search.Terms["changed"] = 1
			raw := definitions[0].Input.Contract()
			raw.Schema[0] = '!'
		}
	}
	assert.EqualValues(t, 1, ordinary.Load())
	assert.EqualValues(t, 1, textOnly.Load())
	selected := rt.advertisedToolDefinitions([]tools.ToolSpec{spec}, compiledToolPolicy{textOnly: true})[0]
	assert.True(t, selected.AcceptsEmptyObject())
	assert.EqualValues(t, 1, ordinary.Load())
	assert.EqualValues(t, 2, textOnly.Load())
	duplicate := cloneToolSpec(spec)
	duplicate.TextOnly.Payload.Codec = countedCompiledCodec(&replacement)
	definitions, err := rt.validateToolSpecRegistrations(toolSpecRegistration{specs: []tools.ToolSpec{duplicate}})
	require.NoError(t, err)
	assert.Same(t, original.ordinary, definitions[spec.Name].ordinary)
	assert.Same(t, original.textOnly, definitions[spec.Name].textOnly)
	assert.EqualValues(t, 1, replacement.Load(), "a duplicate still validates its supplied text-only contract")
	selected = rt.advertisedToolDefinitions([]tools.ToolSpec{spec}, compiledToolPolicy{textOnly: true})[0]
	assert.True(t, selected.AcceptsEmptyObject())
	assert.EqualValues(t, 3, textOnly.Load())
	assert.EqualValues(t, 1, replacement.Load())
}

func TestCompiledDefinitionRegistrationFailurePublishesNeitherVariant(t *testing.T) {
	var firstOrdinary, firstText, secondOrdinary, secondText atomic.Int64
	first := countedCompiledSpec(&firstOrdinary, &firstText)
	first.Name = "records.first"
	second := countedCompiledSpec(&secondOrdinary, &secondText)
	second.Name = "records.second"
	second.TextOnly.Payload.Codec.FromJSON = rejectCompiledExample
	rt := New(newTestStore())
	err := rt.RegisterToolset(ToolsetRegistration{Name: "records", Specs: []tools.ToolSpec{first, second}, Execute: unusedCompiledExecution})
	require.ErrorContains(t, err, "text-only model contract")
	for _, spec := range []tools.ToolSpec{first, second} {
		_, exists := rt.toolDefinitions[spec.Name]
		assert.False(t, exists)
		_, exists = rt.toolSpec(spec.Name)
		assert.False(t, exists)
	}
}

func TestPreparedTextOnlyDefinitionsUseCurrentVisibility(t *testing.T) {
	for _, exclusion := range []string{"visible", "ordinary", "unavailable", "tags", "confirmation", "requires_ui", "unsupported", "continuation"} {
		t.Run(exclusion, func(t *testing.T) {
			var ordinary, text atomic.Int64
			spec := countedCompiledSpec(&ordinary, &text)
			spec.TextOnly.Payload.Schema = tools.RawJSON(`{"type":`)
			policy := compiledToolPolicy{textOnly: true}
			var unavailable []tools.Ident
			rt := New(newTestStore())
			switch exclusion {
			case "visible":
			case "ordinary":
				policy.textOnly = false
			case "unavailable":
				unavailable = []tools.Ident{spec.Name}
			case "tags":
				policy = compileToolPolicy(&PolicyOverrides{TextOnly: true, TagClauses: []TagPolicyClause{{AllowedAny: []string{"other"}}}})
			case "confirmation":
				rt.toolConfirmation = &ToolConfirmationConfig{Confirm: map[tools.Ident]*ToolConfirmation{spec.Name: {}}}
			case "requires_ui":
				spec.RequiresUI = true
			case "unsupported":
				spec.TextOnly = nil
			case "continuation":
				spec.Bounds = &tools.BoundsSpec{Paging: &tools.PagingSpec{ContinueTool: spec.Name, SourceTool: "records.source"}}
			}
			definition, err := model.NewToolDefinitionFromSpec(spec)
			require.NoError(t, err)
			catalog := &RegistryCatalog{runtime: rt, definitions: map[tools.Ident]compiledToolDefinition{spec.Name: {ordinary: definition}}}
			err = catalog.prepareModelDefinitions([]tools.ToolSpec{spec}, policy, unavailable)
			if exclusion == "visible" {
				require.ErrorContains(t, err, "text-only model input")
			} else {
				require.NoError(t, err)
			}
			assert.Nil(t, catalog.definitions[spec.Name].textOnly)
		})
	}
}

func TestPreparedOrdinaryContractStillValidated(t *testing.T) {
	for _, textOnly := range []bool{false, true} {
		t.Run(fmt.Sprintf("text_only=%t", textOnly), func(t *testing.T) {
			rt := New(newTestStore())
			original, err := contract.Compile(consumerResource("unused.alias").Resolved.Toolset.Tools[0])
			require.NoError(t, err)
			seedTestToolSpecs(rt, original)
			seedTestToolDefinitions(rt, original)
			definition := testAgentDefinition("records.agent", "records.workflow", "records.queue", []tools.ToolSpec{original}, nil)
			catalog := rt.newRegistryCatalog(definition)
			catalog.textOnly = textOnly
			before := catalog.definitions[original.Name]
			bound := consumerBoundSpec(t, original, consumerScopeRef{ID: "north"})
			bound.Payload.Schema = tools.RawJSON(`{"type":`)
			require.ErrorContains(t, catalog.BindInput(original, bound), "bound model input")
			assert.Same(t, before.ordinary, catalog.definitions[original.Name].ordinary)
			assert.Same(t, before.textOnly, catalog.definitions[original.Name].textOnly)
			assert.Nil(t, catalog.specs[original.Name].PrepareInput)
		})
	}
}

func TestPreparedDefinitionsKeepConcurrentActivityInputs(t *testing.T) {
	rt := New(newTestStore())
	original, err := contract.Compile(consumerResource("unused.alias").Resolved.Toolset.Tools[0])
	require.NoError(t, err)
	seedTestToolSpecs(rt, original)
	seedTestToolDefinitions(rt, original)
	var ordinary, text [2]atomic.Int64
	definition := testAgentDefinition("records.agent", "records.workflow", "records.queue", []tools.ToolSpec{original}, nil)
	definition = definition.WithInputBinding([]tools.Ident{original.Name}, func(_ context.Context, catalog *RegistryCatalog) error {
		scope := catalog.RunLabels()["scope"]
		index := 0
		if scope == "south" {
			index = 1
		}
		bound := consumerBoundSpec(t, original, consumerScopeRef{ID: scope})
		schema := tools.RawJSON(fmt.Sprintf(`{"type":"object","properties":{"query":{"type":"string","enum":[%q]}},"required":["query"],"additionalProperties":false}`, scope))
		for _, payload := range []*tools.TypeSpec{&bound.Payload, &bound.TextOnly.Payload} {
			payload.Schema = schema
			payload.SchemaWithoutRootExample = schema
			payload.ExampleJSON = tools.RawJSON(fmt.Sprintf(`{"query":%q}`, scope))
		}
		normalDecode := bound.Payload.Codec.FromJSON
		bound.Payload.Codec.FromJSON = func(raw []byte) (any, error) {
			ordinary[index].Add(1)
			return normalDecode(raw)
		}
		textDecode := bound.TextOnly.Payload.Codec.FromJSON
		bound.TextOnly.Payload.Codec.FromJSON = func(raw []byte) (any, error) {
			text[index].Add(1)
			return textDecode(raw)
		}
		return catalog.BindInput(original, bound)
	})
	rt.agentToolSpecs[definition.route.ID] = []tools.ToolSpec{original}
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition, Planner: &stubPlanner{start: func(_ context.Context, input *planner.PlanInput) (*planner.PlanResult, error) {
		scope := input.RunContext.Labels["scope"]
		index := 0
		if scope == "south" {
			index = 1
		}
		for range 8 {
			definitions := input.Agent.AdvertisedToolDefinitions()
			require.Len(t, definitions, 1)
			schema := string(definitions[0].Input.Contract().Schema)
			assert.Contains(t, schema, fmt.Sprintf(`"enum":[%q]`, scope))
			assert.NotContains(t, schema, `"scope"`)
			definitions[0].Search.Terms["changed"] = 1
		}
		assert.EqualValues(t, 1, ordinary[index].Load())
		assert.EqualValues(t, 1, text[index].Load())
		return &planner.PlanResult{ToolCalls: []planner.ToolRequest{{Name: original.Name, Payload: rawjson.Message(fmt.Sprintf(`{"query":%q}`, scope))}}}, nil
	}}}
	for _, scope := range []string{"north", "south"} {
		t.Run(scope, func(t *testing.T) {
			t.Parallel()
			id := "bound-" + scope
			input := seedTestPlanInput(t, rt, PlanActivityInput{AgentID: definition.route.ID, RunID: id, RunContext: run.Context{RunID: id, TextOnly: true, Labels: map[string]string{"scope": scope}}, Policy: &PolicyOverrides{TextOnly: true}}, nil)
			output, err := rt.PlanStartActivity(t.Context(), input)
			require.NoError(t, err)
			require.Nil(t, output.OutputContractFailure)
			require.Len(t, output.Result.ToolCalls, 1)
			assert.JSONEq(t, fmt.Sprintf(`{"scope":{"id":%q},"query":%q}`, scope, scope), string(output.Result.ToolCalls[0].Payload))
			assert.JSONEq(t, fmt.Sprintf(`{"query":%q}`, scope), string(output.Result.ToolCalls[0].ModelPayload))
		})
	}
}

func TestDynamicCompiledDefinitionsFollowCurrentSelection(t *testing.T) {
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	var previous *RegistryCatalog
	for index, choice := range []string{"north", "south"} {
		resource := consumerResource("records.search")
		resource.Resolved.RegistrationToken = strings.Repeat(string(rune('a'+index)), 64)
		resource.Source.Target = genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: resource.Resolved.Toolset.Name, RegistrationToken: resource.Resolved.RegistrationToken})
		schema := []byte(fmt.Sprintf(`{"type":"object","properties":{"scope":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"],"additionalProperties":false},"query":{"type":"string","enum":[%q]}},"required":["scope","query"],"additionalProperties":false}`, choice))
		resource.Resolved.Toolset.Tools[0].ConsumerContract.TextOnly.PayloadSchema = schema
		resource.Resolved.Toolset.Tools[0].ConsumerContract.TextOnly.Payload.SchemaWithoutRootExample = schema
		definition := testRegistryAgentDefinition(registeredTestSource{[]*genregistry.RegisteredToolsetSelection{resource}})
		catalog, err := rt.resolveRegistryCatalog(t.Context(), definition)
		require.NoError(t, err)
		name := registeredTestName(t, resource, "records.find")
		specs := []tools.ToolSpec{catalog.specs[name]}
		require.NoError(t, catalog.prepareModelDefinitions(specs, compiledToolPolicy{textOnly: true}, nil))
		compiled := catalog.definitions[name].textOnly
		require.NotNil(t, compiled)
		assert.Contains(t, string(compiled.Input.Contract().Schema), fmt.Sprintf(`"enum":[%q]`, choice))
		require.NoError(t, catalog.prepareModelDefinitions(specs, compiledToolPolicy{textOnly: true}, nil))
		assert.Same(t, compiled, catalog.definitions[name].textOnly)
		if previous != nil {
			assert.Contains(t, string(previous.definitions[name].textOnly.Input.Contract().Schema), `"north"`)
			assert.NotSame(t, previous.definitions[name].textOnly, compiled)
		}
		previous = catalog
	}
}

// BenchmarkCompiledTextOnlyDefinitions measures repeated reads after all
// registered contracts have been validated and compiled.
func BenchmarkCompiledTextOnlyDefinitions(b *testing.B) {
	specs := make([]tools.ToolSpec, 32)
	var ordinary, text [32]atomic.Int64
	rt := New(newTestStore())
	for index := range specs {
		spec := countedCompiledSpec(&ordinary[index], &text[index])
		spec.Name = tools.Ident(fmt.Sprintf("records.read_%02d", index))
		specs[index] = spec
	}
	seedTestToolDefinitions(rt, specs...)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		definitions := rt.advertisedToolDefinitions(specs, compiledToolPolicy{textOnly: true})
		goruntime.KeepAlive(definitions)
	}
	b.StopTimer()
	for index := range specs {
		assert.EqualValues(b, 1, ordinary[index].Load())
		assert.EqualValues(b, 1, text[index].Load())
	}
}

// countedCompiledSpec gives both views identical schema bytes and distinct
// decoders, so an advertising read cannot substitute a schema-only cache entry.
func countedCompiledSpec(ordinary, text *atomic.Int64) tools.ToolSpec {
	spec := newAnyJSONSpec("records.read")
	spec.Search = tools.NewSearchDocument("Read records")
	spec.Payload.Schema = tools.RawJSON(`{"type":"object"}`)
	spec.Payload.SchemaWithoutRootExample = spec.Payload.Schema
	spec.Payload.ExampleJSON = tools.RawJSON(`{}`)
	spec.Payload.Codec = countedCompiledCodec(ordinary)
	spec.TextOnly = &tools.ModelContract{Description: "Read records", Search: spec.Search, Payload: spec.Payload, ExecutionSchema: spec.ExecutionPayloadSchema, ExecutionCodec: tools.AnyJSONCodec}
	spec.TextOnly.Payload.Codec = countedCompiledCodec(text)
	return spec
}

// countedCompiledCodec counts typed decoding, including construction-time
// example checks, while preserving the ordinary synthetic JSON result.
func countedCompiledCodec(counter *atomic.Int64) tools.JSONCodec[any] {
	return tools.JSONCodec[any]{
		ToJSON: tools.AnyJSONCodec.ToJSON,
		FromJSON: func(raw []byte) (any, error) {
			counter.Add(1)
			return tools.AnyJSONCodec.FromJSON(raw)
		},
	}
}

func rejectCompiledExample([]byte) (any, error) {
	return nil, errors.New("example rejected")
}
func unusedCompiledExecution(context.Context, *ToolCall) (*ToolExecutionResult, error) {
	return nil, nil
}
