//go:build integration

// These tests check native declarations, retirement, and bounded owner-resource reads.
package registry

import (
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func policyTestService(t *testing.T) *Service {
	t.Helper()
	name := fmt.Sprintf("policy-%s", uuid.NewString())
	store := newRedisCatalogStore(testRedisClient, name)
	return &Service{catalog: newToolsetCatalog(store, newRedisTimeSource(testRedisClient)), validator: newSchemaValidator()}
}

func policyTestAgent(scope, resource, route string) (*genregistry.AgentExportsDeclaration, *genregistry.CatalogOperation) {
	base := testAgentToolset(route)
	declaration := &genregistry.AgentExportsDeclaration{Scope: scope, ResourceName: resource, Name: route, Exports: base.Tools}
	operation := &genregistry.CatalogOperation{Scope: scope, Name: resource, Target: genregistry.NewCatalogTargetNative(&genregistry.NativeCatalogTarget{Toolset: route, Tool: base.Tools[0].Name})}
	return declaration, operation
}

func TestRedisNativeRetirementPreservesRegisteredDeclaration(t *testing.T) {
	service := policyTestService(t)
	declaration, operation := policyTestAgent("scope-a", "resource", "revision-a")
	original, err := service.RegisterAgentExports(t.Context(), declaration)
	require.NoError(t, err)
	resource := operationResource(operation)
	require.NoError(t, service.CheckCatalogResource(t.Context(), resource))
	require.NoError(t, service.RetireAgentResource(t.Context(), resource))
	require.Error(t, service.CheckCatalogResource(t.Context(), resource))
	replay, err := service.RegisterAgentExports(t.Context(), declaration)
	require.NoError(t, err)
	assert.Equal(t, original, replay)
	next, _ := policyTestAgent("scope-a", "resource", "revision-b")
	_, err = service.RegisterAgentExports(t.Context(), next)
	require.Error(t, err)
	require.NoError(t, service.catalog.Retire(t.Context(), declaration.Name, original.RegistrationToken))
	_, err = service.RegisterAgentExports(t.Context(), declaration)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict")
}

func TestRedisNativeDeclarationRepeatRejectsChangedContent(t *testing.T) {
	service := policyTestService(t)
	declaration, _ := policyTestAgent("owner", "parent", "parent-v1")
	require.NoError(t, service.ValidateAgentExports(t.Context(), &genregistry.AgentExportsValidation{Scope: declaration.Scope, ResourceName: declaration.ResourceName, Exports: declaration.Exports}))
	original, err := service.RegisterAgentExports(t.Context(), declaration)
	require.NoError(t, err)
	repeated, err := service.RegisterAgentExports(t.Context(), declaration)
	require.NoError(t, err)
	assert.Equal(t, original, repeated)
	changed := "changed declaration"
	declaration.Exports[0].Description = &changed
	_, err = service.RegisterAgentExports(t.Context(), declaration)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "conflict")
}

func TestRedisResourcePagesBoundRetiredCandidatesAndBindOwner(t *testing.T) {
	service := policyTestService(t)
	for _, name := range []string{"a", "b", "c"} {
		declaration, operation := policyTestAgent("owner", name, "revision-"+name)
		_, err := service.RegisterAgentExports(t.Context(), declaration)
		require.NoError(t, err)
		if name == "a" {
			require.NoError(t, service.RetireAgentResource(t.Context(), operationResource(operation)))
		}
	}
	secondRevision, _ := policyTestAgent("owner", "b", "revision-b2")
	_, err := service.RegisterAgentExports(t.Context(), secondRevision)
	require.NoError(t, err)
	foreign, _ := policyTestAgent("other-owner", "foreign", "foreign-v1")
	_, err = service.RegisterAgentExports(t.Context(), foreign)
	require.NoError(t, err)
	query := &genregistry.CatalogResourcesQuery{Scope: "owner", Kind: "agent", Limit: 1}
	first, err := service.ListCatalogResources(t.Context(), query)
	require.NoError(t, err)
	assert.Empty(t, first.Resources)
	more, ok := first.Continuation.AsMore()
	require.True(t, ok)
	query.After = &more.After
	second, err := service.ListCatalogResources(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, second.Resources, 1)
	assert.Equal(t, "b", second.Resources[0].Name)
	next, ok := second.Continuation.AsMore()
	require.True(t, ok)
	query.After = &next.After
	third, err := service.ListCatalogResources(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, third.Resources, 1)
	assert.Equal(t, "c", third.Resources[0].Name)
	_, done := third.Continuation.AsDone()
	assert.True(t, done)
	query.Scope = "other-owner"
	_, err = service.ListCatalogResources(t.Context(), query)
	require.Error(t, err)
	query.After = nil
	other, err := service.ListCatalogResources(t.Context(), query)
	require.NoError(t, err)
	require.Len(t, other.Resources, 1)
	assert.Equal(t, "foreign", other.Resources[0].Name)
}

func TestRedisPermanentResourceRetirementAfterAllExactRoutesRetire(t *testing.T) {
	service := policyTestService(t)
	declaration, operation := policyTestAgent("scope-a", "resource", "revision-a")
	registration, err := service.RegisterAgentExports(t.Context(), declaration)
	require.NoError(t, err)
	require.NoError(t, service.catalog.Retire(t.Context(), declaration.Name, registration.RegistrationToken))
	require.NoError(t, service.RetireAgentResource(t.Context(), operationResource(operation)))
	require.NoError(t, service.RetireAgentResource(t.Context(), operationResource(operation)))
	next, _ := policyTestAgent("scope-a", "resource", "revision-b")
	_, err = service.RegisterAgentExports(t.Context(), next)
	require.Error(t, err)
	page, err := service.ListCatalogResources(t.Context(), &genregistry.CatalogResourcesQuery{Scope: "scope-a", Kind: "agent", Limit: 1})
	require.NoError(t, err)
	assert.Empty(t, page.Resources)
}

func operationParts(op *genregistry.CatalogOperation) (kind, route, tool, token string) {
	if target, ok := op.Target.AsNative(); ok {
		return "agent", target.Toolset, target.Tool, ""
	}
	target, _ := op.Target.AsService()
	return "service", target.Toolset, target.Tool, target.RegistrationToken
}

func operationResource(op *genregistry.CatalogOperation) *genregistry.CatalogResource {
	kind, _, _, _ := operationParts(op)
	return &genregistry.CatalogResource{Kind: kind, Scope: op.Scope, Name: op.Name}
}
