package registry

import (
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

const (
	testServiceConsumerKind = "service"
	testServiceUnavailable  = "service_unavailable"
)

func TestLookupAgentRegistrationReturnsMatchingAdmissionWithoutWriting(t *testing.T) {
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	identity := CatalogIdentity{Scope: "project", Name: "Public name"}
	input := testAgentToolset("revision/1")
	input.Tags = []string{"first", "second"}
	missing, found, err := svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
	require.NoError(t, err)
	require.False(t, found)
	require.Nil(t, missing)
	require.Empty(t, store.content)
	saved, err := svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, input)
	require.NoError(t, err)
	beforeState, beforeDefinition := maps.Clone(store.content), maps.Clone(store.definitions)
	beforeWrites := store.definitionWrites
	store.beforeCommit = func(string, catalogWrite) { t.Fatal("lookup attempted to commit") }
	clock.Set(time.Unix(1_700_001_000, 0))
	// Registration fingerprints ignore tag order. Lookup uses that same rule
	// and returns the matching input with the saved registration time.
	input.Tags = []string{"second", "first"}
	matched, found, err := svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, matched)
	require.Equal(t, saved.RegistrationToken, matched.RegistrationToken)
	require.Equal(t, saved.Toolset.RegisteredAt, matched.Toolset.RegisteredAt)
	require.Equal(t, input.Tags, matched.Toolset.Tags)
	require.Equal(t, beforeState, store.content)
	require.Equal(t, beforeDefinition, store.definitions)
	require.Equal(t, beforeWrites, store.definitionWrites)
	require.Zero(t, store.snapshotReads, "lookup reads no stored definition")
	matched.Toolset.Tools[0].ConsumerContract.Title = "caller copy"
	again, found, err := svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
	require.NoError(t, err)
	require.True(t, found)
	require.NotNil(t, again)
	require.Equal(t, input.Tools[0].ConsumerContract.Title, again.Toolset.Tools[0].ConsumerContract.Title)
}

func TestLookupAgentRegistrationUsesTheCreateConflictRules(t *testing.T) {
	for _, scenario := range []string{"target", "metadata", "scope", "name", "unowned", testServiceConsumerKind, "retired"} {
		t.Run(scenario, func(t *testing.T) {
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
			identity := CatalogIdentity{Scope: "project", Name: "Public name"}
			input := testAgentToolset("revision/1")
			var saved *genregistry.ResolvedToolset
			var err error
			switch scenario {
			case "unowned":
				saved, err = svc.RegisterAgentToolset(t.Context(), input)
			case testServiceConsumerKind:
				service := testServiceDeclaration()
				service.Name = input.Name
				saved, err = svc.DeclareServiceToolsetWithIdentity(t.Context(), identity, service)
			default:
				saved, err = svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, input)
			}
			require.NoError(t, err)
			switch scenario {
			case "target":
				input = testAgentToolset("revision/2")
			case "metadata":
				input.Tools[0].ConsumerContract.Title = "Changed purpose"
			case "scope":
				identity.Scope = "other"
			case "name":
				identity.Name = "Other public name"
			case "retired":
				require.NoError(t, svc.catalog.Retire(t.Context(), input.Name, saved.RegistrationToken))
			}
			beforeState, beforeDefinition := maps.Clone(store.content), maps.Clone(store.definitions)
			matched, found, err := svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
			requireServiceErrorName(t, err, "admission_conflict")
			require.False(t, found)
			require.Nil(t, matched)
			_, err = svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, input)
			requireServiceErrorName(t, err, "admission_conflict")
			require.Equal(t, beforeState, store.content)
			require.Equal(t, beforeDefinition, store.definitions)
		})
	}
}

