// These tests keep definition reuse separate from authoritative provider state
// and verify cold validation and independently owned responses.
package registry

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

const mutatedDefinitionValue = "mutated"

func TestCatalogDefinitionCacheEvictsRemovedNames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		read func(*testing.T, *toolsetCatalog)
	}{
		{"startup", func(t *testing.T, catalog *toolsetCatalog) {
			t.Helper()
			require.NoError(t, catalog.validatePersistedEntries(t.Context()))
		}},
		{"list and health enumeration", func(t *testing.T, catalog *toolsetCatalog) {
			t.Helper()
			list, err := catalog.ListToolsets(t.Context(), nil)
			require.NoError(t, err)
			require.Len(t, list, 1)
			assert.Equal(t, "retained", list[0].Name)
		}},
		{"search", func(t *testing.T, catalog *toolsetCatalog) {
			t.Helper()
			results, err := catalog.SearchToolsets(t.Context(), "original")
			require.NoError(t, err)
			assert.Empty(t, results)
		}},
		{"exact read", func(t *testing.T, catalog *toolsetCatalog) {
			t.Helper()
			_, err := catalog.ActiveRegistration(t.Context(), "tools")
			require.ErrorIs(t, err, errToolsetNotFound)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			catalog, store, _ := testDefinitionCatalog(t)
			original, err := catalog.ActiveRegistration(ctx, "tools")
			require.NoError(t, err)
			_, err = catalog.Register(ctx, testCatalogDefinition(t, testCatalogToolset("retained", "survives", nil)),
				testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
			require.NoError(t, err)
			retained, err := catalog.ActiveRegistration(ctx, "retained")
			require.NoError(t, err)
			key := toolsetCatalogKey("tools")
			body, exists := store.Get(key)
			require.True(t, exists)
			retainedBody, exists := store.Get(toolsetCatalogKey("retained"))
			require.True(t, exists)
			store.mu.Lock()
			definition := store.definitions[key]
			delete(store.content, key)
			delete(store.definitions, key)
			store.mu.Unlock()

			tt.read(t, catalog)

			assert.NotContains(t, catalog.definitions, "tools")
			assert.Same(t, retained.Toolset, catalog.definitions["retained"])
			_, exists = store.Get(key)
			assert.False(t, exists)
			unchanged, exists := store.Get(toolsetCatalogKey("retained"))
			require.True(t, exists)
			assert.Equal(t, retainedBody, unchanged)

			// Restoring the name with changed metadata must validate the definition
			// again instead of accepting the previously cached fingerprint.
			corrupt := strings.Replace(definition, `"Title":"original"`, `"Title":"changed"`, 1)
			require.NotEqual(t, definition, corrupt)
			store.mu.Lock()
			store.content[key] = body
			store.definitions[key] = corrupt
			store.mu.Unlock()
			_, err = catalog.ActiveRegistration(ctx, "tools")
			require.ErrorContains(t, err, "schema fingerprint")
			assert.NotContains(t, catalog.definitions, "tools")

			store.mu.Lock()
			store.definitions[key] = definition
			store.mu.Unlock()
			reintroduced, err := catalog.ActiveRegistration(ctx, "tools")
			require.NoError(t, err)
			assert.NotSame(t, original.Toolset, reintroduced.Toolset)
			assert.Equal(t, original.Toolset.info, reintroduced.Toolset.info)
			assert.Equal(t, original.RegistrationToken, reintroduced.RegistrationToken)
		})
	}
}

