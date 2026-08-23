package gateway

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestHandlerStreamCloseCancelsBlockedSend(t *testing.T) {
	sendStarted := make(chan struct{})
	sendReturned := make(chan error, 1)
	stream := newHandlerStream(t.Context(), func(_ context.Context, send func(model.Chunk) error) (*model.Response, error) {
		close(sendStarted)
		err := send(model.StopChunk{Reason: "done"})
		sendReturned <- err
		return nil, err
	})
	<-sendStarted

	err := stream.Close()

	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, <-sendReturned, context.Canceled)
}

func TestHandlerStreamCloseWaitsForCleanupAndReturnsItsError(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	cleanupStarted := make(chan struct{})
	cleanupRelease := make(chan struct{})
	stream := newHandlerStream(t.Context(), func(ctx context.Context, _ func(model.Chunk) error) (*model.Response, error) {
		<-ctx.Done()
		close(cleanupStarted)
		<-cleanupRelease
		return nil, cleanupErr
	})
	returned := make(chan error, 1)
	go func() {
		returned <- stream.Close()
	}()
	<-cleanupStarted

	select {
	case err := <-returned:
		t.Fatalf("Close returned before cleanup completed: %v", err)
	default:
	}
	close(cleanupRelease)
	require.ErrorIs(t, <-returned, cleanupErr)
}

func TestHandlerStreamCloseIsIdempotent(t *testing.T) {
	cleanupErr := errors.New("cleanup failed")
	stream := newHandlerStream(t.Context(), func(ctx context.Context, _ func(model.Chunk) error) (*model.Response, error) {
		<-ctx.Done()
		return nil, cleanupErr
	})

	require.ErrorIs(t, stream.Close(), cleanupErr)
	require.ErrorIs(t, stream.Close(), cleanupErr)
}

func TestHandlerStreamConcurrentCancellation(t *testing.T) {
	parent, cancelParent := context.WithCancel(t.Context())
	stream := newHandlerStream(parent, func(ctx context.Context, _ func(model.Chunk) error) (*model.Response, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})

	const callers = 16
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- stream.Close()
		}()
	}
	cancelParent()
	wg.Wait()
	close(results)

	for err := range results {
		require.ErrorIs(t, err, context.Canceled)
	}
}
