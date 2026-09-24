package provider

// Shutdown reports whether settlement completed within the caller's deadline.
// A transport that ignores cancellation must not hold Serve open or cause its
// unsettled registration lease to be released.

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	pulse "goa.design/goa-ai/features/stream/pulse/clients/pulse"
	mockpulse "goa.design/goa-ai/features/stream/pulse/clients/pulse/mocks"
	"goa.design/pulse/streaming"
	streamopts "goa.design/pulse/streaming/options"
)

func TestCloseSinkBoundedCompletion(t *testing.T) {
	t.Parallel()
	closeFailure := errors.New("transport close failed")
	for _, test := range []struct {
		name     string
		closeErr error
		expire   bool
	}{
		{name: "successful close"},
		{name: "close error", closeErr: closeFailure},
		{name: "deadline and successful completion", expire: true},
		{name: "deadline and failed completion", expire: true, closeErr: closeFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			finished := make(chan struct{})
			sink := mockpulse.NewSink(t)
			sink.SetClose(func(got context.Context) error {
				defer close(finished)
				assert.Same(t, ctx, got)
				if test.expire {
					cancel()
				}
				return test.closeErr
			})
			err := closeSinkBounded(ctx, sink)
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("Close did not complete")
			}
			switch {
			case test.expire:
				require.ErrorIs(t, err, context.Canceled)
			case test.closeErr != nil:
				require.ErrorIs(t, err, closeFailure)
			default:
				require.NoError(t, err)
			}
		})
	}
}

func TestWaitForSinkCloseCompletedAtCancellation(t *testing.T) {
	t.Parallel()
	closeFailure := errors.New("transport close failed")
	for _, test := range []struct {
		name     string
		closeErr error
	}{
		{name: "successful close"},
		{name: "failed close", closeErr: closeFailure},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			closed := make(chan error, 1)
			closed <- test.closeErr
			// Both choices are ready before the wait starts. Either selection
			// must report cancellation and preserve the completed Close error.
			err := waitForSinkClose(ctx, closed)
			require.ErrorIs(t, err, context.Canceled)
			if test.closeErr != nil {
				require.ErrorIs(t, err, closeFailure)
			}
		})
	}
}

func TestServeIncompleteSinkClosePreservesLease(t *testing.T) {
	t.Parallel()
	events := make(chan *streaming.Event)
	subscribed := make(chan struct{})
	closeStarted := make(chan struct{})
	allowClose := make(chan struct{})
	closeFinished := make(chan struct{})
	var closes, drains, releases atomic.Int64
	sink := mockpulse.NewSink(t)
	sink.SetSubscribe(func() <-chan *streaming.Event {
		close(subscribed)
		return events
	})
	sink.SetAck(func(context.Context, *streaming.Event) error {
		return nil
	})
	sink.SetClose(func(ctx context.Context) error {
		closes.Add(1)
		close(closeStarted)
		<-allowClose
		defer close(closeFinished)
		return ctx.Err()
	})
	stream := mockpulse.NewStream(t)
	stream.SetNewSink(func(context.Context, string, ...streamopts.Sink) (pulse.Sink, error) {
		return sink, nil
	})
	client := mockpulse.NewClient(t)
	client.SetStream(func(string, ...streamopts.Stream) (pulse.Stream, error) {
		return stream, nil
	})
	registration := successfulRegistration()
	registration.Drain = func(context.Context, string, string, string, string, time.Duration) error {
		drains.Add(1)
		return nil
	}
	registration.Release = func(context.Context, string, string, string, string) error {
		releases.Add(1)
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- Serve(ctx, client, "test.toolset", &recordingHandler{}, registration, Options{
			ProviderID: testProviderID,
			Pong: func(context.Context, string, string, string) error {
				return nil
			},
			ShutdownTimeout: 20 * time.Millisecond,
		})
	}()
	t.Cleanup(func() {
		close(allowClose)
		select {
		case <-closeFinished:
		case <-time.After(time.Second):
			t.Error("late transport close did not finish")
		}
	})
	select {
	case <-subscribed:
	case <-time.After(time.Second):
		t.Fatal("provider did not subscribe")
	}
	cancel()
	select {
	case <-closeStarted:
	case <-time.After(time.Second):
		t.Fatal("transport Close did not start")
	}
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, context.DeadlineExceeded)
		require.ErrorContains(t, err, "close toolset sink incomplete")
	case <-time.After(time.Second):
		t.Fatal("Serve did not respect its settlement deadline")
	}
	assert.Equal(t, int64(1), closes.Load())
	assert.Equal(t, int64(1), drains.Load())
	assert.Zero(t, releases.Load(), "an incomplete close cannot release its lease")
	select {
	case <-closeFinished:
		t.Fatal("transport should remain blocked until explicitly released")
	default:
	}
}