func TestCatalogDefinitionCacheKeepsNamesOnReadFailure(t *testing.T) {
	t.Parallel()
	catalog, store, _ := testDefinitionCatalog(t)
	original, err := catalog.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	catalog.store = authoritativeKeysFailureMap{catalogStore: store, err: context.DeadlineExceeded}
	_, err = catalog.ListToolsets(t.Context(), nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Same(t, original.Toolset, catalog.definitions["tools"])

	store.mu.Lock()
	store.readErr = context.DeadlineExceeded
	store.mu.Unlock()
	_, err = catalog.ActiveRegistration(t.Context(), "tools")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Same(t, original.Toolset, catalog.definitions["tools"])
}

func TestCatalogStateRejectsInvalidPersistedData(t *testing.T) {
	t.Parallel()
	catalog, store, _ := testDefinitionCatalog(t)
	original, err := catalog.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	body, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	missingSummary := original.catalogState
	missingSummary.Info = nil
	missingSummaryRaw, err := json.Marshal(missingSummary)
	require.NoError(t, err)
	tests := []struct{ name, body, wantErr string }{
		{"unknown field", strings.TrimSuffix(body, "}") + `,"unexpected":true}`, "unknown field"},
		{"old combined definition", strings.TrimSuffix(body, "}") + `,"toolset":{}}`, "unknown field"},
		{"old retirement history", strings.TrimSuffix(body, "}") + `,"retired_tokens":[]}`, "unknown field"},
		{"trailing value", body + `{}`, "trailing JSON"},
		{"invalid epoch", strings.Replace(body, `"health_epoch":1`, `"health_epoch":0`, 1), "health epoch"},
		{"invalid pong", strings.Replace(body, `"last_pong_unix_nano":0`, `"last_pong_unix_nano":-1`, 1), "pong"},
		{"invalid state", strings.Replace(body, `"state":"active"`, `"state":"invalid"`, 1), "catalog state"},
		{"changed revision", strings.Replace(body, testAdmissionRevisionA, testAdmissionRevisionB, 1), "registration token"},
		{"invalid fingerprint", strings.Replace(body, original.SchemaFingerprint, "invalid", 1), "schema fingerprint"},
		{"changed token", strings.Replace(body, original.RegistrationToken, testStaleToken, 1), "registration token"},
		{"missing summary", string(missingSummaryRaw), "invalid discovery metadata"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotEqual(t, body, tt.body)
			_, err := parseCatalogState("tools", tt.body)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
	state, err := parseCatalogState("tools", body)
	require.NoError(t, err)
	roundTrip, err := marshalCatalogState(state)
	require.NoError(t, err)
	assert.Equal(t, body, roundTrip)
	assert.NotContains(t, body, "ConsumerContract")
	assert.NotContains(t, body, "PayloadSchema")
}

func TestCatalogStartupRequiresCompleteConsistentStorage(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"missing definition", "orphan definition", "summary mismatch",
		"fingerprint mismatch", "invalid retired token", "active token marked retired",
		"retired token missing from history", "valid retired admission",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			catalog, store, clock := testDefinitionCatalog(t)
			key := toolsetCatalogKey("tools")
			current, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			wantErr := ""
			switch name {
			case "missing definition":
				delete(store.definitions, key)
				wantErr = "CATALOGINCOMPLETE"
			case "orphan definition":
				delete(store.content, key)
				wantErr = "CATALOGINCOMPLETE"
			case "summary mismatch":
				current.Info.ToolCount++
				store.content[key], err = marshalCatalogState(current)
				require.NoError(t, err)
				wantErr = "discovery metadata does not match definition"
			case "fingerprint mismatch":
				current.SchemaFingerprint = testStaleToken
				current.RegistrationToken, err = admissionRegistrationToken(
					current.SchemaFingerprint, current.AdmissionRevision, current.WireProtocolVersion,
				)
				require.NoError(t, err)
				store.content[key], err = marshalCatalogState(current)
				require.NoError(t, err)
				wantErr = "schema fingerprint does not match canonical schema"
			case "invalid retired token":
				store.retiredTokens["invalid"] = struct{}{}
				wantErr = "registration token"
			case "active token marked retired":
				store.retiredTokens[current.RegistrationToken] = struct{}{}
				wantErr = "disagrees with permanent retirement history"
			case "retired token missing from history":
				require.NoError(t, catalog.Retire(ctx, "tools", current.RegistrationToken))
				clear(store.retiredTokens)
				wantErr = "disagrees with permanent retirement history"
			case "valid retired admission":
				require.NoError(t, catalog.Retire(ctx, "tools", current.RegistrationToken))
			}

			err = newToolsetCatalog(store, clock).validatePersistedEntries(ctx)

			if wantErr == "" {
				require.NoError(t, err)
			} else {
				require.ErrorContains(t, err, wantErr)
			}
		})
	}
}

