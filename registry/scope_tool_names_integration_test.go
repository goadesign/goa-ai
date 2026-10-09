//go:build integration

package registry

// Real Redis tests verify that the commit script checks and claims scope tool
// names in the same atomic write as the declaration. They inspect the saved
// tool-name hash and per-route claim set directly, so a later lifecycle step
// cannot pass while leaving stale or missing claims behind.

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	goa "goa.design/goa/v3/pkg"
)

func TestRedisScopedToolNameClaimsHaveOneConcurrentWinner(t *testing.T) {
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	svc := &Service{catalog: newToolsetCatalog(store, newRedisTimeSource(rdb)), validator: newSchemaValidator()}
	routes := []string{"r0", "r1", "r2", "r3", "r4", "r5", "r6", "r7"}

	// Every declaration uses a different route but the same tool name, and
	// all of them start together so their commits race inside Redis.
	start := make(chan struct{})
	errs := make([]error, len(routes))
	var wg sync.WaitGroup
	for i, route := range routes {
		wg.Go(func() {
			<-start
			_, errs[i] = declareScopedService(t, svc, "tenant-a", route, "inventory.lookup")
		})
	}
	close(start)
	wg.Wait()

	winner := ""
	for i, err := range errs {
		if err == nil {
			require.Empty(t, winner, "only one declaration may claim the tool name")
			winner = routes[i]
			continue
		}
		var serviceErr *goa.ServiceError
		require.ErrorAs(t, err, &serviceErr)
		assert.Equal(t, "tool_name_conflict", serviceErr.Name)
		saved, err := rdb.HExists(t.Context(), store.definitions, toolsetCatalogKey(routes[i])).Result()
		require.NoError(t, err)
		assert.False(t, saved, "a rejected declaration writes no definition")
		member, err := store.Contains(t.Context(), "tenant-a", routes[i])
		require.NoError(t, err)
		assert.False(t, member, "a rejected declaration joins no scope index")
	}
	require.NotEmpty(t, winner)
	assert.Equal(t, map[string]string{"inventory.lookup": winner}, scopeToolOwners(t, store, "tenant-a"))
}

func TestRedisScopedToolNameClaimsFollowLifecycle(t *testing.T) {
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	svc := &Service{catalog: catalog, validator: newSchemaValidator()}

	first, err := declareScopedService(t, svc, "tenant-a", "first", "inventory.lookup", "inventory.list")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"inventory.lookup": "first", "inventory.list": "first"}, scopeToolOwners(t, store, "tenant-a"))

	// Replacement releases the dropped name and claims the added one.
	first, err = replaceScopedService(t, svc, "first", first.RegistrationToken, "inventory.lookup", "inventory.count")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"inventory.lookup": "first", "inventory.count": "first"}, scopeToolOwners(t, store, "tenant-a"))
	assert.ElementsMatch(t, []string{"inventory.lookup", "inventory.count"}, routeToolClaims(t, store, "first"))

	// Provider attachment and renewal keep the declaration and its claims.
	_, err = catalog.AttachProvider(t.Context(), "first", first.RegistrationToken, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.NoError(t, catalog.RenewProvider(t.Context(), "first", "provider", testIncarnationA, first.RegistrationToken, time.Minute))
	assert.Equal(t, map[string]string{"inventory.lookup": "first", "inventory.count": "first"}, scopeToolOwners(t, store, "tenant-a"))

	// Retirement releases every name and removes the route's claim set.
	require.NoError(t, catalog.Retire(t.Context(), "first", first.RegistrationToken))
	assert.Empty(t, scopeToolOwners(t, store, "tenant-a"))
	assert.Empty(t, routeToolClaims(t, store, "first"))
	_, err = declareScopedService(t, svc, "tenant-a", "second", "inventory.lookup")
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"inventory.lookup": "second"}, scopeToolOwners(t, store, "tenant-a"))
}

