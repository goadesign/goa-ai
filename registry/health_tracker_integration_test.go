//go:build integration

package registry

// These tests use Redis to verify the health scheduler behavior shared by
// multiple Tool Registry processes.

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

func TestHealthTrackerReportsActiveCatalogWhenAnotherReplicaOwnsPingLease(t *testing.T) {
	recorder := newHealthSpanRecorder(t)
	ctx := context.Background()
	catalog := newToolsetCatalog(
		newTestCatalogMap(),
		newTestTimeSource(time.Unix(1_700_000_000, 0)),
	)
	_, err := catalog.Register(
		ctx,
		testCatalogToolset("test.toolset", "test", nil),
		testAdmissionRevisionA,
		"provider-a",
		uuid.NewString(),
		time.Minute,
	)
	require.NoError(t, err)

	tracker := newDirectHealthTracker(ctx, catalog)
	tracker.redis = getRedis(t)
	tracker.leaseScope = "test-" + uuid.NewString()
	require.NoError(t, tracker.redis.Set(
		ctx,
		tracker.pingLeaseKey("test.toolset"),
		"other-replica",
		time.Minute,
	).Err())

	ctx, span := otel.Tracer("goa.design/goa-ai/registry").Start(
		ctx,
		"toolregistry.health.sweep",
		trace.WithAttributes(attribute.String("toolregistry.registry", tracker.leaseScope)),
	)
	tracker.pingRegisteredToolsets(ctx)
	span.End()

	require.Len(t, recorder.Ended(), 2)
	assertCatalogEntrySpanForRegistry(t, recorder.Ended()[0], tracker.leaseScope, "test.toolset")
	assert.Equal(t, "toolregistry.health.sweep", recorder.Ended()[1].Name())
}

// assertCatalogEntrySpanForRegistry verifies the catalog name reported by one
// registry process when another process owns the provider ping for that name.
func assertCatalogEntrySpanForRegistry(
	t *testing.T,
	span sdktrace.ReadOnlySpan,
	registryName, toolset string,
) {
	t.Helper()
	attrs := healthSpanAttributes(span)
	assert.Equal(t, "toolregistry.catalog.entry", span.Name())
	assert.Equal(t, registryName, attrs["toolregistry.registry"].AsString())
	assert.Equal(t, toolset, attrs["toolregistry.toolset"].AsString())
}