func TestCatalogColdReadChecksCurrentRetirementMembership(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"active token marked retired", "retired token missing from history", "invalid state before membership"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			writer, store, clock := testDefinitionCatalog(t)
			current, err := writer.activeState(ctx, "tools")
			require.NoError(t, err)
			wantErr := "disagrees with permanent retirement history"
			switch name {
			case "active token marked retired":
				store.mu.Lock()
				store.retiredTokens[current.RegistrationToken] = struct{}{}
				store.mu.Unlock()
			case "retired token missing from history":
				require.NoError(t, writer.Retire(ctx, "tools", current.RegistrationToken))
				store.mu.Lock()
				clear(store.retiredTokens)
				store.mu.Unlock()
			case "invalid state before membership":
				current.WireProtocolVersion++
				body, err := marshalCatalogState(current)
				require.NoError(t, err)
				store.mu.Lock()
				store.content[toolsetCatalogKey("tools")] = body
				store.retiredTokens[current.RegistrationToken] = struct{}{}
				store.mu.Unlock()
				wantErr = "invalid wire protocol version"
			}
			cold := newToolsetCatalog(store, clock)

			_, err = cold.snapshot(ctx, "tools")

			require.ErrorContains(t, err, wantErr)
			assert.Empty(t, cold.definitions, "invalid state must not populate the definition cache")
		})
	}
}

func TestCatalogReadsRejectMissingDefinitionWithWarmOrColdCache(t *testing.T) {
	t.Parallel()
	for _, cache := range []string{"warm", "cold"} {
		t.Run(cache, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			catalog, store, clock := testDefinitionCatalog(t)
			current, err := catalog.activeState(ctx, "tools")
			require.NoError(t, err)
			if cache == "cold" {
				catalog = newToolsetCatalog(store, clock)
			}
			key := toolsetCatalogKey("tools")
			before, exists := store.Get(key)
			require.True(t, exists)
			store.mu.Lock()
			delete(store.definitions, key)
			store.mu.Unlock()

			_, err = catalog.ActiveRegistration(ctx, "tools")
			require.ErrorContains(t, err, "CATALOGINCOMPLETE")
			_, err = catalog.GetToolset(ctx, "tools")
			require.ErrorContains(t, err, "CATALOGINCOMPLETE")
			_, err = (&Service{catalog: catalog}).ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
			require.ErrorContains(t, err, "CATALOGINCOMPLETE")
			_, err = catalog.ListToolsets(ctx, nil)
			require.ErrorContains(t, err, "CATALOGINCOMPLETE")
			_, err = catalog.SearchToolsets(ctx, "original")
			require.ErrorContains(t, err, "CATALOGINCOMPLETE")
			health, err := newDirectHealthTracker(ctx, catalog).Health(ctx, "tools", current.RegistrationToken)
			require.ErrorContains(t, err, "CATALOGINCOMPLETE")
			assert.False(t, health.Healthy)

			after, exists := store.Get(key)
			require.True(t, exists)
			assert.Equal(t, before, after)
			store.mu.RLock()
			assert.NotContains(t, store.definitions, key)
			assert.Zero(t, store.snapshotReads, "presence checks must not fetch full definitions")
			assert.Equal(t, 1, store.definitionWrites)
			store.mu.RUnlock()
		})
	}
}

