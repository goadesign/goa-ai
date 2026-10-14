//go:build integration

// Registered service executions cross the generated gRPC contract and real
// provider stream. Standalone calls must complete without creating a session;
// sessionful calls must retain their existing session and execution identity.
package registry

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	clientspulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/agent/engine"
	engineinmem "goa.design/goa-ai/runtime/agent/engine/inmem"
	frameworkruntime "goa.design/goa-ai/runtime/agent/runtime"
	"goa.design/goa-ai/runtime/agent/session"
	storageinmem "goa.design/goa-ai/runtime/agent/storage/inmem"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/goa-ai/runtime/toolregistry/provider"
)

type registeredServiceHandler struct {
	calls    atomic.Int64
	received chan toolregistry.ToolCallMessage
}

func (h *registeredServiceHandler) HandleToolCall(_ context.Context, message toolregistry.ToolCallMessage) (toolregistry.ToolResultMessage, error) {
	h.calls.Add(1)
	h.received <- message
	return toolregistry.NewToolResultMessage(message.RegistrationToken, message.ToolUseID, json.RawMessage(`{"value":7}`)), nil
}

func TestRegisteredServiceRootCompletesAcrossProviderTransport(t *testing.T) {
	for _, sessionID := range []string{"", "existing-session"} {
		t.Run("session="+sessionID, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			rdb := getRedis(t)
			reg, err := New(ctx, Config{Redis: rdb, Name: t.Name(), PingInterval: 50 * time.Millisecond, MissedPingThreshold: 2})
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, reg.Close(context.Background())) })
			client, _ := startServiceAndClients(t, reg.Service())
			declared, err := client.DeclareServiceToolset(ctx, testServiceDeclaration())
			require.NoError(t, err)
			pulse, err := clientspulse.New(clientspulse.Options{Redis: rdb})
			require.NoError(t, err)
			handler := &registeredServiceHandler{received: make(chan toolregistry.ToolCallMessage, 2)}
			serveCtx, stop := context.WithCancel(ctx)
			done := make(chan error, 1)
			go func() {
				done <- provider.Serve(serveCtx, pulse, declared.Toolset.Name, handler, attachedTestRegistration(client, declared.RegistrationToken), provider.Options{
					ProviderID: "inventory-provider",
					Pong: func(ctx context.Context, providerID, incarnationID, pingID string) error {
						return client.Pong(ctx, &genregistry.PongPayload{Toolset: declared.Toolset.Name, ProviderID: providerID, ProviderIncarnationID: incarnationID, PingID: pingID})
					},
				})
			}()
			t.Cleanup(func() {
				stop()
				select {
				case err := <-done:
					assert.ErrorIs(t, err, context.Canceled)
				case <-time.After(provider.DefaultShutdownTimeout + provider.DefaultRegistrationReleaseTimeout + time.Second):
					t.Error("provider did not stop")
				}
			})
			require.Eventually(t, func() bool {
				ready, err := client.CheckAdmission(ctx, &genregistry.CheckAdmissionPayload{Name: declared.Toolset.Name, ExpectedRegistrationToken: declared.RegistrationToken})
				return err == nil && ready.Ready
			}, 10*time.Second, 20*time.Millisecond)
			store := storageinmem.New()
			if sessionID != "" {
				_, err = store.CreateSession(ctx, sessionID, time.Now().UTC())
				require.NoError(t, err)
			}
			runtime := frameworkruntime.New(store, frameworkruntime.WithEngine(engineinmem.New()), frameworkruntime.WithToolWorkflowQueue("service-worker"))
			require.NoError(t, runtime.RegisterRegistry("registry", client, pulse))
			require.NoError(t, runtime.RegisterToolWorkflow(ctx))
			invocation := &genregistry.RegisteredInvocation{
				Invocation: &genregistry.ExecutionInvocation{Invocation: genregistry.NewExecutionInvocationKindRoot(&genregistry.RootInvocation{RunID: "service-run"})},
				Target:     &genregistry.CatalogOperation{Scope: "inventory-provider", Name: "inventory", Target: genregistry.NewCatalogTargetService(&genregistry.ServiceCatalogTarget{Toolset: declared.Toolset.Name, Tool: "inventory.lookup", RegistrationToken: declared.RegistrationToken})},
				Arguments:  []byte(`{"value":7}`),
			}
			labels := map[string]string{"actor": "reader", "application": "inventory"}
			for range 2 {
				var handle engine.WorkflowHandle
				if sessionID == "" {
					handle, err = runtime.StartOneShotToolOperation(ctx, "registry", declared, invocation, labels, nil)
				} else {
					handle, err = runtime.StartToolOperation(ctx, sessionID, "registry", declared, invocation, labels, nil)
				}
				require.NoError(t, err)
				result, err := handle.Wait(ctx)
				require.NoError(t, err)
				require.NotNil(t, result.FinalToolResult)
				assert.JSONEq(t, `{"value":7}`, string(result.FinalToolResult.Result))
				assert.Nil(t, result.FinalToolResult.Failure)
				saved, err := store.LoadRun(ctx, "service-run")
				require.NoError(t, err)
				assert.Equal(t, session.RunStatusCompleted, saved.Status)
				assert.Equal(t, sessionID, saved.SessionID)
			}

			assert.EqualValues(t, 1, handler.calls.Load())
			message := <-handler.received
			assert.Equal(t, sessionID, message.Meta.SessionID)
			assert.Equal(t, "service-run", message.Meta.RunID)
			assert.NotEmpty(t, message.Meta.ToolCallID)
			assert.Equal(t, labels, message.Meta.Labels)
			assert.Zero(t, message.Meta.ExecutionSequence)
			assert.Nil(t, message.Meta.ExecutionContinuation)
			_, err = runtime.StartToolOperation(ctx, "", "registry", declared, invocation, labels, nil)
			assert.ErrorIs(t, err, frameworkruntime.ErrMissingSessionID)
		})
	}
}
