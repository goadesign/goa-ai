//go:build integration

package registry

// Real Redis tests pin the storage split and the atomic renewal checks. Large
// schemas must not affect steady-state lease, health, or call-preparation reads.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

type (
	measuredCatalogStore struct {
		catalogStore
		definitionReads  int
		definitionWrites int
		stateBytes       int
	}
)

func TestRedisRenewalPreservesDrainingAndSettlement(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	definition := testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil))
	first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.NoError(t, catalog.RecordPong(ctx, definition.info.Name, "provider", testIncarnationA, first.RegistrationToken, first.HealthEpoch))

	// Both operations may win first. Every ordering must retain drain and the
	// longer settlement deadline after both complete.
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	wg.Go(func() {
		<-start
		errs <- catalog.DrainProvider(ctx, definition.info.Name, "provider", testIncarnationA, first.RegistrationToken, 5*time.Minute)
	})
	wg.Go(func() {
		<-start
		errs <- catalog.RenewProvider(ctx, definition.info.Name, "provider", testIncarnationA, first.RegistrationToken, time.Minute)
	})
	close(start)
	wg.Wait()
	require.NoError(t, <-errs)
	require.NoError(t, <-errs)
	draining, err := catalog.activeState(ctx, definition.info.Name)
	require.NoError(t, err)
	leaseKey := providerLeaseKey("provider", testIncarnationA)
	lease := draining.ProviderLeases[leaseKey]
	require.True(t, lease.Draining)
	require.Greater(t, lease.ExpiresAtUnixMilli, first.ProviderLeases[leaseKey].ExpiresAtUnixMilli)
	require.NoError(t, catalog.RenewProvider(ctx, definition.info.Name, "provider", testIncarnationA, first.RegistrationToken, time.Minute))
	renewed, err := catalog.activeState(ctx, definition.info.Name)
	require.NoError(t, err)
	assert.Equal(t, draining, renewed, "renewal must preserve a longer draining lease and its health state")
	_, err = catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errProviderLeaseLost)
}

func TestRedisRenewalChecksExpirationAtCommit(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	definition := testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil))
	first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	now, err := rdb.Time(ctx).Result()
	require.NoError(t, err)

	// The Go read sees a still-valid lease, but Redis sees expiration when the
	// conditional write runs. This models a stalled request crossing expiry.
	first.ProviderLeases[providerLeaseKey("provider", testIncarnationA)] = providerLease{ExpiresAtUnixMilli: now.Add(-time.Second).UnixMilli()}
	raw, err := marshalCatalogState(first)
	require.NoError(t, err)
	require.NoError(t, rdb.HSet(ctx, store.state, toolsetCatalogKey(definition.info.Name), raw).Err())
	catalog.clock = newTestTimeSource(now.Add(-2 * time.Second))
	err = catalog.RenewProvider(ctx, definition.info.Name, "provider", testIncarnationA, first.RegistrationToken, time.Minute)
	require.ErrorIs(t, err, errProviderLeaseLost)
	saved, err := rdb.HGet(ctx, store.state, toolsetCatalogKey(definition.info.Name)).Result()
	require.NoError(t, err)
	assert.Equal(t, raw, saved)
}

func TestRedisDrainCannotExtendAnExpiredLease(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	definition := testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil))
	first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	now, err := rdb.Time(ctx).Result()
	require.NoError(t, err)
	first.ProviderLeases[providerLeaseKey("provider", testIncarnationA)] = providerLease{ExpiresAtUnixMilli: now.Add(-time.Second).UnixMilli()}
	raw, err := marshalCatalogState(first)
	require.NoError(t, err)
	require.NoError(t, rdb.HSet(ctx, store.state, toolsetCatalogKey(definition.info.Name), raw).Err())
	catalog.clock = newTestTimeSource(now.Add(-2 * time.Second))
	require.NoError(t, catalog.DrainProvider(ctx, definition.info.Name, "provider", testIncarnationA, first.RegistrationToken, time.Minute))
	saved, err := rdb.HGet(ctx, store.state, toolsetCatalogKey(definition.info.Name)).Result()
	require.NoError(t, err)
	assert.Equal(t, raw, saved, "a delayed drain cannot restore expired settlement authority")
}

func TestRedisRetirementSurvivesCurrentRecordLoss(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	definition := testCatalogDefinition(t, testCatalogToolset("test.toolset", "test", nil))
	first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "old", testIncarnationA, time.Minute)
	require.NoError(t, err)
	require.NoError(t, catalog.ReleaseProvider(ctx, definition.info.Name, "old", testIncarnationA, first.RegistrationToken))
	second, err := catalog.Register(ctx, definition, testAdmissionRevisionB, "new", testIncarnationB, time.Minute)
	require.NoError(t, err)
	key := toolsetCatalogKey(definition.info.Name)
	require.NoError(t, rdb.HDel(ctx, store.state, key).Err())
	require.ErrorIs(t, catalog.RenewProvider(ctx, definition.info.Name, "new", testIncarnationB, second.RegistrationToken, time.Minute), errProviderLeaseLost)
	require.ErrorContains(t, catalog.validatePersistedEntries(ctx), "CATALOGINCOMPLETE")
	require.NoError(t, rdb.HDel(ctx, store.definitions, key).Err())
	_, err = catalog.Register(ctx, definition, testAdmissionRevisionA, "old", uuid.NewString(), time.Minute)
	require.ErrorIs(t, err, errAdmissionRetired)
}

