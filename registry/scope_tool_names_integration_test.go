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
