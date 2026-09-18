//go:build integration

package registry

// These tests run the provider against real Pulse streams and the registry.
// Stream loss is repairable while durable lease authority remains. Catalog
// loss stops the provider without uploading definitions to recreate its lease.

import (
	"context"
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/provider"
	goa "goa.design/goa/v3/pkg"
)

func TestProviderRecoveryPreservesLeaseAuthority(t *testing.T) {
	for _, tc := range []struct {
		name        string
		catalogLost bool
	}{
		{name: "stream loss repairs consumer group"},
		{name: "catalog loss stops provider", catalogLost: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rdb := getRedis(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			registryName := fmt.Sprintf("recovery-e2e-%d", time.Now().UnixNano())
			// The minimum lease schedules renewal soon enough to observe both recovery
			// and lease loss without changing the production scheduling rules.
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
			expectedToken, err := toolregistry.RegistrationToken(payload.SchemaFingerprint, payload.AdmissionRevision)
			require.NoError(t, err)

			pulseClient, err := clientspulse.New(clientspulse.Options{Redis: rdb})
			require.NoError(t, err)

			var pongs, registrations, renewals atomic.Int64
			var serveErr error
			serveDone := make(chan struct{})
			go func() {
				registration := provider.Registration{
					AdmissionRevision: payload.AdmissionRevision,
					Register: func(ctx context.Context, toolset, providerID, incarnationID, revision string) (provider.RegistrationLease, error) {
						registrations.Add(1)
						registerPayload := *payload
						registerPayload.Name = toolset
						registerPayload.ProviderID = providerID
						registerPayload.ProviderIncarnationID = incarnationID
						registerPayload.AdmissionRevision = revision
						res, err := svc.Register(ctx, &registerPayload)
						if err != nil {
							return provider.RegistrationLease{}, err
						}
						return provider.RegistrationLease{
							RegistrationToken: res.RegistrationToken,
							Duration:          time.Duration(res.LeaseDurationMs) * time.Millisecond,
						}, nil
					},
					Renew: func(ctx context.Context, toolset, providerID, incarnationID, token string) (time.Duration, error) {
						assert.Equal(t, expectedToken, token)
						result, err := svc.RenewProvider(ctx, &genregistry.RenewProviderPayload{
							Name:                      toolset,
							ProviderID:                providerID,
							ProviderIncarnationID:     incarnationID,
							ExpectedRegistrationToken: token,
						})
						if err != nil {
							return 0, err
						}
						renewals.Add(1)
						return time.Duration(result.LeaseDurationMs) * time.Millisecond, nil
					},
					ReleaseTimeout: time.Second,
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
				serveErr = provider.Serve(ctx, pulseClient, toolset, noopHandler{}, registration, provider.Options{
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
					EnsureInterval:  100 * time.Millisecond,
					ShutdownTimeout: time.Second,
				})
				close(serveDone)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-serveDone:
				case <-time.After(5 * time.Second):
					t.Error("provider did not finish shutdown")
				}
			})

			healthy := func() bool {
				health, err := reg.healthTracker.Health(ctx, toolset, expectedToken)
				return err == nil && health.Healthy
			}

			// The ping/pong loop must converge to healthy.
			require.Eventually(t, healthy, 10*time.Second, 20*time.Millisecond,
				"toolset should become healthy from live ping/pong")

			if tc.catalogLost {
				// Delete authoritative catalog data as well as stream state. An active
				// provider must not recreate permission whose draining history was lost.
				require.NoError(t, rdb.FlushDB(ctx).Err())
				select {
				case <-serveDone:
				case <-time.After(30 * time.Second):
					t.Fatal("provider did not stop after losing catalog authority")
				}
				var serviceErr *goa.ServiceError
				require.ErrorAs(t, serveErr, &serviceErr)
				assert.Equal(t, "provider_lease_lost", serviceErr.Name)
				assert.Equal(t, int64(1), registrations.Load(), "lease loss must not invoke Register again")
				_, err := svc.GetToolset(ctx, &genregistry.GetToolsetPayload{Name: toolset})
				require.ErrorAs(t, err, &serviceErr)
				assert.Equal(t, "not_found", serviceErr.Name)
				return
			}

			// Lose stream data, groups, lifecycle, and recovery cursor while
			// retaining catalog authority. Pulse Destroy intentionally retires
			// the generation and is not a simulation of Redis state loss.
			streamKey := "pulse:stream:" + toolregistry.ToolsetStreamID(toolset)
			lifecycleKey := streamKey + ":lifecycle"
			lifecycle, err := rdb.HGetAll(ctx, lifecycleKey).Result()
			require.NoError(t, err)
			require.Equal(t, "1", lifecycle["generation"])
			require.Equal(t, "active", lifecycle["state"])
			deleted, err := rdb.Del(ctx, streamKey, lifecycleKey, streamKey+":sink-recovery:1").Result()
			require.NoError(t, err)
			require.GreaterOrEqual(t, deleted, int64(2), "the stream and its active lifecycle must have existed")
			pongsBeforeRepair := pongs.Load()
			renewalsBeforeRepair := renewals.Load()
			require.Eventually(t, func() bool {
				lifecycle, err := rdb.HGetAll(ctx, lifecycleKey).Result()
				if err != nil || lifecycle["generation"] != "1" || lifecycle["state"] != "active" {
					return false
				}
				groups, err := rdb.XInfoGroups(ctx, streamKey).Result()
				if err != nil {
					return false
				}
				for _, group := range groups {
					if group.Name == toolregistry.ProviderConsumerGroup {
						return true
					}
				}
				return false
			}, 10*time.Second, 20*time.Millisecond,
				"the existing sink should recover its original generation and consumer group")
			require.Eventually(t, func() bool { return pongs.Load() >= pongsBeforeRepair+2 }, 10*time.Second, 20*time.Millisecond,
				"provider should receive fresh pings after consumer group repair")
			require.Eventually(t, func() bool { return renewals.Load() > renewalsBeforeRepair }, 30*time.Second, 20*time.Millisecond,
				"the same provider should renew its retained lease without Register")
			require.Eventually(t, healthy, 10*time.Second, 20*time.Millisecond,
				"the retained admission should remain healthy after stream repair and renewal")
			assert.Equal(t, int64(1), registrations.Load(), "stream repair must not upload definitions again")

			cancel()
			select {
			case <-serveDone:
				require.ErrorIs(t, serveErr, context.Canceled)
			case <-time.After(5 * time.Second):
				t.Fatal("provider did not finish shutdown after stream repair")
			}
		})
	}
}

// noopHandler satisfies provider.Handler for tests that never dispatch tool calls.
type noopHandler struct{}

func (noopHandler) HandleToolCall(_ context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	return toolregistry.NewToolResultErrorMessage(msg.RegistrationToken, msg.ToolUseID, "unexpected", "no tool calls expected in this test"), nil
}
