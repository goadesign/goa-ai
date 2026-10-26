// Package runtime measures static service binding with synthetic declarations.
// The same independent generated-style inputs feed sparse and full consumers;
// retained-memory measurements keep only finished activity catalogs alive.
package runtime

import (
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genrecords "goa.design/goa-ai/internal/testpresentation/gen/records/toolsets/records"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	genrunidentity "goa.design/goa-ai/registry/gen/runidentity"
	"goa.design/goa-ai/runtime/agent/rawjson"
	"goa.design/goa-ai/runtime/agent/tools"
)

type staticBindingFixture struct {
	runtime    *Runtime
	definition AgentDefinition
	expected   *genregistry.Toolset
	selected   *genregistry.ResolvedToolset
	names      []tools.Ident
}

// BenchmarkStaticServiceBinding measures complete binding, including its
// unchanged JSON copy, full declaration checks and generated restore checks.
func BenchmarkStaticServiceBinding(b *testing.B) {
	for _, consumer := range []string{"sparse", "full"} {
		b.Run(consumer, func(b *testing.B) {
			fixture := newStaticBindingFixture(b, 46)
			names := fixture.namesFor(consumer)
			warm := fixture.catalog()
			require.NoError(b, warm.BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, names...))
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				catalog := fixture.catalog()
				if err := catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, names...); err != nil {
					b.Fatal(err)
				}
				runtime.KeepAlive(catalog)
			}
			runtime.KeepAlive(fixture)
		})
	}
}

// BenchmarkStaticServiceBindingRetained keeps eight completed catalogs after
// each bounded cohort, collects temporary values, and reports live bytes per
// catalog. Its elapsed time includes collection; use the other benchmark for
// binding time and total allocation.
func BenchmarkStaticServiceBindingRetained(b *testing.B) {
	const cohortSize = 8
	for _, consumer := range []string{"sparse", "full"} {
		b.Run(consumer, func(b *testing.B) {
			fixture := newStaticBindingFixture(b, 46)
			names := fixture.namesFor(consumer)
			warm := fixture.catalog()
			require.NoError(b, warm.BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, names...))
			runtime.KeepAlive(warm)
			warm = nil
			var totalLive uint64
			b.ResetTimer()
			for range b.N {
				catalogs := make([]*RegistryCatalog, cohortSize)
				runtime.GC()
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				for index := range catalogs {
					catalog := fixture.catalog()
					if err := catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, names...); err != nil {
						b.Fatal(err)
					}
					catalogs[index] = catalog
				}
				runtime.GC()
				runtime.GC()
				runtime.ReadMemStats(&after)
				if after.HeapAlloc < before.HeapAlloc {
					b.Fatal("retained heap decreased while the completed catalog cohort remained live")
				}
				totalLive += after.HeapAlloc - before.HeapAlloc
				runtime.KeepAlive(catalogs)
				runtime.KeepAlive(fixture)
			}
			b.ReportMetric(float64(totalLive)/float64(b.N*cohortSize), "retained_B/catalog")
		})
	}
}

