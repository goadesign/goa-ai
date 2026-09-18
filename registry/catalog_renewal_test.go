// These tests verify that renewal extends one existing provider lease without
// recreating lost authority, changing definitions, or undoing shutdown.
package registry

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestCatalogRenewProviderExtendsOnlyExactLease(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	catalog, store, clock := testDefinitionCatalog(t)
	first, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	_, err = catalog.Register(ctx, testCatalogDefinition(t, testDefinitionToolset()), testAdmissionRevisionA,
		"provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, first.HealthEpoch))
	before, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	clock.Set(time.Unix(1_700_000_010, 0))

	require.NoError(t, catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, time.Minute))

	after, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	before.ProviderLeases[providerLeaseKey("provider", testIncarnationA)] = providerLease{
		ExpiresAtUnixMilli: time.Unix(1_700_000_070, 0).UnixMilli(),
	}
	assert.Equal(t, before, after, "only the exact lease deadline may change")
	assert.Equal(t, 1, store.definitionWrites)
	assert.Zero(t, store.snapshotReads)
}

func TestCatalogRenewProviderRejectsLostAuthority(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"missing state", "missing toolset", "missing provider", "different incarnation",
		"expired lease", "released lease", "retired admission", "replaced admission", "stale token", "different wire token",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			catalog, store, clock := testDefinitionCatalog(t)
			first, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			toolset, provider, incarnation, token := "tools", "provider", testIncarnationA, first.RegistrationToken
			switch name {
			case "missing state":
				store.mu.Lock()
				delete(store.content, toolsetCatalogKey("tools"))
				store.mu.Unlock()
			case "missing toolset":
				toolset = "missing"
			case "missing provider":
				provider = "missing"
			case "different incarnation":
				incarnation = testIncarnationB
			case "expired lease":
				clock.Set(time.Unix(1_700_000_060, 0))
			case "released lease":
				require.NoError(t, catalog.ReleaseProvider(ctx, "tools", provider, incarnation, token))
			case "retired admission":
				require.NoError(t, catalog.Retire(ctx, "tools", token))
			case "replaced admission":
				require.NoError(t, catalog.ReleaseProvider(ctx, "tools", provider, incarnation, token))
				_, err := catalog.Register(ctx, testCatalogDefinition(t, testDefinitionToolset()),
					testAdmissionRevisionB, provider, testIncarnationB, time.Minute)
				require.NoError(t, err)
			case "stale token":
				token = testStaleToken
			case "different wire token":
				token, err = admissionRegistrationToken(first.SchemaFingerprint, testAdmissionRevisionA, toolregistry.WireProtocolVersion+1)
				require.NoError(t, err)
			}
			before, beforeExists := store.Get(toolsetCatalogKey("tools"))
			writes := store.definitionWrites

			err = catalog.RenewProvider(ctx, toolset, provider, incarnation, token, time.Minute)

			require.ErrorIs(t, err, errProviderLeaseLost)
			after, afterExists := store.Get(toolsetCatalogKey("tools"))
			assert.Equal(t, beforeExists, afterExists)
			assert.Equal(t, before, after, "failed renewal must not recreate or change authority")
			assert.Equal(t, writes, store.definitionWrites)
			assert.Zero(t, store.snapshotReads)
		})
	}
}

func TestCatalogRenewProviderPreservesDrainingDeadline(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name               string
		drain, renew, want time.Duration
	}{
		{"keep longer drain", 2 * time.Minute, time.Minute, 2 * time.Minute},
		{"extend draining lease", time.Minute, 2 * time.Minute, 2 * time.Minute},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			catalog, store, _ := testDefinitionCatalog(t)
			first, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			require.NoError(t, catalog.DrainProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, tc.drain))
			drained, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)

			require.NoError(t, catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, tc.renew))

			renewed, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			drained.ProviderLeases[providerLeaseKey("provider", testIncarnationA)] = providerLease{
				Draining:           true,
				ExpiresAtUnixMilli: time.Unix(1_700_000_000, 0).Add(tc.want).UnixMilli(),
			}
			assert.Equal(t, drained, renewed)
			assert.Zero(t, routableProviderCount(renewed, time.Unix(1_700_000_000, 0)))
			assert.Zero(t, store.snapshotReads)
			assert.Equal(t, 1, store.definitionWrites)
		})
	}
}

func TestCatalogRenewProviderRetriesAfterConcurrentDrain(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	catalog, store, _ := testDefinitionCatalog(t)
	first, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	store.mu.Lock()
	store.beforeCommit = func(_ string, write catalogWrite) {
		require.NotEmpty(t, write.LiveLease)
		store.mu.Lock()
		store.beforeCommit = nil
		store.mu.Unlock()
		require.NoError(t, catalog.DrainProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, 2*time.Minute))
	}
	store.mu.Unlock()

	require.NoError(t, catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, time.Minute))

	current, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	assert.True(t, current.ProviderLeases[providerLeaseKey("provider", testIncarnationA)].Draining)
	assert.Equal(t, time.Unix(1_700_000_120, 0).UnixMilli(), current.ProviderLeases[providerLeaseKey("provider", testIncarnationA)].ExpiresAtUnixMilli)
	assert.Equal(t, first.HealthEpoch+1, current.HealthEpoch)
}

