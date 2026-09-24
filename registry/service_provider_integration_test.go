//go:build integration

package registry

// These tests run the real provider lifecycle through generated registry gRPC
// callbacks and real Pulse streams, including uncertain startup and shutdown.

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/provider"
)

type (
	// lostAttachmentReply commits the first attachment but returns a transport
	// failure. The retry must reuse the same provider incarnation and token.
	lostAttachmentReply struct {
		genregistry.Service
		attempts atomic.Int64
	}

	// settlingServiceHandler holds an accepted call until the test observes
	// drain. It then completes using the still-valid settlement lease.
	settlingServiceHandler struct {
		entered chan struct{}
		finish  chan struct{}
	}
)

func (s *lostAttachmentReply) AttachProvider(ctx context.Context, p *genregistry.AttachProviderPayload) (*genregistry.RegisterResult, error) {
	result, err := s.Service.AttachProvider(ctx, p)
	if err != nil {
		return nil, err
	}
	if s.attempts.Add(1) == 1 {
		return nil, genregistry.MakeServiceUnavailable(errors.New("attachment reply lost"))
	}
	return result, nil
}

func (h *settlingServiceHandler) HandleToolCall(ctx context.Context, msg toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	close(h.entered)
	select {
	case <-h.finish:
		return toolregistry.NewToolResultMessage(msg.RegistrationToken, msg.ToolUseID, json.RawMessage(`{"value":7}`)), nil
	case <-ctx.Done():
		return toolregistry.ToolResultMessage{}, ctx.Err()
	}
}