// BenchmarkStaticAgentBinding measures per-turn static Agent binding across a
// 70-tool export set using the consuming definition's pre-compiled declarations.
func BenchmarkStaticAgentBinding(b *testing.B) {
	generated := staticBindingDeclarations(68)
	expected := make([]*genregistry.ToolSchema, len(generated.Tools))
	for index, declaration := range generated.Tools {
		declaration.ConsumerContract.Kind = "agent"
		unbound := *declaration
		contract := *declaration.ConsumerContract
		unbound.ConsumerContract = &contract
		expected[index] = &unbound
		declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{
			Executor:      "generic.agent",
			Workflow:      "generic.workflow",
			Queue:         "generic.queue",
			Configuration: "revision/1",
		}
	}
	compiled, err := registrycontract.Compile(generated.Tools)
	require.NoError(b, err)
	specs := make([]tools.ToolSpec, 0, len(generated.Tools))
	names := make([]tools.Ident, 0, len(generated.Tools))
	for _, declaration := range generated.Tools {
		name := tools.Ident(declaration.Name)
		spec := compiled[name]
		spec.IsAgentTool = true
		spec.AgentID = "generic.agent"
		specs = append(specs, spec)
		names = append(names, name)
	}
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", specs, nil)
	definition := NewAgentDefinition(
		AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"},
		specs, nil, nil, names, []AgentDefinition{child}, nil,
	).WithRegistryTools(testRegistrySources{whole: true}).WithRegistryDeclarations(testDeclarations(expected))
	rt := New(newTestStore())
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	for _, spec := range specs {
		rt.toolSpecs[spec.Name] = spec
		seedTestToolDefinitions(rt, spec)
	}
	require.NoError(b, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	selected := &genregistry.ResolvedToolset{Toolset: generated, RegistrationToken: strings.Repeat("a", 64)}
	selected.Toolset.Name = "tenant.records_agent"
	selected.Toolset.RegisteredAt = "2026-09-17T00:00:00Z"

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		catalog := rt.newRegistryCatalog(definition)
		if err := catalog.BindStaticAgent(testRegistryName, expected, selected, readTestStaticAgentResource, names...); err != nil {
			b.Fatal(err)
		}
		runtime.KeepAlive(catalog)
	}
}

