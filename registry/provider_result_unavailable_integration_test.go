//go:build integration

package registry

// These tests acknowledge real Pulse deliveries without provider terminals.
// The existing Redis claim and generated registry callbacks must preserve
// execution ownership and settle unknown outcomes on deadline or clean release.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/provider"
)

type (
	registryUnavailableHandler func(context.Context, toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error)

	unavailableCompletion struct {
		callID   string
		accepted bool
	}
)

func TestProviderResultUnavailableRetainsClaimAndRecovers(t *testing.T) {
	for _, test := range []struct {
		name      string
		deadline  bool
		lostReply bool
	}{
		{name: "deadline while another call continues", deadline: true},
		{name: "clean release before deadline"},
		{name: "lost clean release reply", lostReply: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := t.Context()
			rdb := getRedis(t)
			executionTimeout := time.Minute
			if test.deadline {
				executionTimeout = 5 * time.Second
			}
			reg, err := New(ctx, Config{
				Redis: rdb, Name: t.Name(), ExecutionTimeout: executionTimeout,
				PingInterval: 50 * time.Millisecond, MissedPingThreshold: 2,
				ProviderLeaseDuration: toolregistry.MinProviderLeaseDuration,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			svc := reg.Service()
			client, _ := startServiceAndClients(t, svc)
			declaration, err := client.DeclareServiceToolset(ctx, testServiceDeclaration())
			require.NoError(t, err)
			name, token := declaration.Toolset.Name, declaration.RegistrationToken
			pulse, err := clientspulse.New(clientspulse.Options{Redis: rdb})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, pulse.Close(context.Background())) })
			registration := attachedTestRegistration(client, token)
			originalDrain, originalRelease, originalComplete := registration.Drain, registration.Release, registration.Complete
			drained := make(chan struct{})
			var drainOnce sync.Once
			var releases, handled atomic.Int64
			completions := make(chan unavailableCompletion, 4)
			registration.Drain = func(ctx context.Context, name, providerID, incarnation, token string, duration time.Duration) error {
				if err := originalDrain(ctx, name, providerID, incarnation, token, duration); err != nil {
					return err
				}
				drainOnce.Do(func() { close(drained) })
				return nil
			}
			registration.Release = func(ctx context.Context, name, providerID, incarnation, token string) error {
				if err := originalRelease(ctx, name, providerID, incarnation, token); err != nil {
					return err
				}
				if releases.Add(1) == 1 && test.lostReply {
					return errors.New("release response lost")
				}
				return nil
			}
			registration.Complete = func(
				ctx context.Context,
				name, providerID, incarnation, token, eventID string,
				result toolregistry.ToolResultMessage,
			) (bool, error) {
				accepted, err := originalComplete(ctx, name, providerID, incarnation, token, eventID, result)
				if err == nil {
					completions <- unavailableCompletion{callID: result.ToolUseID, accepted: accepted}
				}
				return accepted, err
			}
			unavailable := make(chan toolregistry.ToolCallMessage, 3)
			active := make(chan context.Context, 1)
			finishActive := make(chan struct{})
			var finishOnce sync.Once
			handler := registryUnavailableHandler(func(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
				handled.Add(1)
				if msg.Meta.ToolCallID != "active" {
					// The counter represents the effect already performed; only
					// its result has been lost. Never execute this call again.
					unavailable <- msg
					return toolregistry.ToolResultMessage{}, provider.ErrResultUnavailable
				}
				active <- ctx
				select {
				case <-finishActive:
				case <-ctx.Done():
					return toolregistry.ToolResultMessage{}, ctx.Err()
				}
				return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"value":7}`)), nil
			})
			serveCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			var serveErr error
			// The real Pulse sink may wait for its blocking Redis read to finish.
			// Keep the provider's default settlement budget and allow release afterward.
			shutdownWait := provider.DefaultShutdownTimeout + provider.DefaultRegistrationReleaseTimeout + time.Second
			go func() {
				serveErr = provider.Serve(serveCtx, pulse, name, handler, registration, provider.Options{
					ProviderID:             "result-unavailable-provider",
					MaxConcurrentToolCalls: 2,
					MaxQueuedToolCalls:     2,
					Pong: func(ctx context.Context, providerID, incarnation, pingID string) error {
						return client.Pong(ctx, &genregistry.PongPayload{
							Toolset: name, ProviderID: providerID, ProviderIncarnationID: incarnation, PingID: pingID,
						})
					},
				})
				close(done)
			}()
			t.Cleanup(func() {
				finishOnce.Do(func() { close(finishActive) })
				cancel()
				select {
				case <-done:
				case <-time.After(shutdownWait):
					t.Error("provider did not finish during cleanup")
				}
			})
			require.Eventually(t, func() bool {
				result, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{Name: name, ExpectedRegistrationToken: token})
				return err == nil && result.Ready
			}, 10*time.Second, 20*time.Millisecond)

			store := svc.callAdmissions.(*callAdmissionStore)
			var calls []*genregistry.CallToolResult
			var firstClaim map[string]string
			for i := range 3 {
				call, err := client.CallTool(ctx, unavailableCallPayload(name, fmt.Sprintf("lost-%d", i)))
				require.NoError(t, err)
				select {
				case message := <-unavailable:
					assert.Equal(t, call.ToolUseID, message.ToolUseID)
				case <-time.After(3 * time.Second):
					t.Fatal("provider did not execute the call")
				}
				key := store.callKey(call.ToolUseID)
				eventID := retainedPublicationEventID(t, ctx, rdb, store, call.ToolUseID)
				requireUnavailableDeliveryAcknowledged(t, ctx, rdb, name, eventID)
				fields, err := rdb.HGetAll(ctx, key).Result()
				require.NoError(t, err)
				assert.NotEqual(t, "1", fields["terminal"])
				assert.Empty(t, fields["terminal_payload"])
				assert.Equal(t, token, fields["dispatch_provider_token"])
				assert.Equal(t, eventID, fields["dispatch_request_event_id"])
				require.NotEmpty(t, fields["dispatch_claim_operation_id"])
				_, err = rdb.ZScore(ctx, store.settlementKey, key).Result()
				require.NoError(t, err)
				leaseIndex := store.leaseSettlementKey(token, fields["dispatch_provider_lease"])
				_, err = rdb.ZScore(ctx, leaseIndex, key).Result()
				require.NoError(t, err)
				member, err := rdb.HGet(ctx, store.membershipKey, key).Result()
				require.NoError(t, err)
				assert.Equal(t, leaseIndex, member)
				if i == 0 {
					firstClaim = fields
				}
				calls = append(calls, call)
			}
			assert.Empty(t, completions)
			assert.Zero(t, releases.Load())
			leaseIndex := store.leaseSettlementKey(token, firstClaim["dispatch_provider_lease"])
			pending, err := rdb.ZCard(ctx, leaseIndex).Result()
			require.NoError(t, err)
			assert.Equal(t, int64(3), pending, "durable claims can outnumber running workers")

			// A new incarnation can attach but cannot take over this call, even
			// though its original Pulse delivery has already been acknowledged.
			replacement := uuid.NewString()
			_, err = client.AttachProvider(ctx, &genregistry.AttachProviderPayload{
				Name: name, ProviderID: "other-provider", ProviderIncarnationID: replacement,
				ExpectedRegistrationToken: token, WireProtocolVersion: toolregistry.WireProtocolVersion,
			})
			require.NoError(t, err)
			owned, err := client.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
				Toolset: name, ProviderID: "other-provider", ProviderIncarnationID: replacement,
				ProviderRegistrationToken: token, CallRegistrationToken: token,
				ToolUseID: calls[0].ToolUseID, RequestEventID: firstClaim["dispatch_request_event_id"],
				ClaimOperationID: uuid.NewString(),
			})
			require.NoError(t, err)
			assert.Equal(t, "claimed", owned.Disposition)
			require.NoError(t, client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
				Name: name, ProviderID: "other-provider", ProviderIncarnationID: replacement,
				ExpectedRegistrationToken: token,
			}))
			assert.Equal(t, int64(3), handled.Load())

			if test.deadline {
				deadline, err := time.Parse(time.RFC3339Nano, calls[0].ExecutionDeadline)
				require.NoError(t, err)
				// Admit the next call later, giving it its own later deadline.
				require.Eventually(t, func() bool {
					now, err := rdb.Time(ctx).Result()
					return err == nil && !now.Before(deadline.Add(-time.Second))
				}, 6*time.Second, 10*time.Millisecond)
			}
			next, err := client.CallTool(ctx, unavailableCallPayload(name, "active"))
			require.NoError(t, err)
			var activeCtx context.Context
			select {
			case activeCtx = <-active:
			case <-time.After(3 * time.Second):
				t.Fatal("provider did not keep serving after unavailable results")
			}
			if test.deadline {
				for _, call := range calls {
					requireUnavailableTerminal(t, ctx, rdb, store, call, "execution_deadline")
				}
				require.NoError(t, activeCtx.Err(), "another call retains its later deadline")
				assert.Zero(t, releases.Load())
			}

			cancel()
			select {
			case <-drained:
			case <-time.After(3 * time.Second):
				t.Fatal("provider did not drain")
			}
			require.NoError(t, activeCtx.Err(), "draining must preserve the active handler")
			assert.Zero(t, releases.Load(), "no release while the other handler is active")
			finishOnce.Do(func() { close(finishActive) })
			select {
			case completion := <-completions:
				assert.Equal(t, next.ToolUseID, completion.callID)
				assert.True(t, completion.accepted)
			case <-time.After(3 * time.Second):
				t.Fatal("active call did not complete during drain")
			}
			select {
			case <-done:
			case <-time.After(shutdownWait):
				t.Fatal("provider did not release")
			}
			require.EqualError(t, serveErr, context.Canceled.Error())
			wantReleases := int64(1)
			if test.lostReply {
				wantReleases = 2
			}
			assert.Equal(t, wantReleases, releases.Load())
			for _, call := range calls {
				cause := "provider_lease_lost"
				if test.deadline {
					cause = "execution_deadline"
				}
				requireUnavailableTerminal(t, ctx, rdb, store, call, cause)
			}
			state, err := svc.catalog.activeState(ctx, name)
			require.NoError(t, err)
			assert.Empty(t, state.ProviderLeases)
			assert.Equal(t, int64(4), handled.Load())
			assert.Empty(t, completions, "no terminal submission for the three unavailable results")
		})
	}
}

func (h registryUnavailableHandler) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	return h(ctx, msg)
}

// unavailableCallPayload sends one valid call through the declared test tool;
// only the existing run-scoped call ID changes between effects.
func unavailableCallPayload(name, callID string) *genregistry.CallToolPayload {
	return &genregistry.CallToolPayload{
		Toolset: name, Tool: "inventory.lookup", PayloadJSON: []byte(`{"value":7}`),
		Meta: &genregistry.ToolCallMeta{
			RunID: "result-unavailable-run", SessionID: "result-unavailable-session", ToolCallID: callID,
		},
		WireProtocolVersion: toolregistry.WireProtocolVersion,
	}
}

// requireUnavailableDeliveryAcknowledged observes actual Pulse pending state
// after the handler ran, without replacing or directly invoking Sink.Ack.
func requireUnavailableDeliveryAcknowledged(t *testing.T, ctx context.Context, rdb *redis.Client, toolset, eventID string) {
	t.Helper()
	key := pulseStreamKeyPrefix + toolregistry.ToolsetStreamID(toolset)
	require.Eventually(t, func() bool {
		pending, err := rdb.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: key, Group: toolregistry.ProviderConsumerGroup, Start: eventID, End: eventID, Count: 1,
		}).Result()
		return err == nil && len(pending) == 0
	}, 3*time.Second, 10*time.Millisecond)
}

// requireUnavailableTerminal waits for the existing owner recovery and checks
// the retained identity, unknown-outcome result, and removed settlement indexes.
func requireUnavailableTerminal(
	t *testing.T,
	ctx context.Context,
	rdb *redis.Client,
	store *callAdmissionStore,
	call *genregistry.CallToolResult,
	cause string,
) {
	t.Helper()
	key := store.callKey(call.ToolUseID)
	require.Eventually(t, func() bool {
		fields, err := rdb.HGetAll(ctx, key).Result()
		return err == nil && fields["terminal"] == "1"
	}, 3*time.Second, 10*time.Millisecond)
	fields, err := rdb.HGetAll(ctx, key).Result()
	require.NoError(t, err)
	assert.Equal(t, cause, fields["terminal_cause"])
	var result toolregistry.ToolResultMessage
	require.NoError(t, json.Unmarshal([]byte(fields["terminal_payload"]), &result))
	assert.Equal(t, call.ToolUseID, result.ToolUseID)
	assert.Equal(t, call.RegistrationToken, result.RegistrationToken)
	require.NotNil(t, result.Error)
	assert.Equal(t, toolregistry.ToolErrorCodeOutcomeUnknown, result.Error.Code)
	assert.Empty(t, result.Result)
	_, err = rdb.ZScore(ctx, store.settlementKey, key).Result()
	require.ErrorIs(t, err, redis.Nil)
	_, err = rdb.ZScore(ctx, store.leaseSettlementKey(call.RegistrationToken, fields["dispatch_provider_lease"]), key).Result()
	require.ErrorIs(t, err, redis.Nil)
	member, err := rdb.HExists(ctx, store.membershipKey, key).Result()
	require.NoError(t, err)
	assert.False(t, member)
}