func TestCatalogFullRegisterCannotUndoDrain(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"already draining", "drain before registration commit", "expired draining lease"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			catalog, store, clock := testDefinitionCatalog(t)
			first, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			drain := func() {
				require.NoError(t, catalog.DrainProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, 2*time.Minute))
			}
			if name == "drain before registration commit" {
				store.mu.Lock()
				store.beforeCommit = func(string, catalogWrite) {
					store.mu.Lock()
					store.beforeCommit = nil
					store.mu.Unlock()
					drain()
				}
				store.mu.Unlock()
			} else {
				drain()
			}
			if name == "expired draining lease" {
				clock.Set(time.Unix(1_700_000_120, 0))
			}

			_, err = catalog.Register(ctx, testCatalogDefinition(t, testDefinitionToolset()), testAdmissionRevisionA,
				"provider", testIncarnationA, time.Minute)

			require.ErrorIs(t, err, errProviderLeaseLost)
			current, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			assert.True(t, current.ProviderLeases[providerLeaseKey("provider", testIncarnationA)].Draining)
			assert.Equal(t, time.Unix(1_700_000_120, 0).UnixMilli(), current.ProviderLeases[providerLeaseKey("provider", testIncarnationA)].ExpiresAtUnixMilli)

			fresh, err := catalog.Register(ctx, testCatalogDefinition(t, testDefinitionToolset()), testAdmissionRevisionA,
				"provider", testIncarnationB, time.Minute)
			require.NoError(t, err)
			assert.Equal(t, first.RegistrationToken, fresh.RegistrationToken)
			assert.False(t, fresh.ProviderLeases[providerLeaseKey("provider", testIncarnationB)].Draining)
			assert.Equal(t, 1, store.definitionWrites)
		})
	}
}

func TestCatalogReplacementWaitsForDrainingLease(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	catalog, _, clock := testDefinitionCatalog(t)
	old, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	require.NoError(t, catalog.DrainProvider(ctx, "tools", "provider", testIncarnationA, old.RegistrationToken, 2*time.Minute))
	definition := testCatalogDefinition(t, testDefinitionToolset())
	clock.Set(time.Unix(1_700_000_060, 0))
	_, err = catalog.Register(ctx, definition, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
	require.ErrorIs(t, err, errAdmissionBlocked)
	clock.Set(time.Unix(1_700_000_120, 0))
	next, err := catalog.Register(ctx, definition, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, old.RegistrationToken, next.RegistrationToken)
	assert.Len(t, next.ProviderLeases, 1)
}

func TestCatalogOldRenewalCannotOverwriteReplacement(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	catalog, store, clock := testDefinitionCatalog(t)
	old, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	definition := testCatalogDefinition(t, testDefinitionToolset())
	var replacement catalogState
	store.mu.Lock()
	store.beforeCommit = func(_ string, write catalogWrite) {
		require.NotEmpty(t, write.LiveLease)
		store.mu.Lock()
		store.beforeCommit = nil
		store.mu.Unlock()
		clock.Set(time.Unix(1_700_000_060, 0))
		var registerErr error
		replacement, registerErr = catalog.Register(ctx, definition, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
		require.NoError(t, registerErr)
	}
	store.mu.Unlock()

	err = catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, old.RegistrationToken, time.Minute)

	require.ErrorIs(t, err, errProviderLeaseLost)
	current, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	assert.Equal(t, replacement, current)
	retired, err := store.Retired(ctx, old.RegistrationToken)
	require.NoError(t, err)
	assert.True(t, retired)
}

func TestCatalogRenewProviderRechecksExpiryAtCommit(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	catalog, store, clock := testDefinitionCatalog(t)
	first, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	before, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	store.mu.Lock()
	store.beforeCommit = func(string, catalogWrite) {
		clock.Set(time.Unix(1_700_000_060, 0))
	}
	store.mu.Unlock()

	err = catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, 2*time.Minute)

	require.ErrorIs(t, err, errProviderLeaseLost)
	after, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	assert.Equal(t, before, after)
}

