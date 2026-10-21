// Package runtime checks that static service bindings retain the already-read
// generated operation and paging partner without changing saved call bytes,
// optional metadata, input ownership or complete-response rejection.
package runtime

import (
	"bytes"
	"fmt"
	"maps"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/registrycontract"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestStaticServiceRestoredSelectionsPreserveSavedBindings(t *testing.T) {
	for _, consumer := range []string{"sparse", "unsorted pair", "full"} {
		t.Run(consumer, func(t *testing.T) {
			fixture := newStaticBindingFixture(t, 6)
			names := fixture.namesFor(consumer)
			if consumer == "unsorted pair" {
				names = []tools.Ident{"records.extra_003", "records.find", "records.continue", "records.extra_000"}
			}
			// Generated JSON omission rules accept this empty optional list while
			// keeping null and empty values inside annotation maps distinct.
			fixture.selected.Toolset.Tools[0].ConsumerContract.Payload.Fields[0].DiscriminatorValues = []string{}
			expected := staticBindingExpectedBindings(t, fixture.selected, names)
			catalog := fixture.catalog()
			require.NoError(t, catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, names...))
			for _, name := range names {
				selection := catalog.selections[name]
				saved, err := selection.resolution.Select(selection.registry, name)
				require.NoError(t, err)
				assert.Equal(t, expected[name], saved, "the complete saved representation must remain identical")
				wantOperations := 1
				if name == "records.find" || name == "records.continue" {
					wantOperations = 2
				}
				assert.Len(t, selection.resolution.Registered.Toolset.Tools, wantOperations)
				if name == "records.find" {
					declaration, err := selection.resolution.Declaration(name)
					require.NoError(t, err)
					assert.Equal(t, []string{"second_label", "first_label"}, catalog.labels[name])
					assert.Nil(t, declaration.ConsumerContract.Meta["nil_values"])
					assert.NotNil(t, declaration.ConsumerContract.Meta["empty_values"])
					assert.Empty(t, declaration.ConsumerContract.Meta["empty_values"])
					assert.Equal(t, []string{"second", "first"}, declaration.ConsumerContract.Meta["ordered_values"])
				}
			}
		})
	}
}

func TestStaticServiceRestoredSelectionsKeepMutationIsolation(t *testing.T) {
	fixture := newStaticBindingFixture(t, 4)
	names := []tools.Ident{"records.extra_002", "records.find"}
	expected := staticBindingExpectedBindings(t, fixture.selected, names)
	catalog := fixture.catalog()
	require.NoError(t, catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, names...))
	original := fixture.selected.Toolset.Tools[0]
	fixture.selected.RegistrationToken = strings.Repeat("b", 64)
	fixture.selected.Toolset.Name = "replacement.records"
	*fixture.selected.Toolset.Description = "Changed description"
	*fixture.selected.Toolset.Version = "2.0.0"
	fixture.selected.Toolset.Tags[0] = "changed"
	fixture.selected.Toolset.RegisteredAt = "2026-09-18T00:00:00Z"
	original.Name = "records.changed"
	original.PayloadSchema[0] = '!'
	original.ConsumerContract.RequiredLabels[0] = "changed_label"
	original.ConsumerContract.Meta["nil_values"] = []string{"changed"}
	original.ConsumerContract.Meta["ordered_values"][0] = "changed"
	for _, name := range names {
		selection := catalog.selections[name]
		binding, err := selection.resolution.Select(selection.registry, name)
		require.NoError(t, err)
		assert.Equal(t, expected[name], binding)
	}
	assert.Equal(t, []string{"second_label", "first_label"}, catalog.labels["records.find"])
	declaration, err := catalog.selections["records.find"].resolution.Declaration("records.find")
	require.NoError(t, err)
	assert.Nil(t, declaration.ConsumerContract.Meta["nil_values"])
	assert.Equal(t, []string{"second", "first"}, declaration.ConsumerContract.Meta["ordered_values"])
}

func TestStaticServiceRestoredSelectionsRejectUnboundChanges(t *testing.T) {
	fixture := newStaticBindingFixture(t, 4)
	fixture.selected.Toolset.Tools[len(fixture.selected.Toolset.Tools)-1].Description = new("Changed unrelated operation")
	catalog := fixture.catalog()
	err := catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, "records.find")
	require.ErrorContains(t, err, "complete generated declaration")
	assert.Empty(t, catalog.selections)
	assert.Empty(t, catalog.labels)
}

func TestStaticServiceRestoreFailureLeavesExistingCatalogUntouched(t *testing.T) {
	fixture := newStaticBindingFixture(t, 4)
	catalog := fixture.catalog()
	require.NoError(t, catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, "records.extra_000"))
	beforeSelections := maps.Clone(catalog.selections)
	beforeLabels := maps.Clone(catalog.labels)
	catalog.definition.declarations = maps.Clone(catalog.definition.declarations)
	delete(catalog.definition.declarations, "records.extra_001")
	err := catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, "records.find", "records.extra_001")
	require.ErrorContains(t, err, "cannot be restored")
	assert.Equal(t, beforeSelections, catalog.selections)
	assert.Equal(t, beforeLabels, catalog.labels)
}

func TestStaticServiceRestoredSelectionsKeepConcurrentCatalogsIndependent(t *testing.T) {
	fixture := newStaticBindingFixture(t, 4)
	names := []tools.Ident{"records.find", "records.extra_002"}
	expected := staticBindingExpectedBindings(t, fixture.selected, names)
	catalogs := make([]*RegistryCatalog, 8)
	failures := make(chan error, len(catalogs))
	var workers sync.WaitGroup
	for index := range catalogs {
		workers.Go(func() {
			catalog := fixture.catalog()
			if err := catalog.BindStaticService(testRegistryName, fixture.expected, fixture.selected, names...); err != nil {
				failures <- err
				return
			}
			for _, name := range names {
				selection := catalog.selections[name]
				binding, err := selection.resolution.Select(selection.registry, name)
				if err != nil {
					failures <- err
					return
				}
				if binding.Registry != expected[name].Registry || !bytes.Equal(binding.Resolution, expected[name].Resolution) {
					failures <- fmt.Errorf("saved binding changed for %s", name)
					return
				}
			}
			catalogs[index] = catalog
		})
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		require.NoError(t, err)
	}
	catalogs[0].labels["records.find"][0] = "changed_label"
	for _, catalog := range catalogs[1:] {
		assert.Equal(t, []string{"second_label", "first_label"}, catalog.labels["records.find"])
	}
}

// staticBindingExpectedBindings saves calls from the complete owned provider
// response, which is the representation retained before this optimization.
// Comparing it with the catalog proves that dropping unrelated operations does
// not change the exact registration or query/page declarations saved for calls.
func staticBindingExpectedBindings(t *testing.T, selected *genregistry.ResolvedToolset, names []tools.Ident) map[tools.Ident]*tools.RegistryBinding {
	t.Helper()
	encoded, err := genregistry.EncodeResolvedToolset(selected)
	require.NoError(t, err)
	owned, err := genregistry.DecodeResolvedToolset(encoded)
	require.NoError(t, err)
	resolved, err := registrycontract.ResolveCompiled(owned)
	require.NoError(t, err)
	bindings := make(map[tools.Ident]*tools.RegistryBinding, len(names))
	for _, name := range names {
		binding, err := resolved.Select(testRegistryName, name)
		require.NoError(t, err)
		bindings[name] = binding
	}
	return bindings
}
