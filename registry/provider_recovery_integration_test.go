//go:build integration

package registry

// End-to-end reproduction of the Redis state-loss incident against real Pulse
// streams and the full registry: catalog, leases, ping scheduling, consumer
// group, and provider supervision must all recover without restarting any
// process.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/telemetry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/provider"
	"goa.design/pulse/rmap"
)

// TestProviderRecoversAfterRedisStateLoss registers a provider through the
// full registry, proves the ping/pong loop is healthy, flushes Redis with
// everything live, and requires the loop to heal itself: the lease scheduler
// re-acquires ping leases, the provider's ensure loop recreates the consumer
// group, registration supervision restores the catalog entry with the same
// token, and fresh pongs make the toolset healthy again.
func TestProviderRecoversAfterRedisStateLoss(t *testing.T) {
	rdb := getRedis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	registryName := fmt.Sprintf("recovery-e2e-%d", time.Now().UnixNano())
	// The minimum provider lease makes registration renewal — the mechanism
	// that restores the catalog record after state loss — fire every
	// leaseDuration/3, bounding recovery time for this test.
	reg, err := New(ctx, Config{
		Redis:                 rdb,
		Name:                  registryName,
		PingInterval:          50 * time.Millisecond,
		MissedPingThreshold:   2,
		ProviderLeaseDuration: toolregistry.MinProviderLeaseDuration,
	})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
	svc := reg.Service()

	toolset := "recovery-toolset"
	payload := validRegisterPayloadForSchemaAdmission(toolset)
	first, err := svc.Register(ctx, payload)
	require.NoError(t, err)

	pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
	require.NoError(t, err)

	var pongs atomic.Int64
	serveErr := make(chan error, 1)
	go func() {
		registration := provider.Registration{
			AdmissionRevision: payload.AdmissionRevision,
			Register: func(ctx context.Context, _, _, incarnationID, _ string) (provider.RegistrationLease, error) {
				registerPayload := *payload
				registerPayload.ProviderIncarnationID = incarnationID
				res, err := svc.Register(ctx, &registerPayload)
				if err != nil {
					return provider.RegistrationLease{}, err
				}
				return provider.RegistrationLease{
					RegistrationToken: res.RegistrationToken,
					Duration:          time.Duration(res.LeaseDurationMs) * time.Millisecond,
				}, nil
			},
			Drain: func(ctx context.Context, _, providerID, incarnationID, token string, settlementDuration time.Duration) error {
				return svc.DrainProvider(ctx, &genregistry.DrainProviderPayload{
					Name:                      toolset,
					ProviderID:                providerID,
					ExpectedRegistrationToken: token,
					ProviderIncarnationID:     incarnationID,
					SettlementDurationMs:      settlementDuration.Milliseconds(),
				})
			},
			Release: func(ctx context.Context, _, providerID, incarnationID, token string) error {
				return svc.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
					Name:                      toolset,
					ProviderID:                providerID,
					ExpectedRegistrationToken: token,
					ProviderIncarnationID:     incarnationID,
				})
			},
			Complete: func(
				ctx context.Context,
				_, providerID, incarnationID, providerToken, requestEventID string,
				result toolregistry.ToolResultMessage,
			) error {
				body, err := json.Marshal(result)
				if err != nil {
					return err
				}
				return svc.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
					Toolset:                   toolset,
					ProviderID:                providerID,
					ProviderIncarnationID:     incarnationID,
					RegistrationToken:         result.RegistrationToken,
					ToolUseID:                 result.ToolUseID,
					ResultJSON:                body,
					RequestEventID:            requestEventID,
					ProviderRegistrationToken: providerToken,
				})
			},
			PublishOutputDelta: func(
				ctx context.Context,
				_, providerID, incarnationID, providerToken, callToken,
				toolUseID, requestEventID, stream, delta string,
			) error {
				return svc.PublishToolOutputDelta(ctx, &genregistry.PublishToolOutputDeltaPayload{
					Toolset:                   toolset,
					ProviderID:                providerID,
					ProviderIncarnationID:     incarnationID,
					ProviderRegistrationToken: providerToken,
					CallRegistrationToken:     callToken,
					ToolUseID:                 toolUseID,
					RequestEventID:            requestEventID,
					Stream:                    stream,
					Delta:                     delta,
				})
			},
			ReportOverload: func(
				ctx context.Context,
				_, providerID, incarnationID, providerToken, callToken,
				toolUseID, requestEventID string,
			) error {
				return svc.ReportToolCallOverload(ctx, &genregistry.ProviderToolCallClaimPayload{
					Toolset:                   toolset,
					ProviderID:                providerID,
					ProviderIncarnationID:     incarnationID,
					ProviderRegistrationToken: providerToken,
					CallRegistrationToken:     callToken,
					ToolUseID:                 toolUseID,
					RequestEventID:            requestEventID,
				})
			},
			Claim: func(
				ctx context.Context,
				claim provider.ClaimRequest,
			) (provider.ClaimDisposition, error) {
				result, err := svc.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
					Toolset:                   claim.Toolset,
					ProviderID:                claim.ProviderID,
					ProviderIncarnationID:     claim.ProviderIncarnationID,
					ProviderRegistrationToken: claim.ProviderRegistrationToken,
					CallRegistrationToken:     claim.CallRegistrationToken,
					ToolUseID:                 claim.ToolUseID,
					RequestEventID:            claim.RequestEventID,
					ClaimOperationID:          claim.OperationID,
				})
				if err != nil {
					return "", err
				}
				return provider.ClaimDisposition(result.Disposition), nil
			},
		}
		serveErr <- provider.Serve(ctx, pulseClient, toolset, noopHandler{}, registration, provider.Options{
			ProviderID: payload.ProviderID,
			Pong: func(ctx context.Context, providerID, incarnationID, pingID string) error {
				if err := svc.Pong(ctx, &genregistry.PongPayload{
					PingID:                pingID,
					Toolset:               toolset,
					ProviderID:            providerID,
					ProviderIncarnationID: incarnationID,
				}); err != nil {
					return err
				}
				pongs.Add(1)
				return nil
			},
			EnsureInterval: 100 * time.Millisecond,
		})
	}()

	healthy := func() bool {
		health, err := reg.healthTracker.Health(ctx, toolset, first.RegistrationToken)
		return err == nil && health.Healthy
	}

	// The ping/pong loop must converge to healthy.
	require.Eventually(t, healthy, 10*time.Second, 20*time.Millisecond,
		"toolset should become healthy from live ping/pong")

	// Redis loses everything: catalog hash, ping leases, the toolset stream,
	// and the provider's consumer group.
	require.NoError(t, rdb.FlushDB(ctx).Err())

	// Ping delivery must resume: the scheduler re-acquires its ping lease,
	// registration supervision restores the catalog entry with the same
	// token, and the ensure loop recreates the consumer group. New pongs
	// prove the full loop is live again.
	// Recovery is bounded by one registration renewal period (lease/3 = 15s
	// at the minimum lease): the renewal restores the catalog record, the
	// next scheduler tick re-acquires the ping lease, and the ensure loop has
	// already recreated the consumer group.
	pongsBeforeRecovery := pongs.Load()
	require.Eventually(t, func() bool { return pongs.Load() >= pongsBeforeRecovery+2 }, 30*time.Second, 20*time.Millisecond,
		"provider should pong post-flush pings after consumer group repair")
	require.Eventually(t, healthy, 30*time.Second, 20*time.Millisecond,
		"toolset should be healthy again after recovery")

	// Recovery must repair durable catalog state under the SAME map name: a
	// fresh replica joined from Redis (as a restarted registry node would)
	// sees the toolset. The registry map revision pin is what makes this
	// possible after a flush reset "=rev" to zero.
	require.Eventually(t, func() bool {
		freshMap, err := rmap.Join(ctx, registryName+":toolsets", rdb)
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
	return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "unexpected", "no tool calls expected in this test"), nil
}