func TestCatalogColdDefinitionRejectsInvalidPersistedData(t *testing.T) {
	t.Parallel()
	_, store, clock := testDefinitionCatalog(t)
	key := toolsetCatalogKey("tools")
	body, definition, retired, exists, err := store.Snapshot(t.Context(), key)
	require.NoError(t, err)
	require.True(t, exists)
	assert.False(t, retired)
	invalidPayload := testDefinitionToolset()
	invalidPayload.Tools[0].ExecutionPayloadSchema = []byte(`{"type":"not-a-json-schema-type"}`)
	invalidRaw, err := json.Marshal(invalidPayload)
	require.NoError(t, err)
	tests := []struct{ name, raw, wantErr string }{
		{"changed metadata", strings.Replace(definition, `"Title":"original"`, `"Title":"changed"`, 1), "schema fingerprint"},
		{"unknown field", strings.Replace(definition, `"Title":"original"`, `"Title":"original","unexpected":true`, 1), "unknown field"},
		{"invalid union", strings.Replace(definition, `"type":"field"`, `"type":"invalid"`, 1), "invalid"},
		{"trailing value", definition + `{}`, "trailing JSON"},
		{"invalid execution schema", string(invalidRaw), "invalid persisted schemas"},
		{"wrong name", strings.Replace(definition, `"Name":"tools"`, `"Name":"other"`, 1), "invalid static definition"},
		{"registration time in static definition", strings.Replace(definition, `"RegisteredAt":""`, `"RegisteredAt":"2026-09-18T00:00:00Z"`, 1), "invalid static definition"},
		{"null definition", `null`, "invalid static definition"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotEqual(t, definition, tt.raw)
			store.mu.Lock()
			store.definitions[key] = tt.raw
			store.mu.Unlock()
			cold := newToolsetCatalog(store, clock)
			_, err := cold.ActiveRegistration(t.Context(), "tools")
			require.ErrorContains(t, err, tt.wantErr)
			assert.Empty(t, cold.definitions)
			unchanged, exists := store.Get(key)
			require.True(t, exists)
			assert.Equal(t, body, unchanged)
		})
	}
}

func TestCatalogColdReadValidatesAndCachesExecutionSchemas(t *testing.T) {
	t.Parallel()
	_, store, clock := testDefinitionCatalog(t)
	cold := newToolsetCatalog(store, clock)
	assert.Empty(t, cold.validator.compiled)
	first, err := cold.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	assert.Equal(t, 1, store.snapshotReads)
	digest := schemaDigest(testDefinitionToolset().Tools[0].ExecutionPayloadSchema)
	compiled := cold.validator.compiled[digest]
	require.NotNil(t, compiled)
	assert.Same(t, compiled, first.Toolset.executionSchemas["tools.lookup"])

	store.mu.Lock()
	store.snapshotErr = context.DeadlineExceeded
	store.mu.Unlock()
	for range 3 {
		cached, err := cold.ActiveRegistration(t.Context(), "tools")
		require.NoError(t, err)
		assert.Same(t, first.Toolset, cached.Toolset)
		require.NoError(t, validatePayload(cached.Toolset.executionSchemas["tools.lookup"], []byte(`{}`)))
		require.Error(t, validatePayload(cached.Toolset.executionSchemas["tools.lookup"], []byte(`[]`)))
	}
	assert.Equal(t, 1, store.snapshotReads)
	assert.Len(t, cold.validator.compiled, 1)
	assert.Same(t, compiled, cold.validator.compiled[digest])
}

