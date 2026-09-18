package provider

// These tests send queued calls through Serve and the generated registry gRPC
// client and server. The registry decides whether a call may execute even when
// its message deadline has passed; the deadline still bounds handler work.

import (
	"context"
	"encoding/json"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	genregistrygrpc "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistrysrv "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

type (
	// deadlineClaimService returns a configured registry decision for the first
	// delivery and permits the following healthy call to execute.
	deadlineClaimService struct {
		genregistry.Service
		disposition ClaimDisposition
		requests    atomic.Int64
	}

	// blockedDeadlineClaimService withholds its application response until the
	// test observes client cancellation.
	blockedDeadlineClaimService struct {
		genregistry.Service
		started        chan struct{}
		clientReturned chan struct{}
	}

	// deadlineClaimHandler records the execution context and honors cancellation.
	deadlineClaimHandler struct {
		observations chan deadlineClaimObservation
	}

	deadlineClaimObservation struct {
		deadline time.Time
		err      error
	}
)

func TestServeGeneratedClaimUsesRegistryDecisionPastMessageDeadline(t *testing.T) {
	t.Parallel()

	for _, timing := range []struct {
		name   string
		offset time.Duration
	}{
		{name: "future", offset: time.Hour},
		{name: "past", offset: -time.Second},
	} {
		for _, disposition := range []ClaimDisposition{
			ClaimExpired, ClaimTerminal, ClaimOwned, ClaimExecute,
		} {
			t.Run(timing.name+"/"+string(disposition), func(t *testing.T) {
				service := &deadlineClaimService{disposition: disposition}
				claim := newGeneratedClaimCallback(t, service)
				ctx, cancel := context.WithCancel(context.Background())
				events := make(chan *streaming.Event, 1)
				acknowledgements := make(chan string, 2)
				handler := &deadlineClaimHandler{
					observations: make(chan deadlineClaimObservation, 2),
				}
				var completed, released atomic.Int64

				sink := mockpulse.NewSink(t)
				sink.SetSubscribe(func() <-chan *streaming.Event { return events })
				sink.SetClose(func(context.Context) error { return nil })
				sink.SetAck(func(ackCtx context.Context, event *streaming.Event) error {
					if err := ackCtx.Err(); err != nil {
						return err
					}
					acknowledgements <- event.ID
					return nil
				})
				requestStream := mockpulse.NewStream(t)
				requestStream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
					return sink, nil
				})
				client := mockpulse.NewClient(t)
				client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
					return requestStream, nil
				})
				registration := successfulRegistration()
				registration.Claim = claim
				registration.Complete = func(
					completeCtx context.Context,
					_, _, _, _, _ string,
					_ toolregistry.ToolResultMessage,
				) error {
					if err := completeCtx.Err(); err != nil {
						return err
					}
					completed.Add(1)
					return nil
				}
				registration.Release = func(context.Context, string, string, string, string) error {
					released.Add(1)
					return nil
				}

				errc := make(chan error, 1)
				done := make(chan struct{})
				go func() {
					defer close(done)
					errc <- Serve(ctx, client, "test.toolset", handler, registration, Options{
						ProviderID:             testProviderID,
						Pong:                   func(context.Context, string, string, string) error { return nil },
						MaxConcurrentToolCalls: 1,
						MaxQueuedToolCalls:     1,
						ShutdownTimeout:        time.Second,
					})
				}()
				t.Cleanup(func() {
					cancel()
					select {
					case <-done:
					case <-time.After(3 * time.Second):
						t.Error("provider did not stop during test cleanup")
					}
				})

				// The event helper accepts result retention, so add its transport
				// allowance to select the execution deadline exactly.
				executionDeadline := time.Now().Add(timing.offset).Truncate(time.Millisecond)
				event := testToolCallEventAt(
					t, "deadline-call",
					executionDeadline.Add(toolregistry.ResultStreamTransportBudget),
				)
				event.ID = "1-0"
				events <- event
				select {
				case id := <-acknowledgements:
					assert.Equal(t, "1-0", id)
				case err := <-errc:
					t.Fatalf("provider stopped before receiving the registry decision: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatal("call was neither acknowledged nor reported as a provider failure")
				}
				assert.Equal(t, int64(1), service.requests.Load())
				if disposition == ClaimExecute {
					require.Len(t, handler.observations, 1)
					observation := <-handler.observations
					assert.Equal(t, executionDeadline, observation.deadline)
					if timing.offset < 0 {
						require.ErrorIs(t, observation.err, context.DeadlineExceeded)
					} else {
						require.NoError(t, observation.err)
					}
					assert.Equal(t, int64(1), completed.Load())
				} else {
					assert.Empty(t, handler.observations)
					assert.Zero(t, completed.Load())
				}

				// An acknowledged non-execution decision must leave Serve able to
				// accept another call through the same registry connection.
				next := testToolCallEvent(t, "following-call")
				next.ID = "2-0"
				events <- next
				select {
				case id := <-acknowledgements:
					assert.Equal(t, "2-0", id)
				case err := <-errc:
					t.Fatalf("provider stopped before the following call: %v", err)
				case <-time.After(3 * time.Second):
					t.Fatal("provider did not acknowledge the following call")
				}
				assert.Equal(t, int64(2), service.requests.Load())
				require.Len(t, handler.observations, 1)
				require.NoError(t, (<-handler.observations).err)
				cancel()
				select {
				case err := <-errc:
					require.ErrorIs(t, err, context.Canceled)
				case <-time.After(3 * time.Second):
					t.Fatal("provider did not finish after cancellation")
				}
				assert.Equal(t, int64(1), released.Load())
			})
		}
	}
}

