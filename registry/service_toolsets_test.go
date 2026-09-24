package registry

// Declaration and attachment tests distinguish immutable saved definitions from
// lease membership. Redis and generated-transport tests exercise the same rules.

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	goa "goa.design/goa/v3/pkg"
)

func TestServiceDeclarationRetainsWinnerWithoutProviderWork(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	store := newTestCatalogMap(clock)
	// Missing stream and health dependencies make any accidental provider work
	// fail. Declaration only needs the catalog and its schema validator.
	svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
	input := testServiceDeclaration()
	first, err := svc.DeclareServiceToolset(t.Context(), input)
	require.NoError(t, err)
	state, err := svc.catalog.snapshot(t.Context(), input.Name)
	require.NoError(t, err)
	_, err = uuid.Parse(state.AdmissionRevision)
	require.NoError(t, err)
	require.NotNil(t, state.ProviderLeases)
	assert.Empty(t, state.ProviderLeases)
	assert.Positive(t, state.HealthEpoch)
	assert.Zero(t, state.LastPongUnixNano)
	before, err := json.Marshal(state.catalogState)
	require.NoError(t, err)
	clock.Set(now.Add(time.Hour))
	slices.Reverse(input.Tags)
	slices.Reverse(input.Tools)
	retry, err := svc.DeclareServiceToolset(t.Context(), input)
	require.NoError(t, err)
	assert.Equal(t, first, retry)
	state, err = svc.catalog.snapshot(t.Context(), input.Name)
	require.NoError(t, err)
	after, err := json.Marshal(state.catalogState)
	require.NoError(t, err)
	assert.JSONEq(t, string(before), string(after))
	assert.Equal(t, 1, store.definitionWrites)
	require.NoError(t, newToolsetCatalog(store, clock).validatePersistedEntries(t.Context()))

	require.NoError(t, svc.catalog.Retire(t.Context(), input.Name, first.RegistrationToken))
	_, err = svc.DeclareServiceToolset(t.Context(), input)
	requireServiceErrorName(t, err, "admission_retired")
}

func TestServiceDeclarationRejectsIncompleteOrChangedContracts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*genregistry.ServiceToolsetDeclaration)
	}{
		{"schema only", func(p *genregistry.ServiceToolsetDeclaration) { p.Tools[0].ConsumerContract = nil }},
		{"agent", func(p *genregistry.ServiceToolsetDeclaration) { p.Tools = testAgentToolset("revision/1").Tools }},
		{"unqualified", func(p *genregistry.ServiceToolsetDeclaration) { p.Tools[0].Name = "lookup" }},
		{"duplicate", func(p *genregistry.ServiceToolsetDeclaration) { p.Tools[1].Name = p.Tools[0].Name }},
		{"invalid schema", func(p *genregistry.ServiceToolsetDeclaration) {
			p.Tools[0].ExecutionPayloadSchema = []byte(`{"type":"missing"}`)
		}},
		{"missing pagination partner", func(p *genregistry.ServiceToolsetDeclaration) {
			continuation := "inventory.continue"
			p.Tools[0].ConsumerContract.Bounds = &genregistry.ToolBounds{Paging: &genregistry.ToolPaging{
				CursorField: "cursor", NextCursorField: "next_cursor", ContinueTool: &continuation,
			}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
			store := newTestCatalogMap(clock)
			svc := &Service{catalog: newToolsetCatalog(store, clock), validator: newSchemaValidator()}
			input := testServiceDeclaration()
			tc.change(input)
			_, err := svc.DeclareServiceToolset(t.Context(), input)
			requireServiceErrorName(t, err, "validation_error")
			assert.Empty(t, store.content)
			assert.Empty(t, store.definitions)
		})
	}
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	svc := &Service{catalog: newToolsetCatalog(newTestCatalogMap(clock), clock), validator: newSchemaValidator()}
	first, err := svc.DeclareServiceToolset(t.Context(), testServiceDeclaration())
	require.NoError(t, err)
	changed := testServiceDeclaration()
	changed.Tools[0].ConsumerContract.Title = "Different instruction"
	_, err = svc.DeclareServiceToolset(t.Context(), changed)
	requireServiceErrorName(t, err, "admission_conflict")
	saved, err := svc.ResolveToolset(t.Context(), &genregistry.GetToolsetPayload{Name: changed.Name})
	require.NoError(t, err)
	assert.Equal(t, first, saved)
}

