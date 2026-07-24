//go:build integration

package registry

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/provider"
	"goa.design/pulse/rmap"
)

// TestProviderRecoversAfterRedisStateLoss reproduces the production incident
// end to end with real Pulse streams: a registered provider is serving and
// healthy, then Redis loses all state (catalog, health, leases, consumer
// groups). The provider's ensure loop must recreate its consumer group and
// re-assert its registration so ping delivery, pongs, and durable catalog
// state all recover without restarting anything.
func TestProviderRecoversAfterRedisStateLoss(t *testing.T) {
	rdb := getRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	if err != nil {
		t.Fatalf("create pulse client: %v", err)
	}

	healthMapName := "health-e2e-" + t.Name()
	registryMapName := "registry-e2e-" + t.Name()
	healthMap, err := rmap.Join(ctx, healthMapName, rdb)
	require.NoError(t, err)
	defer healthMap.Close()
	registryMap, err := rmap.Join(ctx, registryMapName, rdb)
	require.NoError(t, err)
	defer registryMap.Close()

	streamManager := NewStreamManager(pulseClient)
	tracker, err := NewHealthTracker(
		streamManager,
		healthMap,
		registryMap,
		rdb,
		WithPingInterval(50*time.Millisecond),
		WithMissedPingThreshold(2),
		WithPingLeaseScope("lease-e2e-"+t.Name()),
	)
	require.NoError(t, err)
	defer func() { _ = tracker.Close() }()

	svc, err := newService(serviceOptions{
		catalog:       newToolsetCatalog(registryMap),
		StreamManager: streamManager,
		HealthTracker: tracker,
		PulseClient:   pulseClient,
	})
	require.NoError(t, err)

	// Register the toolset like a provider deployment does at startup.
	toolset := "recovery-toolset"
	payload := validRegisterPayloadForSchemaAdmission(toolset)
	_, err = svc.Register(ctx, payload)
	require.NoError(t, err)

	// Run the provider loop with a fast ensure interval. Pong and
	// EnsureRegistration go through the real service methods, mirroring the
	// generated provider wiring.
	var pongs atomic.Int64
	serveErr := make(chan error, 1)
	go func() {
		serveErr <- provider.Serve(ctx, pulseClient, toolset, noopHandler{}, provider.Options{
			ProviderID: payload.ProviderID,
			Pong: func(ctx context.Context, providerID, pingID string) error {
				if err := svc.Pong(ctx, &genregistry.PongPayload{
					PingID:     pingID,
					Toolset:    toolset,
					ProviderID: providerID,
				}); err != nil {
					return err
				}
				pongs.Add(1)
				return nil
			},
			EnsureRegistration: func(ctx context.Context) error {
				_, err := svc.Register(ctx, payload)
				return err
			},
			EnsureInterval: 100 * time.Millisecond,
		})
	}()

	// The ping/pong loop must converge to healthy.
	require.Eventually(t, func() bool { return tracker.IsHealthy(toolset) }, 10*time.Second, 20*time.Millisecond,
		"toolset should become healthy from live ping/pong")

	// Redis loses everything: catalog hash, health hash, ping leases, the
	// toolset stream, and the provider's consumer group.
	require.NoError(t, rdb.FlushDB(ctx).Err())

	// Ping delivery must resume: the registry re-acquires its ping lease and
	// the provider's ensure loop recreates the consumer group. New pongs prove
	// the full loop is live again.
	pongsBeforeRecovery := pongs.Load()
	require.Eventually(t, func() bool { return pongs.Load() >= pongsBeforeRecovery+2 }, 10*time.Second, 20*time.Millisecond,
		"provider should pong post-flush pings after consumer group repair")
	require.Eventually(t, func() bool { return tracker.IsHealthy(toolset) }, 10*time.Second, 20*time.Millisecond,
		"toolset should be healthy again after recovery")

	// The re-registration must have repaired durable catalog state: a fresh
	// replica joined from Redis (as a restarted registry node would) sees the
	// toolset.
	require.Eventually(t, func() bool {
		freshMap, err := rmap.Join(ctx, registryMapName, rdb)
		if err != nil {
			return false
		}
		defer freshMap.Close()
		_, ok := freshMap.Get(toolsetCatalogKey(toolset))
		return ok
	}, 10*time.Second, 100*time.Millisecond, "re-registration should restore the catalog entry in Redis")

	cancel()
	<-serveErr
}

// noopHandler satisfies provider.Handler for tests that never dispatch tool calls.
type noopHandler struct{}

func (noopHandler) HandleToolCall(_ context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	return toolregistry.NewToolResultErrorMessage(msg.ToolUseID, "unexpected", "no tool calls expected in this test"), nil
}