func TestClaimToolCallGeneratedGRPCHonorsLifecycleCancellation(t *testing.T) {
	t.Parallel()

	service := &blockedDeadlineClaimService{
		started:        make(chan struct{}),
		clientReturned: make(chan struct{}),
	}
	claim := newGeneratedClaimCallback(t, service)
	t.Cleanup(func() { close(service.clientReturned) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := claimToolCall(
			ctx,
			registrationConfig{claim: claim},
			5*time.Second,
			"test.toolset",
			testProviderID,
			testProviderIncarnationID,
			testRegistrationTokenA,
			toolregistry.ToolCallMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         "blocked-call",
			},
			"1-0",
		)
		result <- err
	}()
	select {
	case <-service.started:
	case <-time.After(3 * time.Second):
		t.Fatal("claim did not reach the registry service")
	}
	cancel()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.Canceled)
	case <-time.After(3 * time.Second):
		t.Fatal("claim did not return after cancellation")
	}
}

func TestClaimToolCallHonorsTimeout(t *testing.T) {
	t.Parallel()

	// Observe the callback's own context so a remote gRPC timer cannot compete
	// with the local timeout this test checks.
	registration := registrationConfig{
		claim: func(ctx context.Context, _ ClaimRequest) (ClaimDisposition, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
	}
	result := make(chan error, 1)
	go func() {
		_, err := claimToolCall(
			t.Context(),
			registration,
			10*time.Millisecond,
			"test.toolset",
			testProviderID,
			testProviderIncarnationID,
			testRegistrationTokenA,
			toolregistry.ToolCallMessage{
				RegistrationToken: testRegistrationTokenA,
				ToolUseID:         "blocked-call",
			},
			"1-0",
		)
		result <- err
	}()
	select {
	case err := <-result:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(3 * time.Second):
		t.Fatal("claim did not return after its timeout")
	}
}

// ClaimToolCall records requests that actually crossed gRPC and respects a
// canceled server context before returning the configured registry decision.
func (s *deadlineClaimService) ClaimToolCall(
	ctx context.Context,
	payload *genregistry.ClaimToolCallPayload,
) (*genregistry.ClaimToolCallResult, error) {
	s.requests.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	disposition := s.disposition
	if payload.ToolUseID == "following-call" {
		disposition = ClaimExecute
	}
	return &genregistry.ClaimToolCallResult{Disposition: string(disposition)}, nil
}

// ClaimToolCall waits for the request context to end and for the client to
// observe that cancellation before returning the server error.
func (s *blockedDeadlineClaimService) ClaimToolCall(
	ctx context.Context,
	_ *genregistry.ClaimToolCallPayload,
) (*genregistry.ClaimToolCallResult, error) {
	close(s.started)
	<-ctx.Done()
	<-s.clientReturned
	return nil, ctx.Err()
}

// HandleToolCall records the context delivered to tool execution, then returns
// its cancellation error or an ordinary result.
func (h *deadlineClaimHandler) HandleToolCall(
	ctx context.Context,
	message toolregistry.ToolCallMessage,
) (toolregistry.ToolResultMessage, error) {
	deadline, _ := ctx.Deadline()
	h.observations <- deadlineClaimObservation{deadline: deadline, err: ctx.Err()}
	if err := ctx.Err(); err != nil {
		return toolregistry.ToolResultMessage{}, err
	}
	return toolregistry.NewToolResultMessage(
		message.RegistrationToken, message.ToolUseID, json.RawMessage(`{"ok":true}`),
	), nil
}

// newGeneratedClaimCallback runs the generated registry server and adapts its
// generated client to the provider callback. Cleanup closes both transports.
func newGeneratedClaimCallback(
	t *testing.T,
	service genregistry.Service,
) func(context.Context, ClaimRequest) (ClaimDisposition, error) {
	t.Helper()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	server := grpc.NewServer()
	genregistrypb.RegisterRegistryServer(
		server, genregistrysrv.New(genregistry.NewEndpoints(service), nil),
	)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- server.Serve(listener)
	}()
	t.Cleanup(func() {
		server.Stop()
		require.NoError(t, <-serverErr)
	})
	connection, err := grpc.NewClient(
		listener.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, connection.Close())
	})
	transport := genregistrygrpc.NewClient(connection, grpc.WaitForReady(true))
	client := &genregistry.Client{ClaimToolCallEndpoint: transport.ClaimToolCall()}
	return func(ctx context.Context, request ClaimRequest) (ClaimDisposition, error) {
		result, err := client.ClaimToolCall(ctx, &genregistry.ClaimToolCallPayload{
			Toolset:                   request.Toolset,
			ProviderID:                request.ProviderID,
			ProviderIncarnationID:     request.ProviderIncarnationID,
			ProviderRegistrationToken: request.ProviderRegistrationToken,
			CallRegistrationToken:     request.CallRegistrationToken,
			ToolUseID:                 request.ToolUseID,
			RequestEventID:            request.RequestEventID,
			ClaimOperationID:          request.OperationID,
		})
		if err != nil {
			return "", err
		}
		return ClaimDisposition(result.Disposition), nil
	}
}
