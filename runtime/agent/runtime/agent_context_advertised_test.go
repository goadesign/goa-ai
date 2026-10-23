// Package runtime checks that planner tool advertising leaves owned tool
// specifications unchanged and returns separate values to each caller.
package runtime

import (
	"fmt"
	goruntime "runtime"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	"goa.design/goa-ai/runtime/agent"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestAdvertisedToolDefinitionsPreserveOwnedInputs(t *testing.T) {
	for _, catalog := range []bool{false, true} {
		for _, supplied := range []bool{false, true} {
			for _, textOnly := range []bool{false, true} {
				t.Run(fmt.Sprintf("catalog=%t/supplied=%t/text=%t", catalog, supplied, textOnly), func(t *testing.T) {
					specs := []tools.ToolSpec{genrecords.SpecShow(), genrecords.SpecRead(), genrecords.SpecErase()}
					specs[1].Payload.ExampleJSON = tools.RawJSON(`{"query":"example"}`)
					specs[1].TextOnly.Payload.ExampleJSON = tools.RawJSON(`{"query":"example"}`)
					for index := range specs {
						specs[index].Tags = []string{"readable"}
					}
					specs[2].Tags = []string{"write"}
					ctx := advertisedContextFixture(t, specs, catalog, supplied)
					ctx.unavailableTools = []tools.Ident{specs[0].Name}
					ctx.policy = compileToolPolicy(&PolicyOverrides{
						TextOnly:   textOnly,
						TagClauses: []TagPolicyClause{{AllowedAny: []string{"readable"}}},
					})
					before := cloneToolSpecs(ctx.advertisedSpecs)
					expectedSpec := specs[1]
					if textOnly {
						expectedSpec = expectedSpec.ForTextOnly()
					}
					expected, err := model.NewToolDefinitionFromSpec(expectedSpec)
					require.NoError(t, err)
					expected.Deferred = true
					ctx.rt.agents[ctx.agent].Definition.deferredTools[specs[1].Name] = struct{}{}

					definitions := ctx.AdvertisedToolDefinitions()
					require.Len(t, definitions, 1)
					assertAdvertisedDefinitionEqual(t, expected, definitions[0])
					assertAdvertisedSpecsUnchanged(t, before, ctx.advertisedSpecs)

					// Caller changes to input specs and public snapshots must not
					// change the next definitions returned by this context.
					specs[1].Name = "changed.input"
					specs[1].Payload.Schema[0] = ' '
					specs[1].Search.Terms["changed"] = 1
					specs[1].Payload.Fields[0].Description = "Changed input field"
					specs[1].TextOnly.Payload.Schema[0] = ' '
					specs[1].TextOnly.Search.Terms["changed"] = 1
					detached := ctx.rt.ToolSpecsForAgent(ctx.agent)
					detached[1].Name = "changed.snapshot"
					detached[1].Payload.Schema[0] = ' '
					detached[1].Search.Terms["changed"] = 1
					detached[1].TextOnly.Payload.Schema[0] = ' '
					detached[1].TextOnly.Search.Terms["changed"] = 1
					definitions[0].Name = "changed.output"
					definitions[0].Description = "Changed output"
					definitions[0].Deferred = false
					definitions[0].Search.Terms["changed"] = 1
					contract := definitions[0].Input.Contract()
					contract.Schema[0] = ' '
					contract.SchemaWithoutRootExample[0] = ' '
					contract.ExampleJSON[0] = ' '
					definitions = ctx.AdvertisedToolDefinitions()
					require.Len(t, definitions, 1)
					assertAdvertisedDefinitionEqual(t, expected, definitions[0])
					assertAdvertisedSpecsUnchanged(t, before, ctx.advertisedSpecs)
				})
			}
		}
	}
}

func TestAdvertisedToolDefinitionsPresentEmptySpecs(t *testing.T) {
	specs := []tools.ToolSpec{genrecords.SpecRead()}
	ctx := advertisedContextFixture(t, specs, false, false)
	assert.Len(t, ctx.AdvertisedToolDefinitions(), 1)
	empty := newAgentContext(agentContextOptions{
		runtime: ctx.rt, agentID: ctx.agent, advertisedSpecs: []tools.ToolSpec{},
	})
	assert.Empty(t, empty.AdvertisedToolDefinitions())
}

func TestAdvertisedToolDefinitionsTextOnlyConfirmation(t *testing.T) {
	spec := genrecords.SpecRead()
	ctx := advertisedContextFixture(t, []tools.ToolSpec{spec}, false, true)
	ctx.rt.toolConfirmation = &ToolConfirmationConfig{
		Confirm: map[tools.Ident]*ToolConfirmation{spec.Name: {}},
	}
	assert.Len(t, ctx.AdvertisedToolDefinitions(), 1)
	ctx.policy = compileToolPolicy(&PolicyOverrides{TextOnly: true})
	assert.Empty(t, ctx.AdvertisedToolDefinitions())
	assert.Equal(t, spec.Name, ctx.advertisedSpecs[0].Name)
}

func TestAdvertisedToolDefinitionsConcurrentCallers(t *testing.T) {
	for _, catalog := range []bool{false, true} {
		for _, textOnly := range []bool{false, true} {
			t.Run(fmt.Sprintf("catalog=%t/text=%t", catalog, textOnly), func(t *testing.T) {
				specs := []tools.ToolSpec{genrecords.SpecShow(), genrecords.SpecRead()}
				ctx := advertisedContextFixture(t, specs, catalog, true)
				ctx.unavailableTools = []tools.Ident{specs[0].Name}
				ctx.policy = compileToolPolicy(&PolicyOverrides{TextOnly: textOnly})
				expected := ctx.AdvertisedToolDefinitions()
				require.Len(t, expected, 1)
				var callers sync.WaitGroup
				for range 8 {
					callers.Go(func() {
						for range 8 {
							definitions := ctx.AdvertisedToolDefinitions()
							if !assert.Len(t, definitions, 1) {
								return
							}
							assertAdvertisedDefinitionEqual(t, expected[0], definitions[0])
							definitions[0].Name = "changed.concurrent"
							definitions[0].Search.Terms["changed"] = 1
							contract := definitions[0].Input.Contract()
							contract.Schema[0] = ' '
						}
					})
				}
				callers.Wait()
				assertAdvertisedDefinitionEqual(t, expected[0], ctx.AdvertisedToolDefinitions()[0])
				assert.Equal(t, specs[0].Name, ctx.advertisedSpecs[0].Name)
			})
		}
	}
}

// BenchmarkAdvertisedToolDefinitions measures one complete advertising call
// over 32 synthetic tool contracts. Setup and contract compilation are excluded.
func BenchmarkAdvertisedToolDefinitions(b *testing.B) {
	for _, catalog := range []bool{false, true} {
		for _, supplied := range []bool{false, true} {
			b.Run(fmt.Sprintf("catalog=%t/supplied=%t", catalog, supplied), func(b *testing.B) {
				specs := make([]tools.ToolSpec, 32)
				for index := range specs {
					spec := newAnyJSONSpec(tools.Ident(fmt.Sprintf("records.read_%02d", index)))
					spec.Description = "Read a record"
					spec.Search = tools.NewSearchDocument(spec.Name.String() + " Read a record")
					spec.Tags = []string{"read"}
					spec.Meta = map[string][]string{"category": {"records"}}
					schema := tools.RawJSON(`{"type":"object","description":"` + strings.Repeat("Record field. ", 256) + `","properties":{"query":{"type":"string"}}}`)
					spec.Payload.Schema = schema
					spec.Payload.SchemaWithoutRootExample = append(tools.RawJSON(nil), schema...)
					spec.Payload.ExampleJSON = tools.RawJSON(`{"query":"example"}`)
					spec.Payload.Fields = []tools.FieldMetadata{{Path: []tools.FieldPathSegment{tools.FixedField("query")}, JSONType: "string", Description: "Record query"}}
					spec.Result.Schema = append(tools.RawJSON(nil), schema...)
					specs[index] = spec
				}
				ctx := advertisedContextFixture(b, specs, catalog, supplied)
				definitions := ctx.AdvertisedToolDefinitions()
				require.Len(b, definitions, len(specs))
				for index, spec := range specs {
					expected, err := model.NewToolDefinitionFromSpec(spec)
					require.NoError(b, err)
					assertAdvertisedDefinitionEqual(b, expected, definitions[index])
				}
				b.ReportAllocs()
				b.ResetTimer()
				for range b.N {
					definitions := ctx.AdvertisedToolDefinitions()
					goruntime.KeepAlive(definitions)
				}
			})
		}
	}
}

// advertisedContextFixture gives a registered agent owned synthetic specs and
// compiled definitions, then selects the catalog and input source under test.
func advertisedContextFixture(tb testing.TB, specs []tools.ToolSpec, catalog, supplied bool) *simplePlannerContext {
	tb.Helper()
	const id agent.Ident = "records.agent"
	rt := newTestRuntimeWithPlanner(id, &stubPlanner{})
	rt.agentToolSpecs = map[agent.Ident][]tools.ToolSpec{id: cloneToolSpecs(specs)}
	seedTestToolDefinitions(rt, specs...)
	definition := rt.agents[id].Definition
	definition.deferredTools = make(map[tools.Ident]struct{})
	definition.specByName = make(map[tools.Ident]tools.ToolSpec, len(specs))
	for _, spec := range specs {
		definition.specByName[spec.Name] = spec
	}
	registration := rt.agents[id]
	registration.Definition = definition
	rt.agents[id] = registration
	opts := agentContextOptions{runtime: rt, agentID: id}
	if supplied {
		opts.advertisedSpecs = specs
	}
	if catalog {
		opts.catalog = &RegistryCatalog{definitions: mustToolDefinitions(specs)}
	}
	return newAgentContext(opts).(*simplePlannerContext)
}

// assertAdvertisedDefinitionEqual checks the values planners can observe,
// including detached input documents, without comparing codec functions.
func assertAdvertisedDefinitionEqual(tb testing.TB, expected, actual *model.ToolDefinition) {
	tb.Helper()
	assert.Equal(tb, expected.Name, actual.Name)
	assert.Equal(tb, expected.Description, actual.Description)
	assert.Equal(tb, expected.Search, actual.Search)
	assert.Equal(tb, expected.Deferred, actual.Deferred)
	assert.Equal(tb, expected.NoArguments, actual.NoArguments)
	assert.Equal(tb, expected.Input.Contract(), actual.Input.Contract())
}

// assertAdvertisedSpecsUnchanged checks the stored names and mutable contract
// data after filtering, so a write into the original slice is observable.
func assertAdvertisedSpecsUnchanged(tb testing.TB, expected, actual []tools.ToolSpec) {
	tb.Helper()
	if !assert.Len(tb, actual, len(expected)) {
		return
	}
	for index, spec := range expected {
		assert.Equal(tb, spec.Name, actual[index].Name)
		assert.Equal(tb, spec.Tags, actual[index].Tags)
		assert.Equal(tb, spec.Search, actual[index].Search)
		assert.Equal(tb, spec.Payload.Schema, actual[index].Payload.Schema)
		assert.Equal(tb, spec.Payload.Fields, actual[index].Payload.Fields)
		assert.Equal(tb, spec.Result.Schema, actual[index].Result.Schema)
		if spec.TextOnly != nil {
			assert.Equal(tb, spec.TextOnly.Search, actual[index].TextOnly.Search)
			assert.Equal(tb, spec.TextOnly.Payload.Schema, actual[index].TextOnly.Payload.Schema)
			assert.Equal(tb, spec.TextOnly.Payload.Fields, actual[index].TextOnly.Payload.Fields)
		}
	}
}
