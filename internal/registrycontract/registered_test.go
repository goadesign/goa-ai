// These tests preserve owner identity, original operations and paging when a
// selected call is saved and later read without consulting a current catalog.
package registrycontract

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/tools"
)

func TestRegisteredIdentityDistinguishesOwnersResourcesKindsAndRegistries(t *testing.T) {
	seen := make(map[tools.Ident]bool)
	for _, item := range []struct{ registry, owner, resource, kind string }{
		{"catalog", "owner", "records", "service"},
		{"catalog", "another-owner", "records", "service"},
		{"catalog", "owner", "another-resource", "service"},
		{"catalog", "owner", "records", "native"},
		{"another-registry", "owner", "records", "service"},
		{"catalog", "owner_records", "other", "service"},
		{"catalog", "owner", "records_other", "service"},
	} {
		t.Run(item.registry+"/"+item.owner+"/"+item.resource+"/"+item.kind, func(t *testing.T) {
			declaration := testDeclaration()
			declaration.ConsumerContract.Bounds = nil
			registered := testResolution(declaration)
			source := registeredServiceSource(registered, item.owner, item.resource)
			if item.kind == "native" {
				source.Target = genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: registered.Toolset.Name})
				declaration.ConsumerContract.Kind = "agent"
				declaration.ConsumerContract.Agent = &genregistry.AgentToolTarget{Executor: "records.agent", Configuration: "revision/1", Workflow: "records.workflow", Queue: "records.queue"}
			}
			resolved, err := ResolveRegistered(item.registry, source, registered)
			require.NoError(t, err)
			require.Len(t, resolved.Specs, 1)
			for name := range resolved.Specs {
				assert.False(t, seen[name], "distinct identity must not shadow another source")
				seen[name] = true
				assert.Equal(t, "change", name.Tool())
				assert.True(t, strings.HasPrefix(string(name), "records."))
				binding, err := resolved.Select(item.registry, name)
				require.NoError(t, err)
				replayed, err := Read(binding)
				require.NoError(t, err)
				original, err := replayed.Declaration(name)
				require.NoError(t, err)
				assert.Equal(t, "records.change", original.Name)
				assert.Equal(t, item.owner, replayed.Source.Scope)
				assert.Equal(t, item.resource, replayed.Source.Name)
			}
		})
	}
}

func TestRegisteredPagingRetainsOriginalPairAcrossReplacement(t *testing.T) {
	query := testDeclaration()
	next := testDeclaration()
	next.Name = continuationToolName
	next.ConsumerContract.Bounds.Paging.SourceTool = &query.Name
	registered := testResolution(query, next)
	source := registeredServiceSource(registered, "owner", "records")
	resolved, err := ResolveRegistered("catalog", source, registered)
	require.NoError(t, err)
	queryName := registeredName("catalog", source, tools.Ident(query.Name))
	nextName := registeredName("catalog", source, tools.Ident(next.Name))
	assert.Equal(t, nextName, resolved.Specs[queryName].Bounds.Paging.ContinueTool)
	assert.Equal(t, queryName, resolved.Specs[nextName].Bounds.Paging.SourceTool)
	binding, err := resolved.Select("catalog", queryName)
	require.NoError(t, err)
	registered.RegistrationToken = strings.Repeat("b", 64)
	registered.Toolset.Name = "replacement-route"
	source.Target = genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: registered.Toolset.Name, RegistrationToken: registered.RegistrationToken})
	fresh, err := ResolveRegistered("catalog", source, registered)
	require.NoError(t, err)
	assert.Contains(t, fresh.Specs, queryName)
	assert.Contains(t, fresh.Specs, nextName)
	replayed, err := Read(binding)
	require.NoError(t, err)
	assert.Equal(t, strings.Repeat("a", 64), replayed.Registered.RegistrationToken)
	assert.Equal(t, "provider.records", replayed.Registered.Toolset.Name)
	continued, err := replayed.Select("catalog", nextName)
	require.NoError(t, err)
	final, err := Read(continued)
	require.NoError(t, err)
	assert.Equal(t, nextName, final.Specs[queryName].Bounds.Paging.ContinueTool)
	original, err := final.Declaration(nextName)
	require.NoError(t, err)
	assert.Equal(t, continuationToolName, original.Name)
	assert.Equal(t, "records.change", *original.ConsumerContract.Bounds.Paging.SourceTool)
}

func TestRegisteredSourceRejectsDifferentTargetAndMissingIdentity(t *testing.T) {
	for _, change := range []string{"route", "token", "kind", "owner", "resource", "registry"} {
		t.Run(change, func(t *testing.T) {
			declaration := testDeclaration()
			declaration.ConsumerContract.Bounds = nil
			registered := testResolution(declaration)
			source := registeredServiceSource(registered, "owner", "records")
			registry := "catalog"
			switch change {
			case "route":
				source.Target = genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: "another-route", RegistrationToken: registered.RegistrationToken})
			case "token":
				source.Target = genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: registered.Toolset.Name, RegistrationToken: strings.Repeat("b", 64)})
			case "kind":
				source.Target = genregistry.NewCatalogSourceTargetNative(&genregistry.NativeCatalogSource{Toolset: registered.Toolset.Name})
			case "owner":
				source.Scope = ""
			case "resource":
				source.Name = ""
			case "registry":
				registry = ""
			}
			_, err := ResolveRegistered(registry, source, registered)
			require.Error(t, err)
		})
	}
}

func TestDirectSelectionRetainsOriginalName(t *testing.T) {
	declaration := testDeclaration()
	declaration.ConsumerContract.Bounds = nil
	resolved, err := Resolve(testResolution(declaration))
	require.NoError(t, err)
	binding, err := resolved.Select("catalog", tools.Ident(declaration.Name))
	require.NoError(t, err)
	selected, err := genregistry.DecodeRegistrySelection(binding.Resolution)
	require.NoError(t, err)
	_, direct := selected.Selection.AsDirect()
	assert.True(t, direct)
	replayed, err := Read(binding)
	require.NoError(t, err)
	assert.Nil(t, replayed.Source)
	assert.Contains(t, replayed.Specs, tools.Ident(declaration.Name))
}

func registeredServiceSource(registered *genregistry.ResolvedToolset, owner, resource string) *genregistry.CatalogSource {
	return &genregistry.CatalogSource{Scope: owner, Name: resource,
		Target: genregistry.NewCatalogSourceTargetService(&genregistry.ServiceCatalogSource{Toolset: registered.Toolset.Name, RegistrationToken: registered.RegistrationToken}),
	}
}
