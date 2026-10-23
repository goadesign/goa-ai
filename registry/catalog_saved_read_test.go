// Saved declaration reads keep retired contracts available for historical
// comparisons. These tests use synthetic catalog records and verify that the
// same read never restores provider admission or changes stored bytes.
package registry

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
)

func TestReadSavedCatalogToolsetPreservesRetiredDeclarationWithoutAdmission(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	service, store := testBoundedCatalogService(t, testDefinitionToolset())
	key := toolsetCatalogKey("tools")
	budget := int64(len(store.content[key]) + len(store.definitions[key]) + 1024)
	service.catalogHealth = func(catalogState, time.Time) ToolsetHealth {
		panic("saved declaration reads must not observe provider health")
	}
	before, err := service.ReadSavedCatalogToolset(ctx, "tools", budget)
	require.NoError(t, err)
	assert.Nil(t, before.ServiceHealth)
	token := before.Registration.RegistrationToken
	require.NoError(t, service.Unregister(ctx, &genregistry.UnregisterPayload{
		Name: "tools", ExpectedRegistrationToken: token,
	}))
	state, definition, retired, exists, err := store.Snapshot(ctx, key)
	require.NoError(t, err)
	require.True(t, exists)
	require.True(t, retired)
	writes := store.definitionWrites
	service.catalog.forgetDefinition("tools")

	after, err := service.ReadSavedCatalogToolset(ctx, "tools", budget)
	require.NoError(t, err)
	assert.Equal(t, before, after)
	assert.Nil(t, after.ServiceHealth)
	mutateDefinitionResult(after.Registration.Toolset)
	again, err := service.ReadSavedCatalogToolset(ctx, "tools", budget)
	require.NoError(t, err)
	assert.Equal(t, before, again, "each historical reader receives its own declaration")
	storedState, storedDefinition, storedRetired, _, err := store.Snapshot(ctx, key)
	require.NoError(t, err)
	assert.Equal(t, state, storedState)
	assert.Equal(t, definition, storedDefinition)
	assert.Equal(t, retired, storedRetired)
	assert.Equal(t, writes, store.definitionWrites)

	_, err = service.ReadCatalogToolset(ctx, "tools", budget)
	requireServiceErrorName(t, err, "not_found")
	_, err = (&CatalogReader{catalog: service.catalog}).ReadCatalogToolset(ctx, "tools", budget)
	requireServiceErrorName(t, err, "not_found")
	_, err = service.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: "tools"})
	requireServiceErrorName(t, err, "not_found")
	_, err = service.AttachProvider(ctx, &genregistry.AttachProviderPayload{
		Name: "tools", ExpectedRegistrationToken: token, ProviderID: "late-provider",
		ProviderIncarnationID: testIncarnationB, WireProtocolVersion: toolregistry.WireProtocolVersion,
	})
	requireServiceErrorName(t, err, "admission_retired")
	service.healthTracker = &healthTracker{catalog: service.catalog}
	admission, err := service.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{
		Name: "tools", ExpectedRegistrationToken: token,
	})
	require.NoError(t, err)
	assert.False(t, admission.Ready)
}

func TestReadSavedCatalogToolsetPreservesRetiredNativeDeclaration(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	input := testAgentToolset("revision/1")
	definition := testCatalogDefinition(t, &genregistry.Toolset{Name: input.Name, Tools: input.Tools})
	definition.identity = &CatalogIdentity{Scope: "example", Name: "assistant"}
	registered, err := catalog.RegisterAgent(ctx, definition, "")
	require.NoError(t, err)
	require.NoError(t, catalog.Retire(ctx, input.Name, registered.RegistrationToken))
	service := &Service{catalog: catalog}
	result, err := service.ReadSavedCatalogToolset(ctx, input.Name, 1<<20)
	require.NoError(t, err)
	assert.True(t, result.NativeAgent)
	assert.Equal(t, *definition.identity, result.Identity)
	assert.Equal(t, registered.RegistrationToken, result.Registration.RegistrationToken)
	assert.Equal(t, input.Tools, result.Registration.Toolset.Tools)
	assert.Nil(t, result.ServiceHealth)
	_, err = service.ReadCatalogToolset(ctx, input.Name, 1<<20)
	requireServiceErrorName(t, err, "not_found")
}

