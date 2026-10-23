// Package runtime checks that compiling a complete service once preserves raw
// binding checks and saved calls while each activity owns its restored values.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/planner"
	"goa.design/goa-ai/runtime/agent/tools"
	toolcontract "goa.design/goa-ai/runtime/toolregistry/contract"
)

func TestCompiledStaticServicePreservesRawSelections(t *testing.T) {
	for _, consumer := range []string{"sparse", "full", "unsorted pair"} {
		t.Run(consumer, func(t *testing.T) {
			fixture := newStaticBindingFixture(t, 6)
			names := fixture.namesFor(consumer)
			if consumer == "unsorted pair" {
				names = []tools.Ident{"records.extra_003", "records.find", "records.continue", "records.extra_000"}
			}
			fixture.selected.Toolset.Tools[0].ConsumerContract.Payload.Fields[0].DiscriminatorValues = []string{}
			compiled, err := CompileStaticService(fixture.expected, fixture.selected)
			require.NoError(t, err)
			assert.Len(t, compiled.selections, len(fixture.selected.Toolset.Tools))
			raw, prepared := fixture.catalog(), fixture.catalog()
			require.NoError(t, raw.BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, names...))
			require.NoError(t, prepared.BindCompiledStaticService(testRegistryName, compiled, readTestStaticResource, names...))
			for _, name := range names {
				left, err := raw.selections[name].resolution.Select(testRegistryName, name)
				require.NoError(t, err)
				right, err := prepared.selections[name].resolution.Select(testRegistryName, name)
				require.NoError(t, err)
				assert.Equal(t, left, right)
				assert.Equal(t, raw.labels[name], prepared.labels[name])
				for operation, spec := range raw.selections[name].resolution.Specs {
					assert.True(t, equivalentToolSpec(spec, prepared.selections[name].resolution.Specs[operation]))
				}
				selection, err := genregistry.DecodeRegistrySelection(compiled.selections[name])
				require.NoError(t, err)
				value, ok := selection.Selection.AsCompiledService()
				require.True(t, ok)
				assert.Contains(t, value.Operations, string(name))
				assert.LessOrEqual(t, len(value.Operations), 2)
			}
		})
	}
}

func TestCompiledStaticServiceOwnsRawInputs(t *testing.T) {
	fixture := newStaticBindingFixture(t, 4)
	names := []tools.Ident{"records.extra_002", "records.find"}
	expected := staticBindingExpectedBindings(t, fixture.selected, names)
	compiled, err := CompileStaticService(fixture.expected, fixture.selected)
	require.NoError(t, err)
	fixture.expected.Name = "changed.expected"
	fixture.expected.Tools[0].ConsumerContract.Meta["ordered_values"][0] = "changed expected"
	fixture.selected.RegistrationToken = strings.Repeat("b", 64)
	fixture.selected.Toolset.Name = "changed.selected"
	*fixture.selected.Toolset.Description = "Changed"
	fixture.selected.Toolset.Tags[0] = "changed"
	fixture.selected.Toolset.RegisteredAt = "2026-09-18T00:00:00Z"
	fixture.selected.Toolset.Tools[0].PayloadSchema[0] = '!'
	fixture.selected.Toolset.Tools[0].ConsumerContract.RequiredLabels[0] = "changed"
	fixture.selected.Toolset.Tools[0].ConsumerContract.Meta["ordered_values"][0] = "changed selected"
	fixture.selected.Toolset.Tools[len(fixture.selected.Toolset.Tools)-1].Name = "changed.unselected"
	catalog := fixture.catalog()
	require.NoError(t, catalog.BindCompiledStaticService(testRegistryName, compiled, readTestStaticResource, names...))
	for _, name := range names {
		binding, err := catalog.selections[name].resolution.Select(testRegistryName, name)
		require.NoError(t, err)
		assert.Equal(t, expected[name], binding)
	}
	assert.Equal(t, []string{"second_label", "first_label"}, catalog.labels["records.find"])
	require.Error(t, fixture.catalog().BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, names...))
}

