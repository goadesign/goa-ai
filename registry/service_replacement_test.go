// Service replacement tests verify publication without provider creation,
// safe replay after a lost reply, and exclusion of still-live old providers.
package registry

import (
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestServiceReplacementReplayAndFreshRollback(t *testing.T) {
	t.Parallel()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	original := testServiceDeclaration()
	first, err := svc.DeclareServiceToolset(t.Context(), original)
	require.NoError(t, err)
	changed := testServiceDeclaration()
	version := genregistry.SemVer("1.1.0")
	changed.Version = &version
	request := serviceReplacementRequest(changed, first.RegistrationToken)
	second, err := svc.ReplaceServiceToolset(t.Context(), request)
	require.NoError(t, err)
	assert.NotEqual(t, first.RegistrationToken, second.RegistrationToken)
	retired, err := store.Retired(t.Context(), first.RegistrationToken)
	require.NoError(t, err)
	assert.True(t, retired)
	state, err := svc.catalog.snapshot(t.Context(), original.Name)
	require.NoError(t, err)
	assert.Empty(t, state.ProviderLeases)
	assert.Zero(t, state.LastPongUnixNano)

	// A repeated update returns the committed winner, even after the caller
	// changes equivalent ordering and the registry clock advances.
	clock.Set(time.Unix(1_700_000_100, 0))
	slices.Reverse(request.Tools)
	slices.Reverse(request.Tags)
	replayed, err := svc.ReplaceServiceToolset(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, second, replayed)
	request.Tools[0].ConsumerContract.Title = "Changed replacement intent"
	_, err = svc.ReplaceServiceToolset(t.Context(), request)
	requireServiceErrorName(t, err, "admission_conflict")
	saved, err := svc.ResolveToolset(t.Context(), &genregistry.GetToolsetPayload{Name: original.Name})
	require.NoError(t, err)
	assert.Equal(t, second, saved)

	// Restoring the original schema publishes a new registration rather than
	// reviving the retired provider authority of the original deployment.
	rollback, err := svc.ReplaceServiceToolset(t.Context(), serviceReplacementRequest(original, second.RegistrationToken))
	require.NoError(t, err)
	assert.NotEqual(t, first.RegistrationToken, rollback.RegistrationToken)
	assert.NotEqual(t, second.RegistrationToken, rollback.RegistrationToken)
	_, err = svc.ReplaceServiceToolset(t.Context(), serviceReplacementRequest(changed, first.RegistrationToken))
	requireServiceErrorName(t, err, "admission_conflict")
}

func TestServiceReplacementWaitsForEveryOldLease(t *testing.T) {
	for _, test := range []struct {
		name     string
		draining bool
		released bool
		elapsed  time.Duration
		blocked  bool
	}{
		{name: "active", blocked: true},
		{name: "draining", draining: true, blocked: true},
		{name: "before expiry", elapsed: time.Minute - time.Millisecond, blocked: true},
		{name: "at expiry", elapsed: time.Minute},
		{name: "after expiry", elapsed: time.Minute + time.Millisecond},
		{name: "released", released: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Unix(1_700_000_000, 0)
			clock := newTestTimeSource(now)
			store := newTestCatalogMap(clock)
			svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
			declaration := testServiceDeclaration()
			first, err := svc.DeclareServiceToolset(t.Context(), declaration)
			require.NoError(t, err)
			_, err = svc.catalog.AttachProvider(t.Context(), declaration.Name, first.RegistrationToken, "provider", testIncarnationA, time.Minute)
			require.NoError(t, err)
			if test.draining {
				require.NoError(t, svc.catalog.DrainProvider(t.Context(), declaration.Name, "provider", testIncarnationA, first.RegistrationToken, time.Minute))
			}
			if test.released {
				require.NoError(t, svc.catalog.ReleaseProvider(t.Context(), declaration.Name, "provider", testIncarnationA, first.RegistrationToken))
			}
			clock.Set(now.Add(test.elapsed))
			request := serviceReplacementRequest(testServiceDeclaration(), first.RegistrationToken)
			result, err := svc.ReplaceServiceToolset(t.Context(), request)
			if test.blocked {
				requireServiceErrorName(t, err, "admission_blocked")
				assert.Nil(t, result)
				saved, readErr := svc.ResolveToolset(t.Context(), &genregistry.GetToolsetPayload{Name: declaration.Name})
				require.NoError(t, readErr)
				assert.Equal(t, first, saved)
				retired, readErr := store.Retired(t.Context(), first.RegistrationToken)
				require.NoError(t, readErr)
				assert.False(t, retired)
				return
			}
			require.NoError(t, err)
			assert.NotEqual(t, first.RegistrationToken, result.RegistrationToken)
		})
	}
}

func TestServiceReplacementLosesToConcurrentAttachment(t *testing.T) {
	t.Parallel()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	svc := &Service{catalog: catalog, validator: newSchemaValidator()}
	declaration := testServiceDeclaration()
	first, err := svc.DeclareServiceToolset(t.Context(), declaration)
	require.NoError(t, err)
	store.beforeCommit = func(_ string, write catalogWrite) {
		if write.RetireToken != first.RegistrationToken {
			return
		}
		store.mu.Lock()
		store.beforeCommit = nil
		store.mu.Unlock()
		_, attachErr := catalog.AttachProvider(t.Context(), declaration.Name, first.RegistrationToken, "late-provider", testIncarnationA, time.Minute)
		require.NoError(t, attachErr)
	}
	_, err = svc.ReplaceServiceToolset(t.Context(), serviceReplacementRequest(testServiceDeclaration(), first.RegistrationToken))
	requireServiceErrorName(t, err, "admission_blocked")
	saved, err := svc.ResolveToolset(t.Context(), &genregistry.GetToolsetPayload{Name: declaration.Name})
	require.NoError(t, err)
	assert.Equal(t, first, saved)
}

func TestServiceReplacementPreservesExplicitIdentity(t *testing.T) {
	t.Parallel()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	identity := CatalogIdentity{Scope: "tenant-a", Name: "inventory"}
	declaration := testServiceDeclaration()
	first, err := svc.DeclareServiceToolsetWithIdentity(t.Context(), identity, declaration)
	require.NoError(t, err)
	request := serviceReplacementRequest(testServiceDeclaration(), first.RegistrationToken)
	_, err = svc.ReplaceServiceToolset(t.Context(), request)
	requireServiceErrorName(t, err, "admission_conflict")
	_, err = svc.ReplaceServiceToolsetWithIdentity(t.Context(), CatalogIdentity{Scope: "tenant-b", Name: "inventory"}, request)
	requireServiceErrorName(t, err, "admission_conflict")
	second, err := svc.ReplaceServiceToolsetWithIdentity(t.Context(), identity, request)
	require.NoError(t, err)
	state, err := svc.catalog.snapshot(t.Context(), declaration.Name)
	require.NoError(t, err)
	assert.Equal(t, &identity, state.Identity)
	assert.Equal(t, second.RegistrationToken, state.RegistrationToken)
}

// serviceReplacementRequest gives each new test deployment an independent
// replacement ID while preserving the complete supplied declaration.
func serviceReplacementRequest(declaration *genregistry.ServiceToolsetDeclaration, expected string) *genregistry.ReplaceServiceToolsetPayload {
	return &genregistry.ReplaceServiceToolsetPayload{
		Name: declaration.Name, Description: declaration.Description,
		Version: declaration.Version, Tags: declaration.Tags, Tools: declaration.Tools,
		ExpectedRegistrationToken: expected, ReplacementID: uuid.NewString(),
	}
}