func TestDeclaredProviderServeLostReplyDrainAndAuthorityLoss(t *testing.T) {
	for _, loseAuthority := range []bool{false, true} {
		t.Run(map[bool]string{false: "drain accepted call", true: "stop after lease loss"}[loseAuthority], func(t *testing.T) {
			ctx := t.Context()
			rdb := getRedis(t)
			reg, err := New(ctx, Config{
				Redis: rdb, Name: t.Name(), PingInterval: 50 * time.Millisecond,
				MissedPingThreshold: 2, ProviderLeaseDuration: toolregistry.MinProviderLeaseDuration,
			})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			svc := reg.Service()
			wrapped := &lostAttachmentReply{Service: svc}
			client, _ := startServiceAndClients(t, wrapped)
			declared, err := client.DeclareServiceToolset(ctx, testServiceDeclaration())
			require.NoError(t, err)
			name, token := declared.Toolset.Name, declared.RegistrationToken
			offline, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{Name: name, ExpectedRegistrationToken: token})
			require.NoError(t, err)
			assert.False(t, offline.Ready)

			pulse, err := clientspulse.New(clientspulse.Options{Redis: rdb})
			require.NoError(t, err)
			registration := attachedTestRegistration(client, token)
			handler := &settlingServiceHandler{entered: make(chan struct{}), finish: make(chan struct{})}
			var finish sync.Once
			serveCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			shutdownWait := provider.DefaultShutdownTimeout + provider.DefaultRegistrationReleaseTimeout + time.Second
			var serveErr error
			go func() {
				serveErr = provider.Serve(serveCtx, pulse, name, handler, registration, provider.Options{
					ProviderID: "provider",
					Pong: func(ctx context.Context, providerID, incarnationID, pingID string) error {
						return client.Pong(ctx, &genregistry.PongPayload{
							Toolset: name, ProviderID: providerID, ProviderIncarnationID: incarnationID, PingID: pingID,
						})
					},
				})
				close(done)
			}()
			t.Cleanup(func() {
				finish.Do(func() { close(handler.finish) })
				cancel()
				select {
				case <-done:
				case <-time.After(shutdownWait):
					t.Error("provider shutdown did not finish")
				}
			})
			require.Eventually(t, func() bool {
				ready, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{Name: name, ExpectedRegistrationToken: token})
				return err == nil && ready.Ready
			}, 10*time.Second, 20*time.Millisecond)
			assert.Equal(t, int64(2), wrapped.attempts.Load())
			state, err := svc.catalog.activeState(ctx, name)
			require.NoError(t, err)
			require.Len(t, state.ProviderLeases, 1, "lost response retry must not create another incarnation")
			assert.Equal(t, declared.Toolset.RegisteredAt, state.RegisteredAt)
			var incarnation string
			for key := range state.ProviderLeases {
				_, incarnation, err = parseProviderLeaseKey(key)
				require.NoError(t, err)
			}
			if loseAuthority {
				require.NoError(t, client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
					Name: name, ProviderID: "provider", ProviderIncarnationID: incarnation, ExpectedRegistrationToken: token,
				}))
				select {
				case <-done:
				case <-time.After(30 * time.Second):
					t.Fatal("provider did not stop after renewal lost its lease")
				}
				requireServiceErrorName(t, serveErr, "provider_lease_lost")
				assert.Equal(t, int64(2), wrapped.attempts.Load(), "active Serve must never attach again")
			} else {
				call, err := client.CallResolvedTool(ctx, &genregistry.CallResolvedToolPayload{
					Toolset: name, Tool: "inventory.lookup", ExpectedRegistrationToken: token,
					PayloadJSON: []byte(`{"value":7}`), WireProtocolVersion: toolregistry.WireProtocolVersion,
					Meta: &genregistry.ToolCallMeta{RunID: "service-run", SessionID: "service-session", ToolCallID: "read-inventory"},
				})
				require.NoError(t, err)
				select {
				case <-handler.entered:
				case <-time.After(10 * time.Second):
					t.Fatal("provider did not execute the declared tool")
				}
				// A second exact attachment gains its own lease, not ownership
				// of another incarnation's already-claimed call.
				_, err = client.AttachProvider(ctx, &genregistry.AttachProviderPayload{
					Name: name, ExpectedRegistrationToken: token, ProviderID: "provider",
					ProviderIncarnationID: testIncarnationB, WireProtocolVersion: toolregistry.WireProtocolVersion,
				})
				require.NoError(t, err)
				admissions := svc.callAdmissions.(*callAdmissionStore)
				event := retainedPublicationEventID(t, ctx, rdb, admissions, call.ToolUseID)
				claim, err := client.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
					Toolset: name, ProviderID: "provider", ProviderIncarnationID: testIncarnationB,
					ProviderRegistrationToken: token, CallRegistrationToken: token,
					ToolUseID: call.ToolUseID, RequestEventID: event, ClaimOperationID: uuid.NewString(),
				})
				require.NoError(t, err)
				assert.Equal(t, "claimed", claim.Disposition)
				require.NoError(t, client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
					Name: name, ProviderID: "provider", ProviderIncarnationID: testIncarnationB, ExpectedRegistrationToken: token,
				}))
				cancel()
				require.Eventually(t, func() bool {
					state, err := svc.catalog.activeState(ctx, name)
					return err == nil && state.ProviderLeases[providerLeaseKey("provider", incarnation)].Draining
				}, 3*time.Second, 10*time.Millisecond)
				_, err = client.AttachProvider(ctx, &genregistry.AttachProviderPayload{
					Name: name, ExpectedRegistrationToken: token, ProviderID: "provider",
					ProviderIncarnationID: incarnation, WireProtocolVersion: toolregistry.WireProtocolVersion,
				})
				requireServiceErrorName(t, err, "provider_lease_lost")
				finish.Do(func() { close(handler.finish) })
				select {
				case <-done:
				case <-time.After(shutdownWait):
					t.Fatal("provider did not settle and release")
				}
				require.EqualError(t, serveErr, context.Canceled.Error(), "shutdown must not hide a cleanup failure")
				body, err := rdb.HGet(ctx, admissions.callKey(call.ToolUseID), "terminal_payload").Bytes()
				require.NoError(t, err)
				var result toolregistry.ToolResultMessage
				require.NoError(t, json.Unmarshal(body, &result))
				require.Nil(t, result.Error)
				assert.JSONEq(t, `{"value":7}`, string(result.Result))
			}
			saved, err := client.ResolveToolset(ctx, &genregistry.GetToolsetPayload{Name: name})
			require.NoError(t, err)
			assert.Equal(t, declared, saved)
			state, err = svc.catalog.activeState(ctx, name)
			require.NoError(t, err)
			assert.Empty(t, state.ProviderLeases, "Serve returned %v", serveErr)
		})
	}
}