func TestCompiledStaticServiceRejectsCompleteRawChanges(t *testing.T) {
	for _, change := range []string{"unselected description", "unselected schema", "unselected kind", "invalid text", "missing union", "negative wide index", "missing schema", "nil tool", "changed expected"} {
		t.Run(change, func(t *testing.T) {
			fixture := newStaticBindingFixture(t, 4)
			last := fixture.selected.Toolset.Tools[len(fixture.selected.Toolset.Tools)-1]
			switch change {
			case "unselected description":
				last.Description = new("Changed unrelated operation")
			case "unselected schema":
				last.PayloadSchema = []byte(`{`)
			case "unselected kind":
				last.ConsumerContract.Kind = "unsupported"
			case "invalid text":
				last.ConsumerContract.Title = string([]byte{0xff})
			case "missing union":
				last.ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{Path: []*genregistry.ToolFieldPathSegment{{Segment: genregistry.ToolFieldSegment{}}}}}
			case "negative wide index":
				last.ConsumerContract.Payload.Fields = []*genregistry.ToolFieldMetadata{{Branches: []*genregistry.ToolUnionBranch{{Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: -1})}}}}
			case "missing schema":
				last.ExecutionPayloadSchema = nil
			case "nil tool":
				fixture.selected.Toolset.Tools[len(fixture.selected.Toolset.Tools)-1] = nil
			case "changed expected":
				fixture.expected.Tools[len(fixture.expected.Tools)-1].Description = new("Changed expected unrelated operation")
			}
			compiled, compileErr := CompileStaticService(fixture.expected, fixture.selected)
			rawErr := fixture.catalog().BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, "records.find")
			require.Error(t, compileErr)
			require.Error(t, rawErr)
			assert.Nil(t, compiled)
			assert.Equal(t, rawErr.Error(), compileErr.Error())
		})
	}
}

func TestCompiledStaticServicePreservesOriginalValues(t *testing.T) {
	for _, variant := range []string{"empty omitted collections", "wide integer"} {
		t.Run(variant, func(t *testing.T) {
			fixture := newStaticBindingFixture(t, 4)
			index := len(fixture.expected.Tools) - 1
			name := tools.Ident(fixture.expected.Tools[index].Name)
			expected := fixture.expected.Tools[index].ConsumerContract
			selected := fixture.selected.Toolset.Tools[index].ConsumerContract
			generated := fixture.definition.declarations[name].ConsumerContract
			switch variant {
			case "empty omitted collections":
				expected.Meta, generated.Meta = nil, nil
				selected.Meta = map[string][]string{}
				expected.RequiredLabels, generated.RequiredLabels = nil, nil
				selected.RequiredLabels = []string{}
				expected.Payload.Fields, generated.Payload.Fields = nil, nil
				selected.Payload.Fields = []*genregistry.ToolFieldMetadata{}
				expected.Payload.ExampleJSON, generated.Payload.ExampleJSON = nil, nil
				selected.Payload.ExampleJSON = []byte{}
			case "wide integer":
				if strconv.IntSize < 64 {
					t.Skip("wide Go integer needs a 64-bit host")
				}
				for _, value := range []*genregistry.ConsumerContract{expected, selected, generated} {
					value.Payload.Fields = []*genregistry.ToolFieldMetadata{{Branches: []*genregistry.ToolUnionBranch{{Selection: genregistry.NewToolUnionSelectionUntagged(&genregistry.ToolUntaggedUnionBranch{JSONKind: "object", Index: 4294967297})}}}}
				}
			}
			spec, err := toolcontract.Compile(fixture.definition.declarations[name])
			require.NoError(t, err)
			fixture.definition.specByName[name] = spec
			compiled, err := CompileStaticService(fixture.expected, fixture.selected)
			require.NoError(t, err)
			raw, prepared := fixture.catalog(), fixture.catalog()
			require.NoError(t, raw.BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, name))
			require.NoError(t, prepared.BindCompiledStaticService(testRegistryName, compiled, readTestStaticResource, name))
			left, err := raw.selections[name].resolution.Select(testRegistryName, name)
			require.NoError(t, err)
			right, err := prepared.selections[name].resolution.Select(testRegistryName, name)
			require.NoError(t, err)
			assert.Equal(t, left, right)
			if variant == "wide integer" {
				assert.Equal(t, 4294967297, prepared.selections[name].resolution.Specs[name].Payload.Fields[0].Branches[0].(tools.UntaggedUnionBranch).Index)
			}
		})
	}
}