// BenchmarkCompiledAgentTurnBatch measures the synchronous workflow-thread
// contract resolution performed across a 20-call compiled Agent batch turn.
func BenchmarkCompiledAgentTurnBatch(b *testing.B) {
	selected := &genregistry.ResolvedToolset{Toolset: genrecords.Toolset(), RegistrationToken: strings.Repeat("a", 64)}
	var operation *genregistry.ToolSchema
	for _, candidate := range selected.Toolset.Tools {
		if candidate.Name == string(genrecords.Read) {
			operation = candidate
		}
	}
	require.NotNil(b, operation)
	operation.ConsumerContract.Kind = "agent"
	expected := *operation
	expectedContract := *operation.ConsumerContract
	expected.ConsumerContract = &expectedContract
	operation.ConsumerContract.Agent = &genregistry.AgentToolTarget{
		Executor:      "generic.agent",
		Workflow:      "generic.workflow",
		Queue:         "generic.queue",
		Configuration: "revision/1",
	}
	selected.Toolset.Tools = []*genregistry.ToolSchema{operation}
	selected.Toolset.RegisteredAt = "2026-09-17T00:00:00Z"
	spec := genrecords.SpecRead()
	spec.IsAgentTool = true
	spec.AgentID = "generic.agent"
	child := testAgentDefinition("generic.agent", "generic.workflow", "generic.queue", nil, nil)
	definition := NewAgentDefinition(
		AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"},
		[]tools.ToolSpec{spec}, nil, nil, []tools.Ident{spec.Name}, []AgentDefinition{child}, nil,
	).WithRegistryTools(testRegistrySources{whole: true}).WithRegistryDeclarations(testDeclarations([]*genregistry.ToolSchema{&expected}))
	rt := New(newTestStore())
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	rt.toolSpecs[spec.Name] = spec
	seedTestToolDefinitions(rt, spec)
	require.NoError(b, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	catalog := rt.newRegistryCatalog(definition)
	require.NoError(b, catalog.BindStaticAgent(testRegistryName, []*genregistry.ToolSchema{&expected}, selected, readTestStaticAgentResource, genrecords.Read))
	binding, err := catalog.selections[genrecords.Read].resolution.Select(testRegistryName, genrecords.Read)
	require.NoError(b, err)

	calls := make([]ToolCall, 20)
	for index := range calls {
		calls[index] = ToolCall{
			Name:       genrecords.Read,
			Registry:   binding,
			ToolCallID: fmt.Sprintf("call-%d", index),
			RunID:      "parent-run",
			SessionID:  "session-1",
			TurnID:     "turn-1",
			Payload:    rawjson.Message(`{"query":"all"}`),
			Subject:    genrunidentity.RunSubject{Executor: genrunidentity.NewRunExecutorAgent("records.agent")},
		}
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, call := range calls {
			if err := validateCheckpointToolRequest(call, definition); err != nil {
				b.Fatal(err)
			}
			if _, err := rt.resolveRegistryExecution(definition.route.ID, call); err != nil {
				b.Fatal(err)
			}
			if _, err := registryCallMetadata(call, definition); err != nil {
				b.Fatal(err)
			}
			for range 3 {
				if _, _, err := rt.callSpec(call); err != nil {
					b.Fatal(err)
				}
			}
			if _, err := rt.selectedAgentToolConfig(call); err != nil {
				b.Fatal(err)
			}
		}
	}
}

// newStaticBindingFixture constructs separate provider and generated values, so
// mutating a provider response never changes the definition's declarations.
func newStaticBindingFixture(tb testing.TB, extraTools int) staticBindingFixture {
	tb.Helper()
	generated := staticBindingDeclarations(extraTools)
	compiled, err := registrycontract.Compile(generated.Tools)
	require.NoError(tb, err)
	specs := make([]tools.ToolSpec, 0, len(generated.Tools))
	names := make([]tools.Ident, 0, len(generated.Tools))
	for _, declaration := range generated.Tools {
		name := tools.Ident(declaration.Name)
		specs = append(specs, compiled[name])
		names = append(names, name)
	}
	definition := NewAgentDefinition(AgentRoute{ID: "records.agent", WorkflowName: "records.workflow", DefaultTaskQueue: "records.queue"}, specs, nil, nil, names, nil, nil).WithRegistryTools(testRegistrySources{whole: true}).WithRegistryDeclarations(testDeclarations(generated.Tools))
	rt := New(newTestStore())
	rt.agents[definition.route.ID] = AgentRegistration{Definition: definition}
	for _, spec := range specs {
		rt.toolSpecs[spec.Name] = spec
		seedTestToolDefinitions(rt, spec)
	}
	require.NoError(tb, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	selected := &genregistry.ResolvedToolset{Toolset: staticBindingDeclarations(extraTools), RegistrationToken: strings.Repeat("a", 64)}
	selected.Toolset.Name = "tenant.records"
	selected.Toolset.RegisteredAt = "2026-09-17T00:00:00Z"
	return staticBindingFixture{runtime: rt, definition: definition, expected: staticBindingDeclarations(extraTools), selected: selected, names: names}
}

// staticBindingDeclarations returns a query/page pair and unrelated generated
// record readers. Labels and optional metadata make saved-byte preservation
// visible without using any application-specific data.
func staticBindingDeclarations(extraTools int) *genregistry.Toolset {
	selected := testRegistryPagingResolution(strings.Repeat("a", 64)).Toolset
	selected.Description = new("Synthetic record service")
	selected.Tags = []string{"records", "read"}
	selected.RegisteredAt = ""
	for _, declaration := range selected.Tools {
		declaration.ConsumerContract.RequiredLabels = []string{"second_label", "first_label"}
		declaration.ConsumerContract.Meta = map[string][]string{"nil_values": nil, "empty_values": {}, "ordered_values": {"second", "first"}}
	}
	for index := range extraTools {
		declaration, ok := genrecords.ToolSchema(genrecords.Read)
		if !ok {
			panic("generated record reader is required")
		}
		declaration.Name = fmt.Sprintf("records.extra_%03d", index)
		selected.Tools = append(selected.Tools, declaration)
	}
	return selected
}

func (f staticBindingFixture) catalog() *RegistryCatalog {
	return f.runtime.newRegistryCatalog(f.definition)
}

func (f staticBindingFixture) namesFor(consumer string) []tools.Ident {
	if consumer == "sparse" {
		return []tools.Ident{"records.find"}
	}
	names := slices.Clone(f.names)
	slices.Reverse(names)
	return names
}
