// These tests exercise generated registry gRPC clients against a real server.
// Local cancellation must retain context errors and gRPC diagnostics, while
// remote interruptions remain separate errors. Claim retries retain their
// operation ID.
package registry

import (
	"context"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	genregistrygrpc "goa.design/goa-ai/registry/gen/grpc/registry/client"
	genregistrypb "goa.design/goa-ai/registry/gen/grpc/registry/pb"
	genregistrysrv "goa.design/goa-ai/registry/gen/grpc/registry/server"
	genregistry "goa.design/goa-ai/registry/gen/registry"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

// blockingRegisterService withholds its application response until the client
// returns. The gRPC transport can still reset the stream when its deadline expires.
type blockingRegisterService struct {
	genregistry.Service
	started        chan struct{}
	clientReturned chan struct{}
}

// recordingClaimService records each operation ID received by the generated
// ClaimToolCall server and returns the execute decision.
type recordingClaimService struct {
	genregistry.Service
	mu           sync.Mutex
	operationIDs []string
}

// Register waits for server context cancellation and the client's return before
// replying. This prevents an application error response from competing with the
// client's result; it does not control gRPC transport deadline resets.
func (s *blockingRegisterService) Register(ctx context.Context, _ *genregistry.RegisterPayload) (*genregistry.RegisterResult, error) {
	close(s.started)
	<-ctx.Done()
	<-s.clientReturned
	return nil, ctx.Err()
}

// ClaimToolCall records the transport request so the test can prove that an
// automatic retry preserves the operation identity.
func (s *recordingClaimService) ClaimToolCall(
	_ context.Context,
	payload *genregistry.ClaimToolCallPayload,
) (*genregistry.ClaimToolCallResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.operationIDs = append(s.operationIDs, payload.ClaimOperationID)
	return &genregistry.ClaimToolCallResult{Disposition: string(callClaimExecute)}, nil
}

func TestGeneratedGRPCClientPreservesCanceledRegisterContext(t *testing.T) {
	t.Parallel()

	service := &blockingRegisterService{
		started:        make(chan struct{}),
		clientReturned: make(chan struct{}),
	}
	defer close(service.clientReturned)
	client := newGeneratedRegisterClient(t, service)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, registerErr := client.Register(ctx, validRegisterPayloadForSchemaAdmission("cancellation-test"))
		result <- registerErr
	}()

	select {
	case <-service.started:
	case <-ctx.Done():
		t.Fatal("Register did not reach the gRPC service")
	}
	cancel()

	err := <-result
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorContains(t, err, "rpc error: code = Canceled")
	require.Equal(t, codes.Canceled, status.Code(err))
}

func TestGeneratedGRPCClientPreservesRegisterDeadline(t *testing.T) {
	t.Parallel()

	service := &blockingRegisterService{
		started:        make(chan struct{}),
		clientReturned: make(chan struct{}),
	}
	defer close(service.clientReturned)
	client := newGeneratedRegisterClient(t, service)
	// End the caller context before invoking the client so error decoding cannot
	// race with the context's deadline timer.
	ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
	defer cancel()
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)

	_, err := client.Register(ctx, validRegisterPayloadForSchemaAdmission("deadline-test"))
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.ErrorContains(t, err, "rpc error: code = DeadlineExceeded")
	require.Equal(t, codes.DeadlineExceeded, status.Code(err))
}

func TestGeneratedGRPCClientKeepsRemoteInterruptionSeparateFromCallerContext(t *testing.T) {
	t.Parallel()

	for _, code := range []codes.Code{codes.Canceled, codes.DeadlineExceeded} {
		t.Run(code.String(), func(t *testing.T) {
			t.Parallel()

			service := &blockingRegisterService{
				started:        make(chan struct{}),
				clientReturned: make(chan struct{}),
			}
			defer close(service.clientReturned)
			remoteErr := status.Error(code, "remote registration interrupted")
			client := newGeneratedRegisterClient(
				t,
				service,
				grpc.UnaryInterceptor(func(context.Context, any, *grpc.UnaryServerInfo, grpc.UnaryHandler) (any, error) {
					return nil, remoteErr
				}),
			)
			ctx := context.Background()

			_, err := client.Register(ctx, validRegisterPayloadForSchemaAdmission("remote-interruption-test"))
			require.Error(t, err)
			require.NoError(t, ctx.Err())
			require.ErrorContains(t, err, remoteErr.Error())
			require.NotErrorIs(t, err, context.Canceled)
			require.NotErrorIs(t, err, context.DeadlineExceeded)
		})
	}
}

func TestGeneratedGRPCClientRetriesClaimWithSameOperationID(t *testing.T) {
	t.Parallel()

	service := &recordingClaimService{}
	var attempts atomic.Int32
	client := newGeneratedRegisterClient(
		t,
		service,
		grpc.UnaryInterceptor(func(
			ctx context.Context,
			request any,
			_ *grpc.UnaryServerInfo,
			handler grpc.UnaryHandler,
		) (any, error) {
			response, err := handler(ctx, request)
			if err == nil && attempts.Add(1) == 1 {
				return nil, status.Error(codes.Unavailable, "response lost after commit")
			}
			return response, err
		}),
	)
	provider := validRegisterPayloadForSchemaAdmission("claim-retry-test")
	operationID := uuid.NewString()
	result, err := client.ClaimToolCall(context.Background(), &genregistry.ClaimToolCallPayload{
		Toolset:                   provider.Name,
		ProviderID:                provider.ProviderID,
		ProviderIncarnationID:     provider.ProviderIncarnationID,
		ProviderRegistrationToken: strings.Repeat("a", 64),
		CallRegistrationToken:     strings.Repeat("a", 64),
		ToolUseID:                 "claim-retry-tool-use",
		RequestEventID:            "1-0",
		ClaimOperationID:          operationID,
	})
	require.NoError(t, err)
	require.Equal(t, string(callClaimExecute), result.Disposition)
	require.Equal(t, int32(2), attempts.Load())
	service.mu.Lock()
	defer service.mu.Unlock()
	require.Equal(t, []string{operationID, operationID}, service.operationIDs)
}

// newGeneratedRegisterClient runs a real generated gRPC server and returns its
// generated service client. Test cleanup stops both ends of the connection.
func newGeneratedRegisterClient(
	t *testing.T,
	service genregistry.Service,
	serverOptions ...grpc.ServerOption,
) *genregistry.Client {
	t.Helper()
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "localhost:0")
	require.NoError(t, err)

	server := grpc.NewServer(serverOptions...)
	genregistrypb.RegisterRegistryServer(
		server,
		genregistrysrv.New(genregistry.NewEndpoints(service), nil),
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
	return &genregistry.Client{
		RegisterEndpoint:      transport.Register(),
		ClaimToolCallEndpoint: transport.ClaimToolCall(),
	}
}
