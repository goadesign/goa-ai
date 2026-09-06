package provider

// These tests pass the cancellation returned by generated Goa gRPC clients
// through Serve. Normal shutdown must finish registration cleanup without
// reporting another failure, while failed lease release must remain visible.

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	goagrpc "goa.design/goa/v3/grpc"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestServeGeneratedClientCancellation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		transportErr error
		releaseErr   error
	}{
		{name: "clean registration cleanup", transportErr: status.Error(codes.Canceled, "registry request canceled")},
		{name: "single transport join", transportErr: errors.Join(status.Error(codes.Canceled, "registry request canceled"))},
		{name: "failed lease release", transportErr: errors.Join(status.Error(codes.Canceled, "registry request canceled")), releaseErr: errors.New("registry refused lease release")},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := make(chan *streaming.Event)
			sink := mockpulse.NewSink(t)
			sink.SetSubscribe(func() <-chan *streaming.Event { return events })
			sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
			sink.SetClose(func(context.Context) error { return nil })
			stream := mockpulse.NewStream(t)
			stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
				return sink, nil
			})
			client := mockpulse.NewClient(t)
			client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
				return stream, nil
			})

			renewalStarted := make(chan struct{})
			var registrations, drains, releases atomic.Int64
			registration := successfulRegistration()
			registration.AttemptTimeout = 500 * time.Millisecond
			registration.RetryInitialInterval = 50 * time.Millisecond
			registration.RetryMaxInterval = 50 * time.Millisecond
			registration.ShutdownMargin = 10 * time.Millisecond
			registration.ReleaseTimeout = time.Second
			registration.Register = func(ctx context.Context, _, _, _, _ string) (RegistrationLease, error) {
				if registrations.Add(1) == 1 {
					return RegistrationLease{RegistrationToken: testRegistrationTokenA, Duration: time.Second}, nil
				}
				close(renewalStarted)
				<-ctx.Done()
				return RegistrationLease{}, goagrpc.ContextError(ctx, test.transportErr)
			}
			registration.Drain = func(context.Context, string, string, string, string, time.Duration) error {
				drains.Add(1)
				return nil
			}
			registration.Release = func(context.Context, string, string, string, string) error {
				releases.Add(1)
				return test.releaseErr
			}

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			done := make(chan error, 1)
			go func() {
				done <- Serve(ctx, client, "test.toolset", &recordingHandler{}, registration, Options{
					ProviderID:      testProviderID,
					Pong:            func(context.Context, string, string, string) error { return nil },
					ShutdownTimeout: time.Second,
				})
			}()
			select {
			case <-renewalStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("registration renewal did not start")
			}
			cancel()
			select {
			case err := <-done:
				require.ErrorIs(t, err, context.Canceled)
				if test.releaseErr == nil {
					assert.True(t, containsOnlyCancellation(err), "unexpected shutdown failure: %v", err)
				} else {
					require.ErrorIs(t, err, test.releaseErr)
					require.ErrorContains(t, err, test.releaseErr.Error())
					assert.False(t, containsOnlyCancellation(err))
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Serve did not finish registration cleanup")
			}
			assert.Equal(t, int64(1), drains.Load())
			if test.releaseErr == nil {
				assert.Equal(t, int64(1), releases.Load())
			} else {
				assert.Positive(t, releases.Load())
			}
		})
	}
}

func TestContainsOnlyCancellationUsesCurrentErrorCauses(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	transportErr := status.Error(codes.Canceled, "registry request canceled")
	cleanupErr := errors.New("registry lease release failed")
	for _, test := range []struct {
		name         string
		transportErr error
	}{
		{name: "direct status", transportErr: transportErr},
		{name: "single transport join", transportErr: errors.Join(transportErr)},
		{name: "wrapped single transport join", transportErr: fmt.Errorf("registry client: %w", errors.Join(transportErr))},
		{name: "nested single transport join", transportErr: errors.Join(fmt.Errorf("registry client: %w", errors.Join(transportErr)))},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			clientErr := goagrpc.ContextError(ctx, test.transportErr)
			require.Error(t, clientErr)
			assert.True(t, containsOnlyCancellation(clientErr))
			assert.True(t, containsOnlyCancellation(fmt.Errorf("renew registration: %w", clientErr)))
			assert.True(t, containsOnlyCancellation(errors.Join(clientErr, context.Canceled)))
			assert.Equal(t, context.Canceled, joinProviderStopErrors(context.Canceled, clientErr))

			mixed := fmt.Errorf("stop provider: %w", errors.Join(clientErr, cleanupErr))
			assert.False(t, containsOnlyCancellation(mixed))
			joined := joinProviderStopErrors(context.Canceled, mixed)
			require.ErrorIs(t, joined, cleanupErr)
			require.ErrorContains(t, joined, mixed.Error())
		})
	}
}
