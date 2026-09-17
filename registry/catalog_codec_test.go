// These tests keep definition reuse separate from authoritative admission state
// and verify that callers cannot mutate the registry's retained metadata.
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
			_, err = catalog.Register(ctx, testCatalogToolset("retained", "survives", nil),
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
			delete(store.content, key)
			store.mu.Unlock()

			tt.read(t, catalog)

			assert.NotContains(t, catalog.definitions, "tools")
			assert.Same(t, retained.Toolset, catalog.definitions["retained"])
			_, exists = store.Get(key)
			assert.False(t, exists)
			unchanged, exists := store.Get(toolsetCatalogKey("retained"))
			require.True(t, exists)
			assert.Equal(t, retainedBody, unchanged)

			// Reintroducing the same name cannot reuse a previously verified
			// fingerprint to accept changed metadata.
			corrupt := strings.Replace(body, `"Title":"original"`, `"Title":"changed"`, 1)
			inserted, err := store.SetIfNotExists(ctx, key, corrupt)
			require.NoError(t, err)
			require.True(t, inserted)
			_, err = catalog.ActiveRegistration(ctx, "tools")
			require.ErrorContains(t, err, "schema fingerprint")
			assert.NotContains(t, catalog.definitions, "tools")

			_, _, updated, err := store.TestAndSetEx(ctx, key, corrupt, body)
			require.NoError(t, err)
			require.True(t, updated)
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
	catalog.m = authoritativeKeysFailureMap{
		catalogMap: store,
		err:        context.DeadlineExceeded,
	}
	_, err = catalog.ListToolsets(t.Context(), nil)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Same(t, original.Toolset, catalog.definitions["tools"])

	store.mu.Lock()
	store.testAndSetErr = context.DeadlineExceeded
	store.mu.Unlock()
	_, err = catalog.ActiveRegistration(t.Context(), "tools")
	require.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Same(t, original.Toolset, catalog.definitions["tools"])
}

func TestCatalogDefinitionCacheRejectsChangedContent(t *testing.T) {
	t.Parallel()

	catalog, store, _ := testDefinitionCatalog(t)
	original, err := catalog.ActiveRegistration(t.Context(), "tools")
	require.NoError(t, err)
	body, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	var saved map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &saved))
	duplicateInvalid := strings.TrimSuffix(
		strings.Replace(body, `"Title":"original"`, `"Title":"original","unexpected":true`, 1),
		"}",
	) + `,"toolset":` + string(saved["toolset"]) + `}`

	tests := []struct {
		name, body, wantErr string
	}{
		{"changed contract with old fingerprint", strings.Replace(body, `"Title":"original"`, `"Title":"changed"`, 1), "schema fingerprint"},
		{"unknown definition field", strings.Replace(body, `"Title":"original"`, `"Title":"original","unexpected":true`, 1), "unknown field"},
		{"invalid generated union", strings.Replace(body, `"type":"field"`, `"type":"invalid"`, 1), "invalid"},
		{"unknown envelope field", strings.TrimSuffix(body, "}") + `,"unexpected":true}`, "unknown field"},
		{"trailing value", body + `{}`, "trailing JSON"},
		{"invalid epoch", strings.Replace(body, `"health_epoch":1`, `"health_epoch":0`, 1), "health epoch"},
		{"invalid pong", strings.Replace(body, `"last_pong_unix_nano":0`, `"last_pong_unix_nano":-1`, 1), "pong"},
		{"invalid state", strings.Replace(body, `"state":"active"`, `"state":"invalid"`, 1), "catalog state"},
		{"changed revision", strings.Replace(body, testAdmissionRevisionA, testAdmissionRevisionB, 1), "registration token"},
		{"changed fingerprint", strings.Replace(body, original.SchemaFingerprint, testStaleToken, 1), "schema fingerprint"},
		{"changed token", strings.Replace(body, original.RegistrationToken, testStaleToken, 1), "registration token"},
		{"later member cannot hide invalid definition", duplicateInvalid, "unknown field"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.NotEqual(t, body, tt.body)
			_, err := catalog.parseCatalogEntry("tools", tt.body)
			require.ErrorContains(t, err, tt.wantErr)
			reloaded, err := catalog.parseCatalogEntry("tools", body)
			require.NoError(t, err)
			assert.Same(t, original.Toolset, reloaded.Toolset)
		})
	}
}

func TestCatalogDefinitionRepeatedJSONMembersPreserveMerge(t *testing.T) {
	t.Parallel()

	catalog, store, _ := testDefinitionCatalog(t)
	body, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	original, err := catalog.parseCatalogEntry("tools", body)
	require.NoError(t, err)
	// A later partial object leaves the first object's other fields intact.
	mergedBody := strings.TrimSuffix(body, "}") + `,"toolset":{"Name":"tools"}}`
	merged, err := catalog.parseCatalogEntry("tools", mergedBody)
	require.NoError(t, err)
	assert.Equal(t, original.Toolset.info, merged.Toolset.info)
	assert.Equal(t, original.Toolset.raw, merged.Toolset.raw)
	encoded, err := marshalCatalogEntry(merged)
	require.NoError(t, err)
	assert.Equal(t, body, encoded)
}

