// These tests follow bounded catalog reads from fresh stored bytes through
// validation and caller-owned decoding. Traces must describe each operation
// without changing which data the caller receives.
package registry

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	genregistry "goa.design/goa-ai/registry/gen/registry"
)

func TestReadCatalogToolsetSpans(t *testing.T) {
	for _, test := range []struct {
		name    string
		warm    bool
		changed bool
	}{
		{name: "cold"},
		{name: "warm", warm: true},
		{name: "changed bytes", warm: true, changed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store := testBoundedCatalogService(t, testDefinitionToolset())
			key := toolsetCatalogKey("tools")
			budget := int64(len(store.content[key]) + len(store.definitions[key]) + 1)
			if test.warm {
				_, err := service.ReadCatalogToolset(t.Context(), "tools", budget)
				require.NoError(t, err)
			}
			if test.changed {
				store.definitions[key] = "\n" + store.definitions[key]
			}
			before := store.snapshotReads
			recorder := newHealthSpanRecorder(t)
			ctx, parent := otel.Tracer("catalog-test").Start(t.Context(), "caller")
			result, err := service.ReadCatalogToolset(ctx, "tools", budget)
			parent.End()
			require.NoError(t, err)
			assert.Equal(t, "tools", result.Registration.Toolset.Name)
			assert.Equal(t, before+1, store.snapshotReads)
			spans := recorder.Ended()
			require.Len(t, spans, 6)
			byName := make(map[string]sdktrace.ReadOnlySpan, len(spans))
			for _, span := range spans {
				byName[span.Name()] = span
				assert.Empty(t, span.Events())
				if span.Name() != "caller" {
					assert.Equal(t, codes.Ok, span.Status().Code)
				}
			}
			for child, owner := range map[string]string{
				"toolregistry.catalog.read":                "caller",
				"toolregistry.catalog.snapshot.read":       "toolregistry.catalog.read",
				"toolregistry.catalog.snapshot.validate":   "toolregistry.catalog.read",
				"toolregistry.catalog.definition.validate": "toolregistry.catalog.snapshot.validate",
				"toolregistry.catalog.definition.decode":   "toolregistry.catalog.read",
			} {
				require.Contains(t, byName, child)
				require.Contains(t, byName, owner)
				assert.Equal(t, byName[owner].SpanContext().SpanID(), byName[child].Parent().SpanID())
				assert.Equal(t, parent.SpanContext().TraceID(), byName[child].SpanContext().TraceID())
			}
			readAttrs := healthSpanAttributes(byName["toolregistry.catalog.snapshot.read"])
			assert.Equal(t, int64(len(store.content[key])), readAttrs["toolregistry.catalog.state_read_bytes"].AsInt64())
			assert.Equal(t, int64(len(store.definitions[key])), readAttrs["toolregistry.catalog.definition_read_bytes"].AsInt64())
			validationAttrs := healthSpanAttributes(byName["toolregistry.catalog.definition.validate"])
			assert.Equal(t, test.warm && !test.changed, validationAttrs["toolregistry.catalog.definition_cache_hit"].AsBool())
		})
	}
}

func TestReadCatalogToolsetFailureSpans(t *testing.T) {
	for _, test := range []struct {
		name       string
		corruption string
		stage      string
	}{
		{name: "budget", stage: "toolregistry.catalog.snapshot.read"},
		{name: "invalid state", corruption: "state", stage: "toolregistry.catalog.snapshot.validate"},
		{name: "invalid definition", corruption: "definition", stage: "toolregistry.catalog.definition.validate"},
	} {
		t.Run(test.name, func(t *testing.T) {
			service, store := testBoundedCatalogService(t, testDefinitionToolset())
			key := toolsetCatalogKey("tools")
			switch test.corruption {
			case "state":
				state, err := parseCatalogState("tools", store.content[key])
				require.NoError(t, err)
				state.RegisteredAt = "invalid timestamp"
				state.Info.RegisteredAt = state.RegisteredAt
				store.content[key], err = marshalCatalogState(state)
				require.NoError(t, err)
			case "definition":
				store.definitions[key] += " {}"
			}
			budget := int64(len(store.content[key]) + len(store.definitions[key]))
			if test.corruption == "" {
				budget--
			}
			recorder := newHealthSpanRecorder(t)
			result, err := service.ReadCatalogToolset(t.Context(), "tools", budget)
			require.Error(t, err)
			assert.Nil(t, result)
			if test.corruption == "" {
				require.ErrorIs(t, err, ErrCatalogReadBudget)
			}
			failedStage, failedRead := false, false
			for _, span := range recorder.Ended() {
				assert.NotEqual(t, "toolregistry.catalog.definition.decode", span.Name())
				if span.Name() == test.stage || span.Name() == "toolregistry.catalog.read" {
					assert.Equal(t, codes.Error, span.Status().Code)
					require.Len(t, span.Events(), 1)
					assert.Equal(t, "exception", span.Events()[0].Name)
					failedStage = failedStage || span.Name() == test.stage
					failedRead = failedRead || span.Name() == "toolregistry.catalog.read"
				}
			}
			assert.True(t, failedStage)
			assert.True(t, failedRead)
		})
	}
}

func TestReadCatalogToolsetReturnsIndependentCopyAndFreshHealth(t *testing.T) {
	t.Parallel()
	input := testDefinitionToolset()
	service, store := testBoundedCatalogService(t, input)
	key := toolsetCatalogKey(input.Name)
	budget := int64(len(store.content[key]) + len(store.definitions[key]) + 1024)
	first, err := service.ReadCatalogToolset(t.Context(), input.Name, budget)
	require.NoError(t, err)
	mutateDefinitionResult(first.Registration.Toolset)
	clock := store.clock.(*testTimeSource)
	clock.Set(time.Unix(1_700_000_010, 0))
	service.catalogHealth = func(_ catalogState, now time.Time) ToolsetHealth {
		assert.Equal(t, clock.now, now)
		return ToolsetHealth{Healthy: true}
	}
	before := store.snapshotReads
	second, err := service.ReadCatalogToolset(t.Context(), input.Name, budget)
	require.NoError(t, err)
	assert.Equal(t, before+1, store.snapshotReads)
	input.RegisteredAt = second.Registration.Toolset.RegisteredAt
	assert.Equal(t, input, second.Registration.Toolset)
	assert.True(t, second.ServiceHealth.Healthy)
}

// testBoundedCatalogService admits a synthetic service declaration with explicit
// ownership and returns a reader whose health result uses the saved snapshot.
func testBoundedCatalogService(t testing.TB, input *genregistry.Toolset) (*Service, *testCatalogMap) {
	t.Helper()
	clock := newTestTimeSource(time.Unix(1_700_000_000, 0))
	store := newTestCatalogMap(clock)
	catalog := newToolsetCatalog(store, clock)
	definition := testCatalogDefinition(t, input)
	definition.identity = &CatalogIdentity{Scope: "example", Name: input.Name}
	_, err := catalog.Register(context.Background(), definition, testAdmissionRevisionA, "provider", testIncarnationA, time.Minute)
	require.NoError(t, err)
	catalog.forgetDefinition(input.Name)
	tracker := &healthTracker{stalenessThreshold: deriveStalenessThreshold(DefaultPingInterval, DefaultMissedPingThreshold)}
	return &Service{catalog: catalog, catalogHealth: tracker.healthFromEntry}, store
}