func TestLookupAgentRegistrationUsesRegistrationValidationAndStorageErrors(t *testing.T) {
	for _, scenario := range []string{"identity", "native kind", "schema", "read", "persisted"} {
		t.Run(scenario, func(t *testing.T) {
			store := newTestCatalogMap()
			svc := &Service{catalog: newToolsetCatalog(store, store.clock), validator: newSchemaValidator()}
			identity := CatalogIdentity{Scope: "project", Name: "Public name"}
			input := testAgentToolset("revision/1")
			expected := "validation_error"
			switch scenario {
			case "identity":
				identity.Scope = ""
			case "native kind":
				input.Tools[0].ConsumerContract.Kind = testServiceConsumerKind
				input.Tools[0].ConsumerContract.Agent = nil
			case "schema":
				input.Tools[0].ExecutionPayloadSchema = []byte(`{"type":"invalid"}`)
			case "read":
				store.readErr = errors.New("storage read failed")
				expected = testServiceUnavailable
			case "persisted":
				_, err := svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, input)
				require.NoError(t, err)
				store.content[toolsetCatalogKey(input.Name)] = "{invalid"
				expected = testServiceUnavailable
			}
			matched, found, err := svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
			requireServiceErrorName(t, err, expected)
			require.False(t, found)
			require.Nil(t, matched)
			if scenario == "read" {
				store.readErr = errors.New("storage read failed")
			}
			_, err = svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, input)
			requireServiceErrorName(t, err, expected)
		})
	}
}

func TestAgentRegistrationRechecksACompetitorAfterAbsentLookup(t *testing.T) {
	for _, scenario := range []string{"identical", "different", "retired"} {
		t.Run(scenario, func(t *testing.T) {
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
			identity := CatalogIdentity{Scope: "project", Name: "Public name"}
			input := testAgentToolset("revision/1")
			missing, found, err := svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
			require.NoError(t, err)
			require.False(t, found)
			require.Nil(t, missing)
			var winner *genregistry.ResolvedToolset
			store.beforeCommit = func(string, catalogWrite) {
				store.beforeCommit = nil
				clock.Set(time.Unix(1_700_000_001, 0))
				competing := testAgentToolset("revision/1")
				if scenario == "different" {
					competing = testAgentToolset("revision/2")
				}
				var err error
				winner, err = svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, competing)
				require.NoError(t, err)
				if scenario == "retired" {
					require.NoError(t, svc.catalog.Retire(t.Context(), input.Name, winner.RegistrationToken))
				}
			}
			registered, err := svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, input)
			if scenario == "identical" {
				require.NoError(t, err)
				require.Equal(t, winner.RegistrationToken, registered.RegistrationToken)
				require.Equal(t, winner.Toolset.RegisteredAt, registered.Toolset.RegisteredAt)
			} else {
				requireServiceErrorName(t, err, "admission_conflict")
			}
			require.Equal(t, 1, store.definitionWrites, "the losing attempt must not rewrite the winner")
		})
	}
}

func TestMatchingAgentLookupDoesNotReserveAgainstRetirement(t *testing.T) {
	store := newTestCatalogMap()
	svc := &Service{catalog: newToolsetCatalog(store, store.clock), validator: newSchemaValidator()}
	identity := CatalogIdentity{Scope: "project", Name: "Public name"}
	input := testAgentToolset("revision/1")
	saved, err := svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, input)
	require.NoError(t, err)
	store.afterExactRead = func(string) {
		store.afterExactRead = nil
		require.NoError(t, svc.catalog.Retire(t.Context(), input.Name, saved.RegistrationToken))
	}
	matched, found, err := svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
	require.NoError(t, err, "lookup returns the active record it observed")
	require.True(t, found)
	require.Equal(t, saved, matched)
	matched, found, err = svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
	requireServiceErrorName(t, err, "admission_conflict")
	require.False(t, found)
	require.Nil(t, matched)
	_, err = svc.RegisterAgentToolsetWithIdentity(t.Context(), identity, input)
	requireServiceErrorName(t, err, "admission_conflict")
	// Explicit same-token replacement remains the existing reactivation path.
	replaced, err := svc.ReplaceAgentToolsetWithIdentity(t.Context(), identity, &genregistry.ReplaceAgentToolsetPayload{
		Name: input.Name, Tools: input.Tools, ExpectedRegistrationToken: saved.RegistrationToken,
	})
	require.NoError(t, err)
	matched, found, err = svc.LookupAgentToolsetRegistration(t.Context(), identity, input)
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, replaced, matched)
}
