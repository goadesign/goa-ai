// Scope tool-name tests verify that active toolsets sharing a catalog identity
// scope never provide the same tool name. Each case calls the public Service
// writers against the in-memory store and checks both the returned error and
// what remained saved.
package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestScopedToolNamesRejectDeclarationsThatRepeatAName(t *testing.T) {
	for _, test := range []struct {
		name          string
		firstScope    string
		secondScope   string
		secondAgent   bool
		secondTools   []string
		wantConflict  bool
		conflictTool  string
		conflictOwner string
	}{
		{
			name: "service repeats a service tool", firstScope: "tenant-a", secondScope: "tenant-a",
			secondTools: []string{"inventory.lookup"}, wantConflict: true,
			conflictTool: "inventory.lookup", conflictOwner: "first",
		},
		{
			name: "agent repeats a service tool", firstScope: "tenant-a", secondScope: "tenant-a",
			secondAgent: true, secondTools: []string{"inventory.list"}, wantConflict: true,
			conflictTool: "inventory.list", conflictOwner: "first",
		},
		{
			name: "different scope", firstScope: "tenant-a", secondScope: "tenant-b",
			secondTools: []string{"inventory.lookup", "inventory.list"},
		},
		{
			name: "different tool names", firstScope: "tenant-a", secondScope: "tenant-a",
			secondTools: []string{"orders.lookup"},
		},
		{
			name: "records without identity", secondTools: []string{"inventory.lookup"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			svc := newScopeToolNamesService()
			_, err := declareScopedService(t, svc, test.firstScope, "first", "inventory.lookup", "inventory.list")
			require.NoError(t, err)

			if test.secondAgent {
				_, err = registerScopedAgent(t, svc, test.secondScope, "second", test.secondTools[0])
			} else {
				_, err = declareScopedService(t, svc, test.secondScope, "second", test.secondTools...)
			}

			if !test.wantConflict {
				require.NoError(t, err)
				return
			}
			requireServiceErrorName(t, err, "tool_name_conflict")
			require.ErrorContains(t, err, test.conflictTool)
			require.ErrorContains(t, err, test.conflictOwner)
			_, err = svc.catalog.snapshot(t.Context(), "second")
			assert.ErrorIs(t, err, errToolsetNotFound, "a rejected declaration saves nothing")
		})
	}
}

func TestScopedToolNamesFollowReplacementAndRetirement(t *testing.T) {
	t.Parallel()
	svc := newScopeToolNamesService()
	first, err := declareScopedService(t, svc, "tenant-a", "first", "inventory.lookup", "inventory.list")
	require.NoError(t, err)
	second, err := declareScopedService(t, svc, "tenant-a", "second", "orders.lookup")
	require.NoError(t, err)

	// Repeating an identical declaration returns the saved registration
	// instead of conflicting with the names it already holds.
	again, err := declareScopedService(t, svc, "tenant-a", "second", "orders.lookup")
	require.NoError(t, err)
	assert.Equal(t, second.RegistrationToken, again.RegistrationToken)

	// A replacement cannot take a name another toolset provides, and the
	// rejected replacement leaves the current registration unchanged.
	_, err = replaceScopedService(t, svc, "second", second.RegistrationToken, "orders.lookup", "inventory.lookup")
	requireServiceErrorName(t, err, "tool_name_conflict")
	current, err := svc.catalog.snapshot(t.Context(), "second")
	require.NoError(t, err)
	assert.Equal(t, second.RegistrationToken, current.RegistrationToken)

	// A toolset keeps its own names across a replacement, and names it drops
	// become available to other toolsets in the scope.
	first, err = replaceScopedService(t, svc, "first", first.RegistrationToken, "inventory.lookup")
	require.NoError(t, err)
	second, err = replaceScopedService(t, svc, "second", second.RegistrationToken, "orders.lookup", "inventory.list")
	require.NoError(t, err)

	// Retirement releases every name of the retired toolset.
	retiredToken := first.RegistrationToken
	require.NoError(t, svc.catalog.Retire(t.Context(), "first", retiredToken))
	_, err = replaceScopedService(t, svc, "second", second.RegistrationToken, "orders.lookup", "inventory.list", "inventory.lookup")
	require.NoError(t, err)

	// Reactivating the retired toolset must claim its names again, so it is
	// rejected while another toolset provides one of them.
	_, err = replaceScopedService(t, svc, "first", retiredToken, "inventory.lookup")
	requireServiceErrorName(t, err, "tool_name_conflict")
	retired, err := svc.catalog.snapshot(t.Context(), "first")
	require.NoError(t, err)
	assert.Equal(t, catalogEntryRetired, retired.State)
}

