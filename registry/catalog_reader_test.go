package registry

// Local reader tests preserve saved-record errors and caller-owned results
// while checking that construction needs neither Redis access nor workers.

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestNewCatalogReaderBorrowsClientWithoutContactingRedis(t *testing.T) {
	reader, err := NewCatalogReader(nil, "catalog")
	require.Error(t, err)
	assert.Nil(t, reader)
	client := redis.NewClient(&redis.Options{Addr: "unused.invalid:6379"})
	require.NoError(t, client.Close())
	reader, err = NewCatalogReader(client, "")
	require.NoError(t, err, "construction must not contact Redis or start workers")
	store := reader.catalog.store.(*redisCatalogStore)
	assert.Same(t, client, store.redis)
	assert.Equal(t, catalogStateHashKey("registry"), store.state)
}

func TestCatalogReaderPreservesSavedReadErrors(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testCatalogMap, *catalogState)
		budget int64
		want   string
	}{
		{name: "missing", change: func(store *testCatalogMap, _ *catalogState) {
			delete(store.content, toolsetCatalogKey("route"))
			delete(store.definitions, toolsetCatalogKey("route"))
		}, budget: 1 << 20, want: "not_found"},
		{name: "retired", change: func(store *testCatalogMap, state *catalogState) {
			state.State = catalogEntryRetired
			store.retiredTokens[state.RegistrationToken] = struct{}{}
		}, budget: 1 << 20, want: "not_found"},
		{name: "unowned", change: func(_ *testCatalogMap, state *catalogState) {
			state.Identity = nil
		}, budget: 1 << 20, want: "identity"},
		{name: "malformed", change: func(store *testCatalogMap, _ *catalogState) {
			store.definitions[toolsetCatalogKey("route")] = "invalid"
		}, budget: 1 << 20, want: "error"},
		{name: "budget", budget: 1, want: "budget"},
		{name: "nonpositive budget", budget: 0, want: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			catalog := newToolsetCatalog(store, clock)
			definition := testCatalogDefinition(t, testCatalogToolset("route", "tool", nil))
			definition.identity = &CatalogIdentity{Scope: "owner", Name: "resource"}
			state, err := catalog.Register(t.Context(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
			require.NoError(t, err)
			if test.change != nil {
				test.change(store, &state)
				if test.name == "unowned" || test.name == "retired" {
					raw, err := marshalCatalogState(state)
					require.NoError(t, err)
					store.content[toolsetCatalogKey("route")] = raw
				}
			}
			reader := &CatalogReader{catalog: catalog}
			got, err := reader.ReadCatalogToolset(t.Context(), "route", test.budget)
			require.Error(t, err)
			assert.Nil(t, got)
			switch test.want {
			case "identity":
				require.ErrorIs(t, err, ErrCatalogIdentityMissing)
			case "budget":
				require.ErrorIs(t, err, ErrCatalogReadBudget)
			case "not_found":
				requireServiceErrorName(t, err, "not_found")
			}
		})
	}
}

func TestCatalogReaderReturnsIndependentDeclarationsAndCancellation(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	definition := testCatalogDefinition(t, &genregistry.Toolset{Name: "route", Tools: testServiceDeclaration().Tools})
	definition.identity = &CatalogIdentity{Scope: "owner", Name: "resource"}
	_, err := catalog.Register(t.Context(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	reader := &CatalogReader{catalog: catalog}
	first, err := reader.ReadCatalogToolset(t.Context(), "route", 1<<20)
	require.NoError(t, err)
	assert.Nil(t, first.ServiceHealth)
	first.Registration.Toolset.Tools[0].Name = "changed"
	second, err := reader.ReadCatalogToolset(t.Context(), "route", 1<<20)
	require.NoError(t, err)
	assert.Equal(t, definition.info.Name, second.Registration.Toolset.Name)
	assert.NotEqual(t, "changed", second.Registration.Toolset.Tools[0].Name)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = reader.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
	require.ErrorIs(t, err, context.Canceled)
	_, err = reader.ReadCatalogToolset(ctx, "route", 1<<20)
	require.ErrorIs(t, err, context.Canceled)
}
