// Catalog owners inspect exact active registration facts without reading
// declarations. These tests retain identity reads after retirement and exercise
// saved-state validation independently of the declaration bytes.
package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCatalogRegistrationTracksReplacementAndRetirementWithoutDefinitions(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	service := &Service{catalog: catalog}
	definition := testCatalogDefinition(t, testCatalogToolset("route", "tool", nil))
	definition.identity = &CatalogIdentity{Scope: "owner", Name: "records"}
	first, err := catalog.Register(t.Context(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	got, err := service.ReadCatalogRegistration(t.Context(), "route")
	require.NoError(t, err)
	assert.Equal(t, CatalogRegistration{Identity: *definition.identity, RegistrationToken: first.RegistrationToken}, got)

	clock.Set(time.Unix(1_700_000_060, 0))
	second, err := catalog.Register(t.Context(), definition, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	store.definitions[toolsetCatalogKey("route")] = "invalid definition bytes"
	got, err = service.ReadCatalogRegistration(t.Context(), "route")
	require.NoError(t, err)
	assert.NotEqual(t, first.RegistrationToken, got.RegistrationToken)
	assert.Equal(t, second.RegistrationToken, got.RegistrationToken)
	assert.Equal(t, *definition.identity, got.Identity)

	require.NoError(t, catalog.Retire(t.Context(), "route", second.RegistrationToken))
	_, err = service.ReadCatalogRegistration(t.Context(), "route")
	requireServiceErrorName(t, err, "not_found")
	identity, err := service.ReadCatalogIdentity(t.Context(), "route")
	require.NoError(t, err)
	assert.Equal(t, *definition.identity, identity)
	assert.Zero(t, store.snapshotReads)
}

func TestReadCatalogRegistrationIdentifiesNativeAgentWithoutDefinitions(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	service := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	identity := CatalogIdentity{Scope: "owner", Name: "assistant"}
	registered, err := service.RegisterAgentToolsetWithIdentity(t.Context(), identity, testAgentToolset("revision/1"))
	require.NoError(t, err)
	store.definitions[toolsetCatalogKey(registered.Toolset.Name)] = "invalid definition bytes"
	got, err := service.ReadCatalogRegistration(t.Context(), registered.Toolset.Name)
	require.NoError(t, err)
	assert.Equal(t, CatalogRegistration{Identity: identity, RegistrationToken: registered.RegistrationToken, NativeAgent: true}, got)
	assert.Zero(t, store.snapshotReads)
}

func TestReadCatalogRegistrationRejectsAbsentUnownedAndCorruptState(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	service := &Service{catalog: catalog}
	_, err := service.ReadCatalogRegistration(t.Context(), "missing")
	requireServiceErrorName(t, err, "not_found")
	definition := testCatalogDefinition(t, testCatalogToolset("route", "tool", nil))
	_, err = catalog.Register(t.Context(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	_, err = service.ReadCatalogRegistration(t.Context(), "route")
	require.ErrorIs(t, err, ErrCatalogIdentityMissing)
	store.content[toolsetCatalogKey("route")] = `{"identity":{"scope":"owner"}}`
	_, err = service.ReadCatalogRegistration(t.Context(), "route")
	requireServiceErrorName(t, err, "validation_error")
	assert.Zero(t, store.snapshotReads)
}