func TestCatalogLifecycleReadsNoDefinitions(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	registered, store, clock := testDefinitionCatalog(t)
	first, err := registered.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	// A new catalog has no definition cache. Even cold lifecycle operations
	// must succeed when full-definition reads are unavailable.
	catalog := newToolsetCatalog(store, clock)
	store.mu.Lock()
	store.snapshotErr = context.DeadlineExceeded
	store.mu.Unlock()
	clock.Set(time.Unix(1_700_000_010, 0))
	require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, first.HealthEpoch))
	ponged, _, err := catalog.healthEntry(ctx, "tools")
	require.NoError(t, err)
	assert.Equal(t, time.Unix(1_700_000_010, 0).UnixNano(), ponged.LastPongUnixNano)
	require.NoError(t, catalog.RenewProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, time.Minute))
	list, err := catalog.ListToolsets(ctx, []string{"original"})
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, first.Info, list[0])
	search, err := catalog.SearchToolsets(ctx, "original")
	require.NoError(t, err)
	assert.Equal(t, list, search)
	token, err := catalog.RegistrationToken(ctx, "tools")
	require.NoError(t, err)
	assert.Equal(t, first.RegistrationToken, token)
	require.NoError(t, catalog.VerifyActiveToken(ctx, "tools", token))
	leases, err := catalog.ActiveProviderLeases(ctx, "tools", token)
	require.NoError(t, err)
	require.Len(t, leases, 1)
	tracker := newDirectHealthTracker(ctx, catalog)
	require.NoError(t, tracker.EnsurePingLoop(ctx, "tools"))
	health, err := tracker.Health(ctx, "tools", token)
	require.NoError(t, err)
	assert.True(t, health.Healthy)
	require.NoError(t, catalog.DrainProvider(ctx, "tools", "provider", testIncarnationA, token, time.Minute))
	require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", testIncarnationA, token, first.HealthEpoch))
	draining, _, err := catalog.healthEntry(ctx, "tools")
	require.NoError(t, err)
	assert.True(t, draining.ProviderLeases[providerLeaseKey("provider", testIncarnationA)].Draining)
	assert.Zero(t, draining.LastPongUnixNano)
	assert.Equal(t, first.HealthEpoch+1, draining.HealthEpoch)
	clock.Set(time.Unix(1_700_000_071, 0))
	expired, _, err := catalog.healthEntry(ctx, "tools")
	require.NoError(t, err)
	assert.Empty(t, expired.ProviderLeases)
	require.NoError(t, catalog.ReleaseProvider(ctx, "tools", "provider", testIncarnationA, token))
	require.NoError(t, catalog.Retire(ctx, "tools", token))
	assert.Zero(t, store.snapshotReads)
	assert.Equal(t, 1, store.definitionWrites)
	assert.Empty(t, catalog.definitions)
	assert.Equal(t, string(first.Toolset.raw), store.definitions[toolsetCatalogKey("tools")])
}

func TestCatalogDefinitionCacheReplacesDefinition(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	catalog, store, clock := testDefinitionCatalog(t)
	first, err := catalog.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	require.NoError(t, catalog.ReleaseProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken))
	clock.Set(time.Unix(1_700_000_010, 0))
	next := testDefinitionToolset()
	next.Tools[0].ConsumerContract.Title = "replacement"
	replacement, err := catalog.Register(ctx, testCatalogDefinition(t, next), testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	current, err := catalog.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	assert.NotSame(t, first.Toolset, current.Toolset)
	assert.Equal(t, replacement.RegistrationToken, current.RegistrationToken)
	assert.NotEqual(t, first.RegistrationToken, current.RegistrationToken)
	retired, err := store.Retired(ctx, first.RegistrationToken)
	require.NoError(t, err)
	assert.True(t, retired)
	currentDefinition, err := current.Toolset.decode(current.RegisteredAt)
	require.NoError(t, err)
	firstDefinition, err := first.Toolset.decode(first.RegisteredAt)
	require.NoError(t, err)
	assert.Equal(t, "replacement", currentDefinition.Tools[0].ConsumerContract.Title)
	assert.Equal(t, "original", firstDefinition.Tools[0].ConsumerContract.Title)
	assert.Len(t, catalog.definitions, 1)
	assert.Same(t, current.Toolset, catalog.definitions["tools"])
	assert.Equal(t, 2, store.definitionWrites)
}