func TestRedisWarmCatalogDoesNotTransferDefinitions(t *testing.T) {
	ctx := t.Context()
	rdb := getRedis(t)
	store := &measuredCatalogStore{catalogStore: newRedisCatalogStore(rdb, t.Name())}
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	schema, err := json.Marshal(map[string]any{
		"type":        "object",
		"description": strings.Repeat("schema evidence ", 32_768),
	})
	require.NoError(t, err)
	toolset := testCatalogToolset("test.toolset", "large generated contract", nil)
	toolset.Tools = []*genregistry.ToolSchema{{
		Name:                   "lookup",
		PayloadSchema:          schema,
		ExecutionPayloadSchema: schema,
		ResultSchema:           schema,
	}}
	definition := testCatalogDefinition(t, toolset)
	require.Greater(t, len(definition.raw), 1_000_000)
	first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)

	// A second registry process validates one complete definition on first use.
	other := newToolsetCatalog(store, newRedisTimeSource(rdb))
	_, err = other.ActiveRegistration(ctx, toolset.Name)
	require.NoError(t, err)
	require.Equal(t, 1, store.definitionReads)
	require.Equal(t, 1, store.definitionWrites)
	store.stateBytes = 0
	for range 10 {
		require.NoError(t, other.RenewProvider(ctx, toolset.Name, "provider", testIncarnationA, first.RegistrationToken, time.Minute))
		require.NoError(t, other.RecordPong(ctx, toolset.Name, "provider", testIncarnationA, first.RegistrationToken, first.HealthEpoch))
		_, _, err := other.healthEntry(ctx, toolset.Name)
		require.NoError(t, err)
		list, err := other.ListToolsets(ctx, nil)
		require.NoError(t, err)
		require.Len(t, list, 1)
		registration, err := other.ActiveRegistration(ctx, toolset.Name)
		require.NoError(t, err)
		require.NoError(t, validatePayload(registration.Toolset.executionSchemas["lookup"], []byte(`{}`)))
	}
	assert.Equal(t, 1, store.definitionReads, "warm lifecycle and call preparation must not fetch definitions")
	assert.Equal(t, 1, store.definitionWrites, "renewal and pong must not rewrite definitions")
	assert.Less(t, store.stateBytes, 100_000, "all state traffic remains smaller than one full definition")
	t.Logf("definition=%d bytes; 10 renewal/pong/health/list/call-preparation cycles transferred %d compact JSON bytes", len(definition.raw), store.stateBytes)
}

func TestRedisCatalogReportsBoundedOperationSizes(t *testing.T) {
	recorder := newHealthSpanRecorder(t)
	ctx := t.Context()
	rdb := getRedis(t)
	store := newRedisCatalogStore(rdb, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(rdb))
	definition := testCatalogDefinition(t, testCatalogToolset("test.toolset", "schema content stays private", nil))
	first, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	cold := newToolsetCatalog(store, newRedisTimeSource(rdb))
	_, err = cold.ActiveRegistration(ctx, definition.info.Name)
	require.NoError(t, err)
	require.NoError(t, cold.RenewProvider(ctx, definition.info.Name, "provider", testIncarnationA, first.RegistrationToken, time.Minute))
	seen := make(map[string]int)
	for _, span := range recorder.Ended() {
		seen[span.Name()]++
		attributes := healthSpanAttributes(span)
		assert.Equal(t, definition.info.Name, attributes["toolregistry.toolset"].AsString())
		switch span.Name() {
		case "toolregistry.catalog.definition.load":
			assert.EqualValues(t, len(definition.raw), attributes["toolregistry.catalog.definition_read_bytes"].AsInt64())
		case "toolregistry.catalog.state.update":
			assert.Positive(t, attributes["toolregistry.catalog.state_write_bytes"].AsInt64())
			assert.False(t, attributes["toolregistry.catalog.conditional_retry"].AsBool())
		case "toolregistry.catalog.state.read":
			// Initial absence has no value bytes. Subsequent reads report only
			// the compact state, never the full schemas.
		default:
			t.Errorf("unexpected catalog operation span %q", span.Name())
		}
		for key := range attributes {
			assert.NotContains(t, key, "payload")
			assert.NotContains(t, key, "schema")
		}
	}
	assert.Equal(t, 1, seen["toolregistry.catalog.definition.load"])
	assert.Equal(t, 2, seen["toolregistry.catalog.state.update"])
	assert.Equal(t, 3, seen["toolregistry.catalog.state.read"])
}

func (s *measuredCatalogStore) Read(ctx context.Context, key string) (string, bool, error) {
	raw, exists, err := s.catalogStore.Read(ctx, key)
	s.stateBytes += len(raw)
	return raw, exists, err
}

func (s *measuredCatalogStore) Snapshot(ctx context.Context, key string) (string, string, bool, bool, error) {
	s.definitionReads++
	return s.catalogStore.Snapshot(ctx, key)
}

func (s *measuredCatalogStore) Commit(ctx context.Context, key, previous string, next catalogWrite) (bool, error) {
	if next.Definition != "" {
		s.definitionWrites++
	}
	s.stateBytes += len(previous) + len(next.State)
	return s.catalogStore.Commit(ctx, key, previous, next)
}