func TestRedisStartupClaimsToolNamesOfSavedRecords(t *testing.T) {
	for _, test := range []struct {
		name  string
		saved func(*testing.T, *Service) error
	}{
		{
			name: "service declaration",
			saved: func(t *testing.T, svc *Service) error {
				_, err := declareScopedService(t, svc, "tenant-a", "first", "inventory.lookup")
				return err
			},
		},
		{
			name: "native Agent registration",
			saved: func(t *testing.T, svc *Service) error {
				_, err := registerScopedAgent(t, svc, "tenant-a", "first", "inventory.lookup")
				return err
			},
		},
		{
			name: "provider registration",
			saved: func(t *testing.T, svc *Service) error {
				declaration := scopedServiceDeclaration("first", "inventory.lookup")
				definition := testCatalogDefinition(t, &genregistry.Toolset{Name: declaration.Name, Tags: declaration.Tags, Tools: declaration.Tools})
				definition.identity = &CatalogIdentity{Scope: "tenant-a", Name: "first"}
				_, err := svc.catalog.Register(t.Context(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			rdb := getRedis(t)
			store := newRedisCatalogStore(rdb, t.Name())
			require.NoError(t, test.saved(t, newRedisScopeToolNamesService(store)))
			forgetToolNameClaims(t, store, "tenant-a", "first")

			restarted := newRedisScopeToolNamesService(store)
			require.NoError(t, restarted.catalog.validatePersistedEntries(t.Context()))
			assert.Equal(t, map[string]string{"inventory.lookup": "first"}, scopeToolOwners(t, store, "tenant-a"))
			_, err := declareScopedService(t, restarted, "tenant-a", "second", "inventory.lookup")
			requireServiceErrorName(t, err, "tool_name_conflict")
		})
	}
}

func TestRedisStartupRejectsSavedRecordsThatRepeatAToolName(t *testing.T) {
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	svc := newRedisScopeToolNamesService(store)
	_, err := declareScopedService(t, svc, "tenant-a", "first", "inventory.lookup")
	require.NoError(t, err)
	forgetToolNameClaims(t, store, "tenant-a", "first")
	_, err = declareScopedService(t, svc, "tenant-a", "second", "inventory.lookup")
	require.NoError(t, err, "without claims the older catalog accepted the repeated name")
	forgetToolNameClaims(t, store, "tenant-a", "second")

	err = newRedisScopeToolNamesService(store).catalog.validatePersistedEntries(t.Context())
	require.ErrorContains(t, err, `"inventory.lookup"`)
	require.ErrorContains(t, err, `"first"`)
	require.ErrorContains(t, err, `"second"`)
}

func TestRedisStartupReleasesStaleClaimsBeforeReportingConflicts(t *testing.T) {
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	svc := newRedisScopeToolNamesService(store)
	_, err := declareScopedService(t, svc, "tenant-a", "alpha", "inventory.lookup")
	require.NoError(t, err)
	forgetToolNameClaims(t, store, "tenant-a", "alpha")
	_, err = declareScopedService(t, svc, "tenant-a", "zeta", "inventory.count")
	require.NoError(t, err)

	// zeta still holds a claim on a name its saved declaration no longer
	// provides, as if an older registry replaced it without releasing names.
	// Startup visits alpha first, so alpha must be retried after zeta
	// releases the stale claim.
	require.NoError(t, rdb.HSet(t.Context(), store.scopeToolNames("tenant-a"), "inventory.lookup", "zeta").Err())
	require.NoError(t, rdb.SAdd(t.Context(), store.routeToolNames("zeta"), "inventory.lookup").Err())

	require.NoError(t, newRedisScopeToolNamesService(store).catalog.validatePersistedEntries(t.Context()))
	assert.Equal(t, map[string]string{"inventory.lookup": "alpha", "inventory.count": "zeta"}, scopeToolOwners(t, store, "tenant-a"))
}

// newRedisScopeToolNamesService returns a Service whose catalog uses store,
// like a registry replica sharing that Redis catalog.
func newRedisScopeToolNamesService(store *redisCatalogStore) *Service {
	return &Service{catalog: newToolsetCatalog(store, newRedisTimeSource(store.redis)), validator: newSchemaValidator()}
}

// forgetToolNameClaims deletes every claim the routes hold in scope, leaving
// the records as a registry without tool-name claims would have saved them.
func forgetToolNameClaims(t *testing.T, store *redisCatalogStore, scope string, routes ...string) {
	t.Helper()
	for _, route := range routes {
		for _, tool := range routeToolClaims(t, store, route) {
			require.NoError(t, store.redis.HDel(t.Context(), store.scopeToolNames(scope), tool).Err())
		}
		require.NoError(t, store.redis.Del(t.Context(), store.routeToolNames(route)).Err())
	}
}

// scopeToolOwners returns the saved map from tool name to owning route for scope.
func scopeToolOwners(t *testing.T, store *redisCatalogStore, scope string) map[string]string {
	t.Helper()
	owners, err := store.redis.HGetAll(t.Context(), store.scopeToolNames(scope)).Result()
	require.NoError(t, err)
	return owners
}

// routeToolClaims returns the saved tool names that route currently holds.
func routeToolClaims(t *testing.T, store *redisCatalogStore, route string) []string {
	t.Helper()
	claims, err := store.redis.SMembers(t.Context(), store.routeToolNames(route)).Result()
	require.NoError(t, err)
	return claims
}