func TestCatalogSameDefinitionNewAdmissionKeepsCurrentMetadata(t *testing.T) {
	t.Parallel()
	catalog, store, clock := testDefinitionCatalog(t)
	reader := newToolsetCatalog(store, clock)
	first, err := reader.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	require.NoError(t, catalog.ReleaseProvider(t.Context(), "tools", "provider", testIncarnationA, first.RegistrationToken))
	clock.Set(time.Unix(1_700_000_010, 0))
	next, err := catalog.Register(t.Context(), testCatalogDefinition(t, testDefinitionToolset()), testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	current, err := reader.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	assert.Same(t, first.Toolset, current.Toolset)
	assert.NotEqual(t, first.RegistrationToken, current.RegistrationToken)
	assert.NotEqual(t, first.RegisteredAt, current.RegisteredAt)
	assert.Equal(t, next.RegisteredAt, current.RegisteredAt)
	decoded, err := current.Toolset.decode(current.RegisteredAt)
	require.NoError(t, err)
	assert.Equal(t, next.RegisteredAt, decoded.RegisteredAt)
	assert.Empty(t, current.Toolset.info.RegisteredAt)
	assert.Equal(t, 1, store.definitionWrites)
	assert.Equal(t, 1, store.snapshotReads)
}

func TestCatalogSameFingerprintReplacementPreservesStoredTagOrder(t *testing.T) {
	t.Parallel()
	for _, cache := range []string{"warm registrar", "cold registrar"} {
		t.Run(cache, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			registrar := newToolsetCatalog(store, clock)
			input := testDefinitionToolset()
			input.Tags = []string{"alpha", "beta"}
			input.Tools[0].Tags = []string{"lookup", "original"}
			definition := testCatalogDefinition(t, input)
			first, err := registrar.Register(ctx, definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
			require.NoError(t, err)
			require.NoError(t, registrar.ReleaseProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken))
			if cache == "cold registrar" {
				registrar = newToolsetCatalog(store, clock)
			}
			clock.Set(time.Unix(1_700_000_010, 0))
			next := testDefinitionToolset()
			next.Tags = []string{"beta", "alpha"}
			next.Tools[0].Tags = []string{"original", "lookup"}
			incoming := testCatalogDefinition(t, next)
			require.Equal(t, definition.fingerprint, incoming.fingerprint)

			replacement, err := registrar.Register(ctx, incoming, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)

			require.NoError(t, err)
			assert.NotEqual(t, first.RegistrationToken, replacement.RegistrationToken)
			assert.NotEqual(t, first.RegisteredAt, replacement.RegisteredAt)
			assert.Equal(t, []string{"alpha", "beta"}, replacement.Info.Tags)
			assert.Equal(t, replacement.RegisteredAt, replacement.Info.RegisteredAt)
			if cache == "cold registrar" {
				assert.Empty(t, registrar.definitions, "an unwritten definition must not populate the cache")
			} else {
				assert.Same(t, definition, registrar.definitions["tools"])
			}
			store.mu.RLock()
			assert.Equal(t, string(definition.raw), store.definitions[toolsetCatalogKey("tools")])
			assert.Equal(t, 1, store.definitionWrites)
			store.mu.RUnlock()
			expected := testDefinitionToolset()
			expected.Tags = []string{"alpha", "beta"}
			expected.Tools[0].Tags = []string{"lookup", "original"}
			expected.RegisteredAt = replacement.RegisteredAt
			for _, reader := range []*toolsetCatalog{registrar, newToolsetCatalog(store, clock)} {
				got, err := reader.GetToolset(ctx, "tools")
				require.NoError(t, err)
				assert.Equal(t, expected, got, "schema bytes and source ordering remain those of the stored definition")
				resolved, err := (&Service{catalog: reader}).ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
				require.NoError(t, err)
				assert.Equal(t, replacement.RegistrationToken, resolved.RegistrationToken)
				assert.Equal(t, expected, resolved.Toolset)
			}
			require.NoError(t, newToolsetCatalog(store, clock).validatePersistedEntries(ctx))
			assert.Equal(t, []string{"beta", "alpha"}, next.Tags, "registration must not rewrite the caller's input")
		})
	}
}