func TestAttachProviderPreservesDefinitionTimeAndLongerLease(t *testing.T) {
	ctx := t.Context()
	now := time.Unix(1_700_000_000, 0)
	clock := newTestTimeSource(now)
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	first, err := catalog.DeclareService(ctx, testCatalogDefinition(t, testCatalogToolset("inventory", "inventory", nil)))
	require.NoError(t, err)
	reads, writes := store.snapshotReads, store.definitionWrites
	attached, err := catalog.AttachProvider(ctx, "inventory", first.RegistrationToken, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, reads, store.snapshotReads)
	assert.Equal(t, writes, store.definitionWrites)
	assert.Equal(t, first.RegisteredAt, attached.RegisteredAt)
	assert.Equal(t, first.HealthEpoch+1, attached.HealthEpoch)
	require.NoError(t, catalog.RecordPong(ctx, "inventory", "provider", testIncarnationA, first.RegistrationToken, attached.HealthEpoch))
	require.NoError(t, catalog.RenewProvider(ctx, "inventory", "provider", testIncarnationA, first.RegistrationToken, 5*time.Minute))
	again, err := catalog.AttachProvider(ctx, "inventory", first.RegistrationToken, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	assert.Equal(t, now.Add(5*time.Minute).UnixMilli(), again.ProviderLeases[providerLeaseKey("provider", testIncarnationA)].ExpiresAtUnixMilli)
	assert.Equal(t, attached.HealthEpoch, again.HealthEpoch)
	assert.NotZero(t, again.LastPongUnixNano)
	require.NoError(t, catalog.DrainProvider(ctx, "inventory", "provider", testIncarnationA, first.RegistrationToken, time.Minute))
	_, err = catalog.AttachProvider(ctx, "inventory", first.RegistrationToken, "provider", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errProviderLeaseLost)
	clock.Set(now.Add(6 * time.Minute))
	_, err = catalog.AttachProvider(ctx, "inventory", first.RegistrationToken, "provider", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errProviderLeaseLost, "draining is checked before expiration pruning")
	next, err := catalog.AttachProvider(ctx, "inventory", first.RegistrationToken, "provider", testIncarnationB, time.Minute)
	require.NoError(t, err)
	assert.Zero(t, next.LastPongUnixNano)
	assert.Greater(t, next.HealthEpoch, again.HealthEpoch)
	assert.Equal(t, first.RegisteredAt, next.RegisteredAt)
	assert.Len(t, next.ProviderLeases, 1)
}

func TestServiceLifecycleRejectsForeignSelectionAndDependencyFailures(t *testing.T) {
	ctx := t.Context()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	svc := &Service{catalog: catalog, validator: newSchemaValidator()}
	_, err := catalog.AttachProvider(ctx, "missing", testStaleToken, "provider", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errAdmissionConflict)
	native, err := svc.RegisterAgentToolset(ctx, testAgentToolset("revision/1"))
	require.NoError(t, err)
	_, err = catalog.AttachProvider(ctx, native.Toolset.Name, native.RegistrationToken, "provider", testIncarnationA, time.Minute)
	require.ErrorIs(t, err, errAdmissionConflict)
	input := testServiceDeclaration()
	input.Name = native.Toolset.Name
	_, err = svc.DeclareServiceToolset(ctx, input)
	requireServiceErrorName(t, err, "admission_conflict")
	store.snapshotErr = errors.New("catalog unavailable")
	_, err = svc.DeclareServiceToolset(ctx, testServiceDeclaration())
	requireServiceErrorName(t, err, "service_unavailable")
	store.snapshotErr = nil
	store.commitErr = errors.New("catalog unavailable")
	_, err = svc.DeclareServiceToolset(ctx, testServiceDeclaration())
	requireServiceErrorName(t, err, "service_unavailable")
}

func testServiceDeclaration() *genregistry.ServiceToolsetDeclaration {
	schema := []byte(`{"type":"object","properties":{"value":{"type":"integer"}},"required":["value"],"additionalProperties":false}`)
	declaration := &genregistry.ServiceToolsetDeclaration{Name: "inventory", Tags: []string{"stock", "warehouse"}}
	for _, name := range []string{"inventory.lookup", "inventory.list"} {
		declaration.Tools = append(declaration.Tools, &genregistry.ToolSchema{
			Name: name, PayloadSchema: schema, ExecutionPayloadSchema: schema, ResultSchema: schema,
			ConsumerContract: &genregistry.ConsumerContract{
				Kind: "service", Title: "Read inventory",
				Search:  &genregistry.ToolSearchDocument{Length: 1, Terms: map[string]int{"inventory": 1}},
				Payload: &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: schema},
				Result:  &genregistry.ToolTypeMetadata{SchemaWithoutRootExample: schema},
			},
		})
	}
	return declaration
}

func requireServiceErrorName(t *testing.T, err error, name string) {
	t.Helper()
	var serviceErr *goa.ServiceError
	require.ErrorAs(t, err, &serviceErr)
	require.Equal(t, name, serviceErr.Name)
}