// TestConcurrentRevisionPinsConverge proves concurrent revision repairs from
// several registry replicas converge on the highest target instead of summing
// increments, and that a post-loss regression is re-pinned above the
// established floor.
func TestConcurrentRevisionPinsConverge(t *testing.T) {
	rdb := getRedis(t)
	ctx := context.Background()
	hashKey := "map:revision-converge:content"

	trackers := make([]*healthTracker, 8)
	for i := range trackers {
		trackers[i] = &healthTracker{
			redis:     rdb,
			revFloors: map[string]int64{},
			logger:    telemetry.NewNoopLogger(),
		}
	}
	var wg sync.WaitGroup
	for _, tracker := range trackers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			require.NoError(t, tracker.ensureMapRevision(ctx, hashKey))
		}()
	}
	wg.Wait()

	pinned, err := rdb.HGet(ctx, hashKey, "=rev").Int64()
	require.NoError(t, err)
	require.GreaterOrEqual(t, pinned, time.Now().Add(-time.Minute).UnixMilli(),
		"pin must dominate the wall clock")
	require.LessOrEqual(t, pinned, time.Now().UnixMilli()+8*revFloorSlack,
		"concurrent pins must converge, not sum")

	observed := pinned + 123
	require.NoError(t, rdb.HSet(ctx, hashKey, "=rev", observed).Err())
	require.NoError(t, trackers[0].ensureMapRevision(ctx, hashKey))
	assert.Equal(t, observed, trackers[0].revFloors[hashKey],
		"every valid observation must advance the local loss-detection floor")

	// Redis loses the map: the counter restarts near zero. Any tracker that
	// established a floor must detect the regression and re-pin above it.
	require.NoError(t, rdb.HSet(ctx, hashKey, "=rev", 5).Err())
	require.NoError(t, trackers[0].ensureMapRevision(ctx, hashKey))
	repinned, err := rdb.HGet(ctx, hashKey, "=rev").Int64()
	require.NoError(t, err)
	require.GreaterOrEqual(t, repinned, observed, "repair must restore the highest observed floor after loss")
}