func TestCatalogColdReadPairsDefinitionWithReplacementState(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	writer, store, clock := testDefinitionCatalog(t)
	original, err := writer.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	require.NoError(t, writer.ReleaseProvider(ctx, "tools", "provider", testIncarnationA, original.RegistrationToken))
	cold := newToolsetCatalog(store, clock)
	next := testDefinitionToolset()
	next.Tools[0].ConsumerContract.Title = "replacement"
	definition := testCatalogDefinition(t, next)
	var replacement catalogState
	store.mu.Lock()
	store.afterExactRead = func(string) {
		store.mu.Lock()
		store.afterExactRead = nil
		store.mu.Unlock()
		var registerErr error
		replacement, registerErr = writer.Register(ctx, definition, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
		require.NoError(t, registerErr)
	}
	store.mu.Unlock()
	resolved, err := (&Service{catalog: cold}).ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
	require.NoError(t, err)
	assert.NotEqual(t, original.RegistrationToken, resolved.RegistrationToken)
	assert.Equal(t, replacement.RegistrationToken, resolved.RegistrationToken)
	assert.Equal(t, "replacement", resolved.Toolset.Tools[0].ConsumerContract.Title)
}

func TestCatalogDefinitionResultsHaveIndependentOwnership(t *testing.T) {
	t.Parallel()
	for _, cold := range []bool{false, true} {
		name := "registered cache"
		if cold {
			name = "cold cache"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ctx := t.Context()
			catalog, store, clock := testDefinitionCatalog(t)
			if cold {
				catalog = newToolsetCatalog(store, clock)
			}
			svc := &Service{catalog: catalog}
			expected := testDefinitionToolset()
			expected.RegisteredAt = time.Unix(1_700_000_000, 0).UTC().Format(time.RFC3339Nano)
			got, err := svc.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
			require.NoError(t, err)
			assert.Equal(t, expected, got)
			mutateDefinitionResult(got)
			resolved, err := svc.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
			require.NoError(t, err)
			assert.Equal(t, expected, resolved.Toolset)
			mutateDefinitionResult(resolved.Toolset)
			list, err := svc.ListToolsets(ctx, &genregistry.ListToolsetsPayload{})
			require.NoError(t, err)
			require.Len(t, list.Toolsets, 1)
			*list.Toolsets[0].Description = mutatedDefinitionValue
			*list.Toolsets[0].Version = "9.9.9"
			list.Toolsets[0].Tags[0] = mutatedDefinitionValue
			search, err := svc.Search(ctx, &genregistry.SearchPayload{Query: "original"})
			require.NoError(t, err)
			require.Len(t, search.Toolsets, 1)
			assert.Equal(t, toolsetToInfo(expected), search.Toolsets[0])
			*search.Toolsets[0].Description = "mutated again"
			*search.Toolsets[0].Version = "8.8.8"
			search.Toolsets[0].Tags[0] = "mutated again"
			got, err = svc.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
			require.NoError(t, err)
			assert.Equal(t, expected, got)
			cached, err := catalog.ActiveRegistration(ctx, "tools")
			require.NoError(t, err)
			require.NoError(t, validatePayload(cached.Toolset.executionSchemas["tools.lookup"], []byte(`{}`)))
			require.Error(t, validatePayload(cached.Toolset.executionSchemas["tools.lookup"], []byte(`[]`)))
		})
	}
}