func TestCatalogDrainProviderRechecksExpiryAtCommit(t *testing.T) {
	t.Parallel()
	for _, status := range []string{"routable lease", "already draining lease"} {
		t.Run(status, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name   string
				offset time.Duration
			}{
				{"before expiry", -time.Millisecond},
				{"at expiry", 0},
				{"after expiry", time.Millisecond},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					ctx := t.Context()
					catalog, store, clock := testDefinitionCatalog(t)
					first, err := catalog.activeState(ctx, "tools")
					require.NoError(t, err)
					if status == "already draining lease" {
						require.NoError(t, catalog.DrainProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, 2*time.Minute))
					}
					current, err := catalog.activeState(ctx, "tools")
					require.NoError(t, err)
					leaseKey := providerLeaseKey("provider", testIncarnationA)
					expiry := time.UnixMilli(current.ProviderLeases[leaseKey].ExpiresAtUnixMilli)
					readTime := expiry.Add(-2 * time.Millisecond)
					clock.Set(readTime)
					before, exists := store.Get(toolsetCatalogKey("tools"))
					require.True(t, exists)
					commitAttempted := false
					store.mu.Lock()
					store.beforeCommit = func(_ string, write catalogWrite) {
						assert.Equal(t, leaseKey, write.LiveLease)
						clock.Set(expiry.Add(tc.offset))
						commitAttempted = true
					}
					store.mu.Unlock()

					require.NoError(t, catalog.DrainProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, 2*time.Minute))

					require.True(t, commitAttempted)
					after, exists := store.Get(toolsetCatalogKey("tools"))
					require.True(t, exists)
					if tc.offset >= 0 {
						assert.Equal(t, before, after, "drain must not restore a lease that expired before commit")
					} else {
						drained, err := parseCatalogState("tools", after)
						require.NoError(t, err)
						lease := current.ProviderLeases[leaseKey]
						lease.Draining = true
						lease.ExpiresAtUnixMilli = readTime.Add(2 * time.Minute).UnixMilli()
						current.ProviderLeases[leaseKey] = lease
						if status == "routable lease" {
							current.HealthEpoch++
							current.LastPongUnixNano = 0
						}
						assert.Equal(t, current, drained)
					}
					store.mu.RLock()
					assert.Zero(t, store.snapshotReads)
					assert.Equal(t, 1, store.definitionWrites)
					store.mu.RUnlock()
				})
			}
		})
	}
}

func TestCatalogRenewProviderReportsFailuresWithoutMutation(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"state read", "commit", "clock", "canceled read", "canceled commit", "missing definition", "invalid wire version"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			catalog, store, clock := testDefinitionCatalog(t)
			first, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			want := errors.New("storage unavailable")
			switch name {
			case "state read":
				store.readErr = want
			case "commit":
				store.commitErr = want
			case "clock":
				clock.mu.Lock()
				clock.err = want
				clock.mu.Unlock()
			case "canceled read":
				cancel()
				want = context.Canceled
			case "canceled commit":
				store.beforeCommit = func(string, catalogWrite) { cancel() }
				want = context.Canceled
			case "missing definition":
				delete(store.definitions, toolsetCatalogKey("tools"))
			case "invalid wire version":
				first.WireProtocolVersion++
				body, err := marshalCatalogState(first)
				require.NoError(t, err)
				store.content[toolsetCatalogKey("tools")] = body
			}
			before, exists := store.Get(toolsetCatalogKey("tools"))
			require.True(t, exists)

			err = catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, time.Minute)

			switch name {
			case "missing definition":
				require.ErrorContains(t, err, "CATALOGINCOMPLETE")
			case "invalid wire version":
				require.ErrorContains(t, err, "invalid wire protocol version")
			default:
				require.ErrorIs(t, err, want)
			}
			assert.NotErrorIs(t, err, errProviderLeaseLost)
			after, exists := store.Get(toolsetCatalogKey("tools"))
			require.True(t, exists)
			assert.Equal(t, before, after)
			assert.Zero(t, store.snapshotReads)
		})
	}
}

func TestCatalogRenewProviderRejectsInvalidDurationAndOverflow(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	catalog, store, clock := testDefinitionCatalog(t)
	first, err := catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	for _, duration := range []time.Duration{toolregistry.MinProviderLeaseDuration - time.Nanosecond, toolregistry.MaxProviderLeaseDuration + time.Nanosecond} {
		err := catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, duration)
		require.ErrorContains(t, err, "provider lease duration")
	}
	clock.Set(time.UnixMilli(math.MaxInt64 - toolregistry.MinProviderLeaseDuration.Milliseconds() + 1))
	first.ProviderLeases[providerLeaseKey("provider", testIncarnationA)] = providerLease{ExpiresAtUnixMilli: math.MaxInt64}
	body, err := marshalCatalogState(first)
	require.NoError(t, err)
	store.mu.Lock()
	store.content[toolsetCatalogKey("tools")] = body
	store.mu.Unlock()

	err = catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, toolregistry.MinProviderLeaseDuration)

	require.ErrorContains(t, err, "overflows Unix milliseconds")
	unchanged, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	assert.Equal(t, body, unchanged)
}
