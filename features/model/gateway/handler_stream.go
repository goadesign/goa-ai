// Package gateway adapts push-based middleware handlers to the pull-based
// model.Streamer contract. The adapter preserves one-at-a-time backpressure but
// deliberately leaves canonical chunk and response validation to the consuming
// model.Client.
package gateway

import (
	"context"
	"io"
	"sync"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// handlerStream runs one middleware chain and exposes its chunks and final
	// response through model.Streamer.
	handlerStream struct {
		cancel context.CancelFunc
		chunks chan model.Chunk
		done   chan struct{}

		mu       sync.Mutex
		response *model.Response
		err      error
	}

	// streamRun invokes one complete push-based stream handler.
	streamRun func(context.Context, func(model.Chunk) error) (*model.Response, error)
)

// newHandlerStream starts one handler whose send calls block until Recv accepts
// each chunk. Closing the stream cancels the handler and waits for cleanup.
func newHandlerStream(parent context.Context, run streamRun) *handlerStream {
	ctx, cancel := context.WithCancel(parent)
	stream := &handlerStream{
		cancel: cancel,
		chunks: make(chan model.Chunk),
		done:   make(chan struct{}),
	}
	go stream.run(ctx, run)
	return stream
}

// Recv returns the next raw middleware-produced chunk, the handler error, or
// EOF after the handler returns its provider response.
func (s *handlerStream) Recv() (model.Chunk, error) {
	chunk, ok := <-s.chunks
	if ok {
		return chunk, nil
	}
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	return nil, io.EOF
}

// Response returns the middleware response after Recv reports EOF.
func (s *handlerStream) Response() *model.Response {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.response
}

// Close cancels the middleware handler, waits until it releases its provider
// stream, and returns the handler's terminal or cleanup error. Repeated calls
// return the same saved result.
func (s *handlerStream) Close() error {
	s.cancel()
	<-s.done
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}

// run executes the middleware chain and closes the chunk channel exactly once.
func (s *handlerStream) run(ctx context.Context, run streamRun) {
	response, err := run(ctx, func(chunk model.Chunk) error {
		select {
		case s.chunks <- chunk:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	s.mu.Lock()
	if err == nil {
		s.response = response
	}
	s.err = err
	s.mu.Unlock()
	close(s.chunks)
	close(s.done)
}
