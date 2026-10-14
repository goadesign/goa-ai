// Registered source traversal must preserve the exact selected provider while giving
// each application callback an independent value it can safely retain or edit.
package runtime

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestRegisteredSourcesDistinctIdentityAndOrder(t *testing.T) {
	rt := New(newTestStore())
	for _, registry := range []string{testRegistryName, "another-registry"} {
		require.NoError(t, rt.RegisterRegistry(registry, &genregistry.Client{}, unusedRegistryPulse{}))
	}
	catalog := rt.newRegistryCatalog(testRegistryAgentDefinition(consumerTwoRegistrySources{}))
	first := consumerResource("provider")
	other := *first.Resolved.Toolset.Tools[0]
	other.Name = "records.other"
	first.Resolved.Toolset.Tools = append(first.Resolved.Toolset.Tools, &other)
	require.NoError(t, catalog.IncludeRegistered(testRegistryName, first.Source, first.Resolved, true))
	for _, difference := range []string{"scope", "resource", "native", "registry"} {
		item := consumerResource("provider")
		registry := testRegistryName
		switch difference {
		case "scope":
			item.Source.Scope = "another-owner"
		case "resource":
			item.Source.Name = "another-resource"
		case "native":
			item.Resolved.Toolset.Tools[0].ConsumerContract.Kind = registryAgentKind
			item.Resolved.Toolset.Tools[0].ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "records.agent", Configuration: "revision/7", Workflow: "records.workflow", Queue: "records.queue"}
			item.Source.Target = genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: item.Resolved.Toolset.Name})
		case "registry":
			registry = "another-registry"
		}
		require.NoError(t, catalog.IncludeRegistered(registry, item.Source, item.Resolved, true))
	}
	var keys []string
	read := func() error {
		return catalog.ForEachRegisteredSource(func(registry string, source *genregistry.CatalogSource) error {
			encoded, err := genregistry.EncodeCatalogSource(source)
			if err != nil {
				return err
			}
			keys = append(keys, registry+":"+string(encoded))
			return nil
		})
	}
	require.NoError(t, read())
	require.Len(t, keys, 5)
	firstOrder := append([]string(nil), keys...)
	keys = nil
	require.NoError(t, read())
	assert.Equal(t, firstOrder, keys)
	assert.Contains(t, strings.Join(keys, "\n"), "another-owner")
	assert.Contains(t, strings.Join(keys, "\n"), "another-resource")
	assert.Contains(t, strings.Join(keys, "\n"), "another-registry:")
	assert.Contains(t, strings.Join(keys, "\n"), `"native"`)
	sentinel := errors.New("stop binding")
	calls := 0
	assert.ErrorIs(t, catalog.ForEachRegisteredSource(func(string, *genregistry.CatalogSource) error { calls++; return sentinel }), sentinel) //nolint:testifylint // The callback count and nil-callback rejection are checked independently of this error.
	assert.Equal(t, 1, calls)
	assert.Error(t, catalog.ForEachRegisteredSource(nil))
}

func TestRegisteredSourcesKeepSelectedSnapshotAndConcurrentCopies(t *testing.T) {
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	original := consumerResource("records.alias")
	definition := testRegistryAgentDefinition(registeredTestSource{[]*genregistry.RegisteredToolsetSelection{original}})
	first, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	original.Resolved.RegistrationToken = strings.Repeat("d", 64)
	original.Source.Target = genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: original.Resolved.Toolset.Name, RegistrationToken: original.Resolved.RegistrationToken})
	later, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	var work sync.WaitGroup
	for _, catalog := range []*RegistryCatalog{first, later} {
		work.Go(func() {
			expected := strings.Repeat("a", 64)
			if catalog == later {
				expected = strings.Repeat("d", 64)
			}
			for range 3 {
				err := catalog.ForEachRegisteredSource(func(registry string, source *genregistry.CatalogSource) error {
					assert.Equal(t, testRegistryName, registry)
					service, ok := source.Target.AsService()
					assert.True(t, ok)
					assert.Equal(t, expected, service.RegistrationToken)
					source.Scope = "callback edit"
					service.RegistrationToken = "callback edit"
					return nil
				})
				assert.NoError(t, err)
			}
		})
	}
	work.Wait()
}

func TestRegisteredSourcesSkipDirectAndStaticTools(t *testing.T) {
	rt := New(newTestStore())
	require.NoError(t, rt.RegisterRegistry(testRegistryName, &genregistry.Client{}, unusedRegistryPulse{}))
	registered := testRuntimeRegistryResolution("records.find", strings.Repeat("a", 64))
	definition := testRegistryAgentDefinition(resolvedTestSource{registered, testRegistryName, true})
	catalog, err := rt.resolveRegistryCatalog(t.Context(), definition)
	require.NoError(t, err)
	require.NoError(t, catalog.ForEachRegisteredSource(func(string, *genregistry.CatalogSource) error {
		t.Error("direct registration is not a resource")
		return nil
	}))
	empty := rt.newRegistryCatalog(definition)
	require.NoError(t, empty.ForEachRegisteredSource(func(string, *genregistry.CatalogSource) error {
		t.Error("empty catalog has no resource")
		return nil
	}))
}