func TestCatalogDefinitionCacheKeepsStateFresh(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	catalog, store, clock := testDefinitionCatalog(t)
	first, err := catalog.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	clock.Set(time.Unix(1_700_000_010, 0))
	require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, first.HealthEpoch))
	ponged, _, err := catalog.healthEntry(ctx, "tools")
	require.NoError(t, err)
	assert.Same(t, first.Toolset, ponged.Toolset)
	assert.Equal(t, time.Unix(1_700_000_010, 0).UnixNano(), ponged.LastPongUnixNano)
	assert.Zero(t, first.LastPongUnixNano)

	renewed, err := catalog.Register(ctx, testDefinitionToolset(), testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	assert.Same(t, first.Toolset, renewed.Toolset)
	key := providerLeaseKey("provider", testIncarnationA)
	assert.Equal(t, time.Unix(1_700_000_070, 0).UnixMilli(), renewed.ProviderLeases[key].ExpiresAtUnixMilli)
	assert.Equal(t, time.Unix(1_700_000_060, 0).UnixMilli(), first.ProviderLeases[key].ExpiresAtUnixMilli)

	require.NoError(t, catalog.DrainProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, time.Minute))
	draining, _, err := catalog.healthEntry(ctx, "tools")
	require.NoError(t, err)
	assert.Same(t, first.Toolset, draining.Toolset)
	assert.True(t, draining.ProviderLeases[key].Draining)
	assert.Equal(t, first.HealthEpoch+1, draining.HealthEpoch)
	assert.Zero(t, draining.LastPongUnixNano)
	require.NoError(t, catalog.RecordPong(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken, first.HealthEpoch))
	stalePong, _, err := catalog.healthEntry(ctx, "tools")
	require.NoError(t, err)
	assert.Zero(t, stalePong.LastPongUnixNano)

	clock.Set(time.Unix(1_700_000_071, 0))
	expired, _, err := catalog.healthEntry(ctx, "tools")
	require.NoError(t, err)
	assert.Same(t, first.Toolset, expired.Toolset)
	assert.Empty(t, expired.ProviderLeases)
	_, _, err = catalog.HealthIdentity(ctx, "tools")
	require.ErrorIs(t, err, errToolsetNotFound)

	body, exists := store.Get(toolsetCatalogKey("tools"))
	require.True(t, exists)
	var envelope map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(body), &envelope))
	assert.Equal(t, []byte(first.Toolset.raw), []byte(envelope["toolset"]))
	require.NoError(t, catalog.Retire(ctx, "tools", first.RegistrationToken))
	_, err = catalog.ActiveRegistration(ctx, "tools")
	require.ErrorIs(t, err, errToolsetNotFound)
}

func TestCatalogDefinitionCacheReplacesDefinition(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	catalog, _, clock := testDefinitionCatalog(t)
	first, err := catalog.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	require.NoError(t, catalog.ReleaseProvider(ctx, "tools", "provider", testIncarnationA, first.RegistrationToken))
	clock.Set(time.Unix(1_700_000_010, 0))
	next := testDefinitionToolset()
	next.Tools[0].ConsumerContract.Title = "replacement"
	replacement, err := catalog.Register(ctx, next, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	current, err := catalog.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	assert.NotSame(t, first.Toolset, current.Toolset)
	assert.Equal(t, replacement.RegistrationToken, current.RegistrationToken)
	assert.NotEqual(t, first.RegistrationToken, current.RegistrationToken)
	assert.Contains(t, current.RetiredTokens, first.RegistrationToken)
	currentDefinition, err := current.Toolset.decode()
	require.NoError(t, err)
	firstDefinition, err := first.Toolset.decode()
	require.NoError(t, err)
	assert.Equal(t, "replacement", currentDefinition.Tools[0].ConsumerContract.Title)
	assert.Equal(t, "original", firstDefinition.Tools[0].ConsumerContract.Title)
	assert.Len(t, catalog.definitions, 1)
	assert.Same(t, current.Toolset, catalog.definitions["tools"])
}

func TestCatalogDefinitionResultsHaveIndependentOwnership(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	catalog, _, _ := testDefinitionCatalog(t)
	svc := &Service{catalog: catalog}
	cached, err := catalog.ActiveRegistration(ctx, "tools")
	require.NoError(t, err)
	expected, err := cached.Toolset.decode()
	require.NoError(t, err)

	got, err := svc.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
	require.NoError(t, err)
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
	assert.Equal(t, expected.Description, search.Toolsets[0].Description)
	assert.Equal(t, expected.Version, search.Toolsets[0].Version)
	assert.Equal(t, expected.Tags, search.Toolsets[0].Tags)
	*search.Toolsets[0].Description = "mutated again"
	*search.Toolsets[0].Version = "8.8.8"
	search.Toolsets[0].Tags[0] = "mutated again"

	got, err = svc.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
	require.NoError(t, err)
	assert.Equal(t, expected, got)
	assert.Equal(t, toolsetToInfo(expected), cached.Toolset.info)
}

func TestCatalogDefinitionCacheConcurrentReadersAndPongs(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	catalog, _, _ := testDefinitionCatalog(t)
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
	definition, err := first.decode()
	require.NoError(t, err)
	assert.Equal(t, "original", definition.Tools[0].ConsumerContract.Title)
}

// testDefinitionCatalog registers metadata with mutable nested fields so tests
// cover both complete contract reuse and ownership of returned results.
func testDefinitionCatalog(t *testing.T) (*toolsetCatalog, *testCatalogMap, *testTimeSource) {
	t.Helper()
	store := newTestCatalogMap()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	catalog := newToolsetCatalog(store, clock)
	input := testDefinitionToolset()
	_, err := catalog.Register(t.Context(), input, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
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
	contract := toolset.Tools[0].ConsumerContract
	contract.Title = mutatedDefinitionValue
	contract.Search.Terms["lookup"] = 99
	contract.Meta["audience"][0] = mutatedDefinitionValue
	contract.Payload.SchemaWithoutRootExample[0] = '!'
	contract.Payload.Fields[0].Path[0].Segment.SetField(mutatedDefinitionValue)
}