func TestScopedToolNamesApplyToAgentReplacement(t *testing.T) {
	t.Parallel()
	svc := newScopeToolNamesService()
	_, err := declareScopedService(t, svc, "tenant-a", "inventory", "inventory.lookup")
	require.NoError(t, err)
	agentToolset, err := registerScopedAgent(t, svc, "tenant-a", "installation", "installation.check")
	require.NoError(t, err)

	replacement := scopedAgentDeclaration("installation", "inventory.lookup")
	_, err = svc.ReplaceAgentToolsetWithIdentity(t.Context(), CatalogIdentity{Scope: "tenant-a", Name: "installation"}, &genregistry.ReplaceAgentToolsetPayload{
		Name: replacement.Name, Tools: replacement.Tools, ExpectedRegistrationToken: agentToolset.RegistrationToken,
	})
	requireServiceErrorName(t, err, "tool_name_conflict")
	assert.ErrorContains(t, err, `"inventory"`)
}

// newScopeToolNamesService returns a Service whose catalog uses the in-memory
// store, so each test starts with an empty catalog.
func newScopeToolNamesService() *Service {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	return &Service{catalog: newToolsetCatalog(newTestCatalogMap(clock), clock), validator: newSchemaValidator()}
}

// declareScopedService declares route with the given tool names. An empty
// scope declares the toolset without a catalog identity.
func declareScopedService(t *testing.T, svc *Service, scope, route string, tools ...string) (*genregistry.ResolvedToolset, error) {
	t.Helper()
	declaration := scopedServiceDeclaration(route, tools...)
	if scope == "" {
		return svc.DeclareServiceToolset(t.Context(), declaration)
	}
	return svc.DeclareServiceToolsetWithIdentity(t.Context(), CatalogIdentity{Scope: scope, Name: route}, declaration)
}

// replaceScopedService replaces the declaration of route in scope "tenant-a"
// with the given tool names.
func replaceScopedService(t *testing.T, svc *Service, route, expected string, tools ...string) (*genregistry.ResolvedToolset, error) {
	t.Helper()
	request := serviceReplacementRequest(scopedServiceDeclaration(route, tools...), expected)
	return svc.ReplaceServiceToolsetWithIdentity(t.Context(), CatalogIdentity{Scope: "tenant-a", Name: route}, request)
}

// registerScopedAgent registers a native Agent toolset at route with one tool.
func registerScopedAgent(t *testing.T, svc *Service, scope, route, tool string) (*genregistry.ResolvedToolset, error) {
	t.Helper()
	return svc.RegisterAgentToolsetWithIdentity(t.Context(), CatalogIdentity{Scope: scope, Name: route}, scopedAgentDeclaration(route, tool))
}

// scopedServiceDeclaration copies the standard test service declaration with
// a different route and one tool per name.
func scopedServiceDeclaration(route string, tools ...string) *genregistry.ServiceToolsetDeclaration {
	template := testServiceDeclaration()
	declaration := &genregistry.ServiceToolsetDeclaration{Name: route, Tags: template.Tags}
	for _, name := range tools {
		tool := *template.Tools[0]
		tool.Name = name
		declaration.Tools = append(declaration.Tools, &tool)
	}
	return declaration
}

// scopedAgentDeclaration copies the standard test Agent declaration with a
// different route and tool name.
func scopedAgentDeclaration(route, tool string) *genregistry.AgentToolsetDeclaration {
	declaration := testAgentToolset("revision-1")
	declaration.Name = route
	declaration.Tools[0].Name = tool
	return declaration
}
