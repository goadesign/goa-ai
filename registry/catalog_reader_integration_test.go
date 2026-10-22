//go:build integration

package registry

// These tests use a Redis account that permits only catalog read commands.
// Successful reads and unchanged data prove inspection cannot repair or prune
// saved registrations, even when provider leases have already expired.

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestRedisCatalogReaderUsesOnlyReadCommands(t *testing.T) {
	ctx := t.Context()
	admin := getRedis(t)
	store := newRedisCatalogStore(admin, t.Name())
	catalog := newToolsetCatalog(store, newRedisTimeSource(admin))
	definition := testCatalogDefinition(t, testCatalogToolset("route", "tool", []string{"inspect"}))
	definition.identity = &CatalogIdentity{Scope: "owner", Name: "resource"}
	admitted, err := catalog.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	// Keep an expired lease in saved state. Inspection must return the record
	// without running the service's lease cleanup or resetting pong freshness.
	admitted.ProviderLeases[providerLeaseKey("provider", testIncarnationA)] = providerLease{ExpiresAtUnixMilli: 1}
	raw, err := marshalCatalogState(admitted)
	require.NoError(t, err)
	require.NoError(t, admin.HSet(ctx, store.state, toolsetCatalogKey("route"), raw).Err())
	native, err := (&Service{catalog: catalog, validator: newSchemaValidator()}).RegisterAgentToolsetWithIdentity(ctx,
		CatalogIdentity{Scope: "owner", Name: "native"}, testAgentToolset("revision/1"))
	require.NoError(t, err)

	username := "catalog-reader"
	require.NoError(t, admin.Do(ctx, "ACL", "SETUSER", username, "reset", "on", ">catalog-reader-test", "~*", "-@all",
		"+hello", "+auth", "+ping", "+client|setinfo", "+hkeys", "+hget", "+hexists", "+hstrlen", "+sismember", "+time", "+eval", "+evalsha").Err())
	t.Cleanup(func() { assert.NoError(t, admin.ACLDelUser(context.Background(), username).Err()) })
	options := *admin.Options()
	options.Username, options.Password = username, "catalog-reader-test"
	client := redis.NewClient(&options)
	t.Cleanup(func() { assert.NoError(t, client.Close()) })
	require.NoError(t, client.Ping(ctx).Err())
	require.Error(t, client.HSet(ctx, store.state, "forbidden", "write").Err())
	require.Error(t, client.Eval(ctx, `return redis.call("HSET", KEYS[1], "forbidden", "write")`, []string{store.state}).Err(),
		"Redis must also reject a write nested in Lua")
	before := catalogReaderRedisDigest(t, admin)

	reader, err := NewCatalogReader(client, t.Name())
	require.NoError(t, err)
	listed, err := reader.ListToolsets(ctx, &genregistry.ListToolsetsPayload{Tags: []string{"inspect"}})
	require.NoError(t, err)
	require.Len(t, listed.Toolsets, 1)
	assert.Equal(t, "route", listed.Toolsets[0].Name)
	bytes := int64(len(raw) + len(definition.raw))
	_, err = reader.ReadCatalogToolset(ctx, "route", bytes-1)
	require.ErrorIs(t, err, ErrCatalogReadBudget)
	got, err := reader.ReadCatalogToolset(ctx, "route", bytes)
	require.NoError(t, err)
	assert.Equal(t, *definition.identity, got.Identity)
	assert.Equal(t, admitted.RegistrationToken, got.Registration.RegistrationToken)
	assert.False(t, got.NativeAgent)
	assert.Nil(t, got.ServiceHealth)
	gotNative, err := reader.ReadCatalogToolset(ctx, native.Toolset.Name, 1<<20)
	require.NoError(t, err)
	assert.True(t, gotNative.NativeAgent)
	assert.Equal(t, native, gotNative.Registration)
	assert.Nil(t, gotNative.ServiceHealth)
	_, err = reader.ReadCatalogToolset(ctx, "absent", 1<<20)
	requireServiceErrorName(t, err, "not_found")

	// A fresh reader has no local validation cache, so it must recover the same
	// registration from the saved bytes without creating any durable state.
	restarted, err := NewCatalogReader(client, t.Name())
	require.NoError(t, err)
	recovered, err := restarted.ReadCatalogToolset(ctx, "route", bytes)
	require.NoError(t, err)
	assert.Equal(t, got, recovered)
	assert.Equal(t, before, catalogReaderRedisDigest(t, admin))
	assert.NoError(t, client.Ping(ctx).Err(), "the reader must leave its borrowed client open")
}

// catalogReaderRedisDigest hashes every test-owned key and its stored encoding
// so newly created keys and changes to provider state or definitions are visible.
func catalogReaderRedisDigest(t *testing.T, client *redis.Client) string {
	t.Helper()
	keys, err := client.Keys(t.Context(), "*").Result()
	require.NoError(t, err)
	sort.Strings(keys)
	digest := sha256.New()
	for _, key := range keys {
		raw, err := client.Dump(t.Context(), key).Bytes()
		require.NoError(t, err)
		_, err = fmt.Fprintf(digest, "%d:%s%d:", len(key), key, len(raw))
		require.NoError(t, err)
		_, err = digest.Write(raw)
		require.NoError(t, err)
	}
	return fmt.Sprintf("%x", digest.Sum(nil))
}
