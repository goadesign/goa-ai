// Provider operations and accepted handlers have different lifetimes. These
// tests preserve existing option defaults while allowing shutdown to wait longer
// without widening the deadlines passed to Registry and the stream consumer.
package provider

import (
	"context"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/goa-ai/runtime/toolregistry"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestProviderOperationTimeoutPreservesExistingDefaults(t *testing.T) {
	for _, test := range []struct {
		name                      string
		shutdown, operation, want time.Duration
	}{
		{"defaults", 0, 0, DefaultShutdownTimeout},
		{"existing short setting", time.Second, 0, time.Second},
		{"existing long setting", time.Hour, 0, time.Hour},
		{"independent short operations", time.Minute, time.Second, time.Second},
		{"independent long operations", time.Second, time.Minute, time.Minute},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				events := make(chan *streaming.Event, 1)
				events <- testToolCallEvent(t, "operation-deadlines")
				observed := make(chan time.Duration, 3)
				acked := make(chan struct{})
				registration := successfulRegistration()
				registration.Claim = func(ctx context.Context, _ ClaimRequest) (ClaimDisposition, error) {
					deadline, ok := ctx.Deadline()
					assert.True(t, ok)
					observed <- time.Until(deadline)
					return ClaimExecute, nil
				}
				registration.Complete = func(ctx context.Context, _, _, _, _, _ string, _ toolregistry.ToolResultMessage) (bool, error) {
					deadline, ok := ctx.Deadline()
					assert.True(t, ok)
					observed <- time.Until(deadline)
					return true, nil
				}
				sink := mockpulse.NewSink(t)
				sink.SetSubscribe(func() <-chan *streaming.Event { return events })
				sink.SetClose(func(context.Context) error { return nil })
				sink.SetAck(func(ctx context.Context, _ *streaming.Event) error {
					deadline, ok := ctx.Deadline()
					assert.True(t, ok)
					observed <- time.Until(deadline)
					close(acked)
					return nil
				})
				stream := mockpulse.NewStream(t)
				stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) { return sink, nil })
				client := mockpulse.NewClient(t)
				client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
				done := make(chan error, 1)
				go func() {
					done <- ServeAndWait(ctx, client, "test.toolset", &recordingHandler{}, registration, Options{
						ProviderID: testProviderID, Pong: func(context.Context, string, string, string) error { return nil },
						ShutdownTimeout: test.shutdown, OperationTimeout: test.operation, EnsureInterval: time.Hour,
					})
				}()
				<-acked
				for range 3 {
					assert.Equal(t, test.want, <-observed)
				}
				cancel()
				err := <-done
				require.ErrorIs(t, err, context.Canceled)
				assert.True(t, containsOnlyCancellation(err))
			})
		})
	}
}

func TestOperationTimeoutDoesNotShortenAcceptedHandlerDrain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		events := make(chan *streaming.Event, 1)
		events <- testToolCallEvent(t, "accepted")
		sink := mockpulse.NewSink(t)
		sink.SetSubscribe(func() <-chan *streaming.Event { return events })
		sink.SetClose(func(context.Context) error { return nil })
		sink.SetAck(func(context.Context, *streaming.Event) error { return nil })
		stream := mockpulse.NewStream(t)
		stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) { return sink, nil })
		client := mockpulse.NewClient(t)
		client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) { return stream, nil })
		handler := &blockingHandler{started: make(chan struct{}), unblock: make(chan struct{}), canceled: make(chan struct{})}
		done := make(chan error, 1)
		go func() {
			done <- ServeAndWait(ctx, client, "test.toolset", handler, successfulRegistration(), Options{
				ProviderID: testProviderID, Pong: func(context.Context, string, string, string) error { return nil },
				OperationTimeout: 20 * time.Millisecond, ShutdownTimeout: time.Second, EnsureInterval: time.Hour,
			})
		}()
		<-handler.started
		cancel()
		time.Sleep(60 * time.Millisecond)
		select {
		case <-handler.canceled:
			t.Error("operation timeout canceled an accepted handler during drain")
		default:
		}
		close(handler.unblock)
		err := <-done
		require.ErrorIs(t, err, context.Canceled)
		assert.True(t, containsOnlyCancellation(err))
	})
}