func TestReadSavedCatalogToolsetChecksRetiredRecordIntegrityAndBudget(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*testCatalogMap, *catalogState)
		budget int64
		want   string
	}{
		{name: "at byte budget"},
		{name: "below byte budget", budget: -1, want: "budget"},
		{name: "nonpositive budget", budget: -2, want: "positive"},
		{name: "missing", change: func(store *testCatalogMap, _ *catalogState) {
			delete(store.content, toolsetCatalogKey("tools"))
			delete(store.definitions, toolsetCatalogKey("tools"))
		}, want: "not_found"},
		{name: "missing identity", change: func(_ *testCatalogMap, state *catalogState) {
			state.Identity = nil
		}, want: "identity"},
		{name: "missing retirement history", change: func(store *testCatalogMap, state *catalogState) {
			delete(store.retiredTokens, state.RegistrationToken)
		}, want: "error"},
		{name: "changed fingerprint", change: func(_ *testCatalogMap, state *catalogState) {
			state.SchemaFingerprint = strings.Repeat("f", 64)
		}, want: "error"},
		{name: "malformed declaration", change: func(store *testCatalogMap, _ *catalogState) {
			store.definitions[toolsetCatalogKey("tools")] += " {}"
		}, want: "error"},
		{name: "incomplete record", change: func(store *testCatalogMap, _ *catalogState) {
			delete(store.definitions, toolsetCatalogKey("tools"))
		}, want: "error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store := testBoundedCatalogService(t, testDefinitionToolset())
			key := toolsetCatalogKey("tools")
			state, err := service.catalog.activeState(t.Context(), "tools")
			require.NoError(t, err)
			require.NoError(t, service.catalog.Retire(t.Context(), "tools", state.RegistrationToken))
			state, err = parseCatalogState("tools", store.content[key])
			require.NoError(t, err)
			if test.change != nil {
				test.change(store, &state)
				if test.name != "missing" {
					store.content[key], err = marshalCatalogState(state)
					require.NoError(t, err)
				}
			}
			budget := int64(len(store.content[key]) + len(store.definitions[key]))
			if test.name == "missing" {
				budget = 1
			}
			switch test.budget {
			case -1:
				budget--
			case -2:
				budget = 0
			}
			result, err := service.ReadSavedCatalogToolset(t.Context(), "tools", budget)
			if test.want == "" {
				require.NoError(t, err)
				assert.Equal(t, state.RegistrationToken, result.Registration.RegistrationToken)
				return
			}
			require.Error(t, err)
			assert.Nil(t, result)
			switch test.want {
			case "budget":
				require.ErrorIs(t, err, ErrCatalogReadBudget)
			case "identity":
				require.ErrorIs(t, err, ErrCatalogIdentityMissing)
			case "not_found":
				requireServiceErrorName(t, err, "not_found")
			}
		})
	}
}

func TestReadSavedCatalogToolsetRejectsCanceledRead(t *testing.T) {
	t.Parallel()
	service, _ := testBoundedCatalogService(t, testDefinitionToolset())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	result, err := service.ReadSavedCatalogToolset(ctx, "tools", 1<<20)
	assert.Nil(t, result)
	assert.ErrorIs(t, err, context.Canceled)
}

func TestStrictCatalogReadsKeepRetiredMissingIdentityError(t *testing.T) {
	t.Parallel()
	service, store := testBoundedCatalogService(t, testDefinitionToolset())
	ctx := t.Context()
	state, err := service.catalog.activeState(ctx, "tools")
	require.NoError(t, err)
	require.NoError(t, service.catalog.Retire(ctx, "tools", state.RegistrationToken))
	key := toolsetCatalogKey("tools")
	state, err = parseCatalogState("tools", store.content[key])
	require.NoError(t, err)
	state.Identity = nil
	store.content[key], err = marshalCatalogState(state)
	require.NoError(t, err)
	_, err = service.ReadCatalogToolset(ctx, "tools", 1<<20)
	requireServiceErrorName(t, err, "not_found")
	_, err = (&CatalogReader{catalog: service.catalog}).ReadCatalogToolset(ctx, "tools", 1<<20)
	requireServiceErrorName(t, err, "not_found")
	_, err = service.ReadSavedCatalogToolset(ctx, "tools", 1<<20)
	assert.ErrorIs(t, err, ErrCatalogIdentityMissing)
}