func TestCatalogDefinitionCacheConcurrentReadersAndPongs(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	_, store, clock := testDefinitionCatalog(t)
	catalog := newToolsetCatalog(store, clock)
	svc := &Service{catalog: catalog}
	const readers = 8
	definitions := make(chan *catalogToolset, readers)
	errs := make(chan error, readers+1)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range readers {
		wg.Go(func() {
			<-start
			for i := range 10 {
				entry, err := catalog.ActiveRegistration(ctx, "tools")
				if err != nil {
					errs <- err
					return
				}
				if i == 0 {
					definitions <- entry.Toolset
				}
				got, err := svc.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
				if err != nil {
					errs <- err
					return
				}
				mutateDefinitionResult(got)
				if _, err := catalog.ListToolsets(ctx, nil); err != nil {
					errs <- err
					return
				}
			}
		})
	}
	wg.Go(func() {
		<-start
		entry, err := catalog.ActiveRegistration(ctx, "tools")
		if err != nil {
			errs <- err
			return
		}
		for range 10 {
			if err := catalog.RecordPong(ctx, "tools", "provider", testIncarnationA, entry.RegistrationToken, entry.HealthEpoch); err != nil {
				errs <- err
				return
			}
		}
	})
	close(start)
	wg.Wait()
	close(errs)
	close(definitions)
	for err := range errs {
		require.NoError(t, err)
	}
	var first *catalogToolset
	for definition := range definitions {
		if first == nil {
			first = definition
		}
		assert.Same(t, first, definition)
	}
	assert.Len(t, catalog.definitions, 1)
	definition, err := first.decode("")
	require.NoError(t, err)
	assert.Equal(t, "original", definition.Tools[0].ConsumerContract.Title)
}

// testDefinitionCatalog admits a nested definition and then mutates its input,
// proving registration retains its own copy before any caller reads it.
func testDefinitionCatalog(t *testing.T) (*toolsetCatalog, *testCatalogMap, *testTimeSource) {
	t.Helper()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	input := testDefinitionToolset()
	_, err := catalog.Register(t.Context(), testCatalogDefinition(t, input), testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	mutateDefinitionResult(input)
	return catalog, store, clock
}

func testDefinitionToolset() *genregistry.Toolset {
	description := "original description"
	version := genregistry.SemVer("1.0.0")
	return &genregistry.Toolset{
		Name: "tools", Description: &description, Version: &version, Tags: []string{"original"},
		Tools: []*genregistry.ToolSchema{{
			Name: "tools.lookup", Tags: []string{"lookup"},
			PayloadSchema: []byte(`{"type":"object"}`), ExecutionPayloadSchema: []byte(`{"type":"object"}`),
			ResultSchema: []byte(`{"type":"object"}`),
			ConsumerContract: &genregistry.ConsumerContract{
				Kind: "service", Title: "original",
				Search: &genregistry.ToolSearchDocument{Length: 1, Terms: map[string]int{"lookup": 1}},
				Meta:   map[string][]string{"audience": {"user"}},
				Payload: &genregistry.ToolTypeMetadata{
					SchemaWithoutRootExample: []byte(`{"type":"object"}`),
					Fields: []*genregistry.ToolFieldMetadata{{
						Path: []*genregistry.ToolFieldPathSegment{{
							Segment: genregistry.NewToolFieldSegmentField("input"),
						}},
					}},
				},
			},
		}},
	}
}

func mutateDefinitionResult(toolset *genregistry.Toolset) {
	*toolset.Description = mutatedDefinitionValue
	*toolset.Version = "9.9.9"
	toolset.Tags[0] = mutatedDefinitionValue
	toolset.Tools[0].Tags[0] = mutatedDefinitionValue
	toolset.Tools[0].PayloadSchema[0] = '!'
	toolset.Tools[0].ExecutionPayloadSchema[0] = '!'
	contract := toolset.Tools[0].ConsumerContract
	contract.Title = mutatedDefinitionValue
	contract.Search.Terms["lookup"] = 99
	contract.Meta["audience"][0] = mutatedDefinitionValue
	contract.Payload.SchemaWithoutRootExample[0] = '!'
	contract.Payload.Fields[0].Path[0].Segment.SetField(mutatedDefinitionValue)
}