func TestCompiledStaticServiceBindChecksCurrentConsumer(t *testing.T) {
	for _, change := range []string{"source", "missing static", "spec policy", "generated declaration", "duplicate", "already bound", "zero", "nil", "reader", "registry", "empty names"} {
		t.Run(change, func(t *testing.T) {
			fixture := newStaticBindingFixture(t, 4)
			compiled, err := CompileStaticService(fixture.expected, fixture.selected)
			require.NoError(t, err)
			catalog := fixture.catalog()
			registry := testRegistryName
			names := []tools.Ident{"records.find"}
			reader := readTestStaticResource
			switch change {
			case "source":
				catalog.sources = nil
			case "missing static":
				delete(catalog.definition.specByName, names[0])
			case "spec policy":
				spec := catalog.definition.specByName[names[0]]
				spec.Bookkeeping = true
				catalog.definition.specByName[names[0]] = spec
			case "generated declaration":
				catalog.definition.declarations[names[0]].Description = new("Changed consuming build")
			case "duplicate":
				names = append(names, names[0])
			case "already bound":
				require.NoError(t, catalog.BindCompiledStaticService(registry, compiled, reader, names...))
			case "zero":
				compiled = &CompiledStaticService{}
			case "nil":
				compiled = nil
			case "reader":
				reader = nil
			case "registry":
				registry = "unknown"
			case "empty names":
				names = nil
			}
			beforeSelections, beforeLabels := maps.Clone(catalog.selections), maps.Clone(catalog.labels)
			require.Error(t, catalog.BindCompiledStaticService(registry, compiled, reader, names...))
			assert.Equal(t, beforeLabels, catalog.labels)
			assert.Len(t, catalog.selections, len(beforeSelections))
		})
	}
}

func TestCompiledStaticServiceFailedRestoreDoesNotPartiallyBind(t *testing.T) {
	fixture := newStaticBindingFixture(t, 4)
	compiled, err := CompileStaticService(fixture.expected, fixture.selected)
	require.NoError(t, err)
	catalog := fixture.catalog()
	delete(catalog.definition.declarations, "records.extra_001")
	err = catalog.BindCompiledStaticService(testRegistryName, compiled, readTestStaticResource, "records.find", "records.extra_001")
	require.ErrorContains(t, err, "cannot be restored")
	assert.Empty(t, catalog.selections)
	assert.Empty(t, catalog.labels)
}

