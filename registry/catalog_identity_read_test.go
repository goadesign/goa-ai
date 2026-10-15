package registry

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReadCatalogIdentitySurvivesReplacementAndRetirementWithoutReadingDefinition(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	service := &Service{catalog: catalog}
	declaration := testCatalogDefinition(t, testCatalogToolset("route", "tool", nil))
	declaration.identity = &CatalogIdentity{Scope: "owner", Name: "resource"}
	first, err := catalog.Register(t.Context(), declaration, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	identity, err := service.ReadCatalogIdentity(t.Context(), "route")
	require.NoError(t, err)
	assert.Equal(t, *declaration.identity, identity)
	clock.Set(time.Unix(1_700_000_060, 0))
	second, err := catalog.Register(t.Context(), declaration, testAdmissionRevisionB, "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.NotEqual(t, first.RegistrationToken, second.RegistrationToken)
	identity, err = service.ReadCatalogIdentity(t.Context(), "route")
	require.NoError(t, err)
	assert.Equal(t, *declaration.identity, identity)
	require.NoError(t, catalog.Retire(t.Context(), "route", second.RegistrationToken))
	store.definitions[toolsetCatalogKey("route")] = `invalid definition bytes`
	identity, err = service.ReadCatalogIdentity(t.Context(), "route")
	require.NoError(t, err)
	assert.Equal(t, *declaration.identity, identity)
	assert.Zero(t, store.snapshotReads)
	changed := *declaration.identity
	changed.Scope = "other"
	require.ErrorIs(t, requireCatalogIdentity(declaration.identity, &changed), errAdmissionConflict)
}

func TestReadCatalogIdentityRejectsAbsentUnownedAndCorruptState(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	service := &Service{catalog: catalog}
	_, err := service.ReadCatalogIdentity(t.Context(), "missing")
	require.Error(t, err)
	declaration := testCatalogDefinition(t, testCatalogToolset("route", "tool", nil))
	_, err = catalog.Register(t.Context(), declaration, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	_, err = service.ReadCatalogIdentity(t.Context(), "route")
	require.ErrorIs(t, err, ErrCatalogIdentityMissing)
	store.content[toolsetCatalogKey("route")] = `{"identity":{"scope":"owner"}}`
	_, err = service.ReadCatalogIdentity(t.Context(), "route")
	require.Error(t, err)
	assert.Zero(t, store.snapshotReads)
}
