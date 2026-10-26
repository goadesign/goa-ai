//go:build integration

package registry

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	registryserver "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"google.golang.org/protobuf/proto"
)

func TestRedisBoundedReadKeepsDefinitionAdmissionIndependent(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	input := &genregistry.Toolset{Name: "product-route", Tools: testServiceDeclaration().Tools}
	description := strings.Repeat("<", 1024*1024)
	input.Tools[0].Description = &description
	definition := testCatalogDefinition(t, input)
	definition.identity = &CatalogIdentity{Scope: "product", Name: "public"}
	admitted, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err, "a read budget must never become registration admission")
	key := toolsetCatalogKey(input.Name)
	stateBytes, err := rdb.HStrLen(ctx, store.state, key).Result()
	require.NoError(t, err)
	pointerBytes, err := rdb.HStrLen(ctx, store.definitions, key).Result()
	require.NoError(t, err)
	require.Less(t, pointerBytes, int64(256))
	definitionBytes := int64(len(definition.raw))
	require.Greater(t, definitionBytes, int64(6*1024*1024))
	require.Less(t, proto.Size(registryserver.NewProtoGetToolsetResponse(input)), 4*1024*1024)
	total := stateBytes + definitionBytes
	svc := &Service{catalog: catalog, catalogHealth: defaultTestCatalogHealth}
	_, err = svc.ReadCatalogToolset(ctx, input.Name, total-1)
	require.ErrorIs(t, err, ErrCatalogReadBudget)
	got, err := svc.ReadCatalogToolset(ctx, input.Name, total)
	require.NoError(t, err)
	require.Equal(t, admitted.RegistrationToken, got.Registration.RegistrationToken)
	require.Equal(t, []byte(definition.raw), gotRegistrationBytes(t, store, key))
	require.NotNil(t, got.ServiceHealth)
	require.False(t, got.ServiceHealth.Healthy)
	require.NoError(t, catalog.RecordPong(ctx, input.Name, "provider", testIncarnationA, admitted.RegistrationToken, admitted.HealthEpoch))
	// A larger test-only read capacity accommodates the changed health timestamp.
	healthy, err := svc.ReadCatalogToolset(ctx, input.Name, total+1024)
	require.NoError(t, err)
	require.True(t, healthy.ServiceHealth.Healthy)
	require.Equal(t, []byte(definition.raw), gotRegistrationBytes(t, store, key))

	// Deliberately corrupt only test-owned state. A budget failure must happen
	// before Lua parses it; otherwise this returns a cjson error.
	require.NoError(t, rdb.HSet(ctx, store.state, key, "{invalid state").Err())
	_, err = store.BoundedSnapshot(ctx, key, 1)
	require.ErrorIs(t, err, ErrCatalogReadBudget)
	_, err = store.BoundedSnapshot(ctx, key, total+1024)
	require.Error(t, err)
	require.NotErrorIs(t, err, ErrCatalogReadBudget)
}

func gotRegistrationBytes(t *testing.T, store *redisCatalogStore, key string) []byte {
	t.Helper()
	_, raw, _, exists, err := store.Snapshot(t.Context(), key)
	require.NoError(t, err)
	require.True(t, exists)
	return []byte(raw)
}

func TestRedisContentAddressedDefinitionsDeduplicateAcrossRoutesAndForgetOnRetire(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	routes := []string{
		"workspace-a.search",
		"workspace-b.search",
		"workspace-c.search",
	}
	tokens := make(map[string]string, len(routes))
	for _, route := range routes {
		input := &genregistry.Toolset{
			Name:  route,
			Tools: testServiceDeclaration().Tools,
		}
		definition := testCatalogDefinition(t, input)
		definition.identity = &CatalogIdentity{Scope: "product", Name: route}
		admitted, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
		require.NoError(t, err)
		tokens[route] = admitted.RegistrationToken
	}

	fields, err := rdb.HKeys(ctx, store.definitions).Result()
	require.NoError(t, err)
	var bodyKeys, pointerKeys []string
	for _, field := range fields {
		switch {
		case strings.HasPrefix(field, catalogDefinitionBlobPrefix):
			bodyKeys = append(bodyKeys, field)
		case strings.HasPrefix(field, toolsetCatalogKeyPrefix):
			pointerKeys = append(pointerKeys, field)
		default:
			t.Fatalf("unexpected definitions field %q", field)
		}
	}
	require.Len(t, bodyKeys, 1, "identical tool schemas across routes must share one canonical body")
	require.Len(t, pointerKeys, len(routes))

	for _, route := range routes {
		got, err := catalog.GetToolset(ctx, route)
		require.NoError(t, err)
		require.Equal(t, route, got.Name)
		require.Len(t, got.Tools, len(testServiceDeclaration().Tools))
	}

	require.NoError(t, catalog.Retire(ctx, routes[0], tokens[routes[0]]))
	restarted := newToolsetCatalog(store, newRedisTimeSource(rdb))
	require.NoError(t, restarted.validatePersistedEntries(ctx))
	require.Nil(t, nativeCacheEntry(restarted, routes[0]), "startup validation must not retain retired definitions in memory")
	require.NotNil(t, nativeCacheEntry(restarted, routes[1]), "startup validation retains active definitions in memory")
}

func defaultTestCatalogHealth(entry catalogState, now time.Time) ToolsetHealth {
	tracker := &healthTracker{
		stalenessThreshold: deriveStalenessThreshold(DefaultPingInterval, DefaultMissedPingThreshold),
	}
	return tracker.healthFromEntry(entry, now)
}

func TestRedisIdentityIndexTracksRetirementAndRejectsStaleCommit(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	for _, route := range []string{"a", "b", "c"} {
		definition := testCatalogDefinition(t, testCatalogToolset(route, "tool", nil))
		definition.identity = &CatalogIdentity{Scope: "product", Name: route}
		_, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
		require.NoError(t, err)
	}
	routes, err := store.After(ctx, "product", "a", 2)
	require.NoError(t, err)
	require.Equal(t, []string{"b", "c"}, routes)
	state, err := catalog.activeState(ctx, "b")
	require.NoError(t, err)
	previous, exists, err := store.Read(ctx, toolsetCatalogKey("b"))
	require.NoError(t, err)
	require.True(t, exists)
	require.NoError(t, catalog.Retire(ctx, "b", state.RegistrationToken))
	member, err := store.Contains(ctx, "product", "b")
	require.NoError(t, err)
	require.False(t, member)
	committed, err := catalog.commit(ctx, toolsetCatalogKey("b"), previous, state, catalogWrite{})
	require.NoError(t, err)
	require.False(t, committed, "a stale active-state writer cannot reinsert membership")
	routes, err = store.After(ctx, "product", "b", 2)
	require.NoError(t, err)
	require.Equal(t, []string{"c"}, routes, "removed anchors remain valid exclusive positions")
	routes, err = store.After(ctx, "other", "", 2)
	require.NoError(t, err)
	require.Empty(t, routes)
}