func TestCompiledStaticServiceConcurrentActivitiesUseFreshReaders(t *testing.T) {
	fixture := newStaticBindingFixture(t, 4)
	compiled, err := CompileStaticService(fixture.expected, fixture.selected)
	require.NoError(t, err)
	const activities = 8
	catalogs := make([]*RegistryCatalog, activities)
	failures := make(chan error, activities)
	var workers sync.WaitGroup
	for index := range activities {
		workers.Go(func() {
			catalog := fixture.catalog()
			readerFailure := fmt.Errorf("reader for activity %d", index)
			reader := func(context.Context, string, string) (*genregistry.ResolvedToolset, error) { return nil, readerFailure }
			if err := catalog.BindCompiledStaticService(testRegistryName, compiled, reader, "records.find"); err != nil {
				failures <- err
				return
			}
			_, err := catalog.selections["records.find"].readPrevious(t.Context(), "old", "token")
			if !errors.Is(err, readerFailure) {
				failures <- fmt.Errorf("activity %d used another reader: %w", index, err)
				return
			}
			catalogs[index] = catalog
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	catalogs[0].labels["records.find"][0] = "changed"
	for _, catalog := range catalogs[1:] {
		assert.Equal(t, []string{"second_label", "first_label"}, catalog.labels["records.find"])
	}
}

func TestRawStaticServiceBindingKeepsFailurePrecedence(t *testing.T) {
	fixture := newStaticBindingFixture(t, 4)
	catalog := fixture.catalog()
	err := catalog.BindStaticService("unknown", nil, nil, nil)
	require.EqualError(t, err, "static service binding requires a reader for previous registrations")
	err = catalog.BindStaticService("unknown", nil, nil, readTestStaticResource)
	require.EqualError(t, err, "static service binding requires explicit tool names")
	err = catalog.BindStaticService("unknown", nil, nil, readTestStaticResource, "records.find")
	require.ErrorContains(t, err, "unknown")
	err = catalog.BindStaticService(testRegistryName, nil, nil, readTestStaticResource, "records.find")
	require.ErrorContains(t, err, "encode ResolvedToolset JSON")
	catalog.sources = nil
	err = catalog.BindStaticService(testRegistryName, nil, fixture.selected, readTestStaticResource, "records.find")
	require.EqualError(t, err, "static service binding requires the generated toolset declaration")
	fixture.expected.Description = new("Changed complete expected catalog")
	err = catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, readTestStaticResource, "records.find")
	require.EqualError(t, err, "static service binding differs from the complete generated declaration")
	assert.Empty(t, catalog.selections)
}

func TestCompiledStaticServiceKeepsEmptyCheckedCatalog(t *testing.T) {
	fixture := newStaticBindingFixture(t, 0)
	fixture.expected.Tools = []*genregistry.ToolSchema{}
	fixture.selected.Toolset.Tools = []*genregistry.ToolSchema{}
	compiled, err := CompileStaticService(fixture.expected, fixture.selected)
	require.NoError(t, err)
	assert.NotEmpty(t, compiled.route)
	assert.Empty(t, compiled.selections)
	err = fixture.catalog().BindCompiledStaticService(testRegistryName, compiled, readTestStaticResource, "records.find")
	require.ErrorContains(t, err, "contains no tool")
}

func TestCompiledStaticServiceNewPagesUseCurrentRegistrationAndFreshProof(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprintf("denied=%t", denied), func(t *testing.T) {
			fixture := newStaticBindingFixture(t, 4)
			previousRoute, previousToken := fixture.selected.Toolset.Name, fixture.selected.RegistrationToken
			previous := staticBindingExpectedBindings(t, fixture.selected, []tools.Ident{"records.find"})["records.find"]
			fixture.selected.Toolset.Name = "current.records"
			fixture.selected.RegistrationToken = strings.Repeat("b", 64)
			compiled, err := CompileStaticService(fixture.expected, fixture.selected)
			require.NoError(t, err)
			catalog := fixture.catalog()
			ownerFailure := errors.New("owner denied the previous registration")
			reads := 0
			reader := func(ctx context.Context, route, token string) (*genregistry.ResolvedToolset, error) {
				assert.Equal(t, t.Context(), ctx)
				assert.Equal(t, previousRoute, route)
				assert.Equal(t, previousToken, token)
				reads++
				if denied {
					return nil, ownerFailure
				}
				resolved, err := registrycontract.Read(previous, fixture.definition.registryDeclaration)
				if err != nil {
					return nil, err
				}
				return resolved.Registered, nil
			}
			require.NoError(t, catalog.BindCompiledStaticService(testRegistryName, compiled, reader, "records.find"))
			assert.Zero(t, reads)
			output := sourceContinuationOutput("records.find", "query", `{"value":1}`, "next-cursor")
			output.Registry = previous
			before := previous.Clone()
			actions, err := fixture.runtime.availableContinuationActions(fixture.definition.route.ID, []*planner.ToolOutput{output}, false)
			require.NoError(t, err)
			actions, err = catalog.bindContinuationActions(t.Context(), actions)
			assert.Equal(t, 1, reads)
			if denied {
				require.ErrorIs(t, err, ownerFailure)
				assert.Empty(t, actions)
			} else {
				require.NoError(t, err)
				require.Len(t, actions, 1)
				token, route, err := registrycontract.ServiceRoute(actions[0].registry, "records.continue")
				require.NoError(t, err)
				assert.Equal(t, fixture.selected.Toolset.Name, route)
				assert.Equal(t, fixture.selected.RegistrationToken, token)
			}
			assert.Equal(t, before, previous)
		})
	}
}

func TestRawStaticServiceSourceCheckPrecedesServiceKindCheck(t *testing.T) {
	fixture := newStaticBindingFixture(t, 0)
	selected := testNativeRegistryResolution("revision/1")
	catalog := fixture.catalog()
	catalog.sources = nil
	err := catalog.BindStaticService(testRegistryName, selected.Toolset, selected, readTestStaticResource, "records.find")
	require.ErrorContains(t, err, "outside this agent's declared consumption")
	err = fixture.catalog().BindStaticService(testRegistryName, selected.Toolset, selected, readTestStaticResource, "records.find")
	require.ErrorContains(t, err, "static service binding cannot include agent")
}