// attachedTestRegistration wires the generated registry client into the real
// Serve lifecycle. Only startup captures a selection token; all later callbacks
// receive the exact lease identity that Serve obtained from that operation.
func attachedTestRegistration(client *genregistry.Client, selectedToken string) provider.Registration {
	return provider.Registration{
		Register: func(ctx context.Context, name, providerID, incarnationID string) (provider.RegistrationLease, error) {
			result, err := client.AttachProvider(ctx, &genregistry.AttachProviderPayload{
				Name: name, ProviderID: providerID, ProviderIncarnationID: incarnationID,
				ExpectedRegistrationToken: selectedToken, WireProtocolVersion: toolregistry.WireProtocolVersion,
			})
			if err != nil {
				return provider.RegistrationLease{}, err
			}
			return provider.RegistrationLease{RegistrationToken: result.RegistrationToken, Duration: time.Duration(result.LeaseDurationMs) * time.Millisecond}, nil
		},
		Renew: func(ctx context.Context, name, providerID, incarnationID, token string) (time.Duration, error) {
			result, err := client.RenewProvider(ctx, &genregistry.RenewProviderPayload{
				Name: name, ProviderID: providerID, ProviderIncarnationID: incarnationID, ExpectedRegistrationToken: token,
			})
			if err != nil {
				return 0, err
			}
			return time.Duration(result.LeaseDurationMs) * time.Millisecond, nil
		},
		Drain: func(ctx context.Context, name, providerID, incarnationID, token string, duration time.Duration) error {
			return client.DrainProvider(ctx, &genregistry.DrainProviderPayload{
				Name: name, ProviderID: providerID, ProviderIncarnationID: incarnationID,
				ExpectedRegistrationToken: token, SettlementDurationMs: duration.Milliseconds(),
			})
		},
		Release: func(ctx context.Context, name, providerID, incarnationID, token string) error {
			return client.ReleaseProvider(ctx, &genregistry.ReleaseProviderPayload{
				Name: name, ProviderID: providerID, ProviderIncarnationID: incarnationID, ExpectedRegistrationToken: token,
			})
		},
		Complete: func(ctx context.Context, name, providerID, incarnationID, token, event string, result toolregistry.ToolResultMessage) error {
			body, err := json.Marshal(result)
			if err != nil {
				return err
			}
			return client.CompleteToolCall(ctx, &genregistry.CompleteToolCallPayload{
				Toolset: name, ProviderID: providerID, ProviderIncarnationID: incarnationID,
				ProviderRegistrationToken: token, RegistrationToken: result.RegistrationToken,
				ToolUseID: result.ToolUseID, RequestEventID: event, ResultJSON: body,
			})
		},
		PublishOutputDelta: func(ctx context.Context, name, providerID, incarnationID, token, callToken, callID, event, stream, delta string) error {
			return client.PublishToolOutputDelta(ctx, &genregistry.PublishToolOutputDeltaPayload{
				Toolset: name, ProviderID: providerID, ProviderIncarnationID: incarnationID,
				ProviderRegistrationToken: token, CallRegistrationToken: callToken,
				ToolUseID: callID, RequestEventID: event, Stream: stream, Delta: delta,
			})
		},
		ReportOverload: func(ctx context.Context, name, providerID, incarnationID, token, callToken, callID, event string) error {
			return client.ReportToolCallOverload(ctx, &genregistry.ProviderToolCallClaimPayload{
				Toolset: name, ProviderID: providerID, ProviderIncarnationID: incarnationID,
				ProviderRegistrationToken: token, CallRegistrationToken: callToken, ToolUseID: callID, RequestEventID: event,
			})
		},
		Claim: func(ctx context.Context, p provider.ClaimRequest) (provider.ClaimDisposition, error) {
			result, err := client.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
				Toolset: p.Toolset, ProviderID: p.ProviderID, ProviderIncarnationID: p.ProviderIncarnationID,
				ProviderRegistrationToken: p.ProviderRegistrationToken, CallRegistrationToken: p.CallRegistrationToken,
				ToolUseID: p.ToolUseID, RequestEventID: p.RequestEventID, ClaimOperationID: p.OperationID,
			})
			if err != nil {
				return "", err
			}
			return provider.ClaimDisposition(result.Disposition), nil
		},
		RetryInitialInterval: 10 * time.Millisecond, RetryMaxInterval: 20 * time.Millisecond,
	}
}
