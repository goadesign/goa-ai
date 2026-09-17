// Native search streaming uses the same request and replay logic as unary
// calls. These tests pin logical completion, cleanup, and failure accounting
// across several physical SDK streams.
package openai

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3/responses"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	searchStreamTransport struct {
		streams  []responseStream
		requests []responses.ResponseNewParams
	}

	searchCancellationTransport struct {
		first        responseStream
		next         *contextSearchStream
		opening      chan struct{}
		cancelOnOpen bool
	}

	contextSearchStream struct {
		ctx     context.Context
		reading chan struct{}
		closed  atomic.Int32
	}
)

func TestNativeSearchStreamReturnsOneLogicalResponse(t *testing.T) {
	first := searchResponseStream(t, searchCallJSON)
	second := searchResponseStream(t, searchToolJSON)
	transport := &searchStreamTransport{streams: []responseStream{first, second}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	stream, err := client.Stream(t.Context(), searchRequest())
	require.NoError(t, err)
	var stops, usages, tools int
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
		switch chunk.(type) {
		case model.StopChunk:
			stops++
		case model.UsageChunk:
			usages++
		case model.ToolCallChunk:
			tools++
		}
	}
	require.NoError(t, stream.Close())
	assert.Equal(t, 1, stops)
	assert.Equal(t, 1, usages)
	assert.Equal(t, 1, tools)
	assert.Equal(t, 24, stream.Response().Usage.TotalTokens)
	require.Len(t, transport.requests, 2)
	assert.Equal(t, int64(28), transport.requests[1].MaxOutputTokens.Value)
	assert.Equal(t, 1, first.closeCalls)
	assert.Equal(t, 1, second.closeCalls)
}

func TestNativeSearchStreamFailureAndBudgetPreserveUsage(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("connection lost")} {
		first := searchResponseStream(t, searchCallJSON)
		second := &mockStream{err: cause}
		transport := &searchStreamTransport{streams: []responseStream{first, second}}
		client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
		require.NoError(t, err)
		stream, err := client.Stream(t.Context(), searchRequest())
		require.NoError(t, err)
		_, err = stream.Recv()
		require.ErrorIs(t, err, cause)
		require.NotNil(t, model.UsageFromError(err))
		assert.Equal(t, 12, model.UsageFromError(err).TotalTokens)
		require.NoError(t, stream.Close())
		assert.Equal(t, 1, first.closeCalls)
		assert.Equal(t, 1, second.closeCalls)
	}
	first := searchResponseStream(t, searchCallJSON)
	transport := &searchStreamTransport{streams: []responseStream{first}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	request := searchRequest()
	request.MaxTokens = 2
	stream, err := client.Stream(t.Context(), request)
	require.NoError(t, err)
	_, err = stream.Recv()
	var rejected *model.OutputValidationError
	require.ErrorAs(t, err, &rejected)
	assert.Equal(t, 12, rejected.Usage().TotalTokens)
	require.NoError(t, stream.Close())
	assert.Equal(t, 1, first.closeCalls)
	assert.Len(t, transport.requests, 1)
}

func TestNativeSearchStreamFailedResponseRetainsFinalUsage(t *testing.T) {
	first := searchResponseStream(t, searchCallJSON)
	failed := &mockStream{events: []responses.ResponseStreamEventUnion{mustStreamEvent(t,
		`{"type":"response.failed","response":{"status":"failed","model":"gpt-5.4","output":[],"error":{"code":"server_error","message":"provider stopped"},"usage":{"input_tokens":7,"output_tokens":2,"total_tokens":9}}}`),
	}}
	transport := &searchStreamTransport{streams: []responseStream{first, failed}}
	client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
	require.NoError(t, err)
	stream, err := client.Stream(t.Context(), searchRequest())
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorContains(t, err, "provider stopped")
	_, providerFailure := model.AsProviderError(err)
	assert.True(t, providerFailure)
	usage := model.UsageFromError(err)
	require.NotNil(t, usage)
	assert.Equal(t, 21, usage.TotalTokens)
	require.NoError(t, stream.Close())
	assert.Equal(t, 1, first.closeCalls)
	assert.Equal(t, 1, failed.closeCalls)
}

func TestNativeSearchContextCancellationClosesContinuation(t *testing.T) {
	for _, cancelOnOpen := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelOnOpen), func(t *testing.T) {
			first := searchResponseStream(t, searchCallJSON)
			next := &contextSearchStream{reading: make(chan struct{})}
			transport := &searchCancellationTransport{
				first: first, next: next, opening: make(chan struct{}), cancelOnOpen: cancelOnOpen,
			}
			client, err := New(Options{DefaultModel: "gpt-5.4", transport: transport})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			stream, err := client.Stream(ctx, searchRequest())
			require.NoError(t, err)
			<-transport.opening
			if !cancelOnOpen {
				<-next.reading
			}
			cancel()
			_, err = stream.Recv()
			require.ErrorIs(t, err, context.Canceled)
			usage := model.UsageFromError(err)
			require.NotNil(t, usage)
			assert.Equal(t, 12, usage.TotalTokens)
			require.ErrorIs(t, stream.Close(), context.Canceled)
			assert.Equal(t, 1, first.closeCalls)
			assert.Equal(t, int32(1), next.closed.Load())
		})
	}
}

func searchResponseStream(t *testing.T, output string) *mockStream {
	t.Helper()
	response := searchResponse(t, output)
	return &mockStream{events: []responses.ResponseStreamEventUnion{
		mustStreamEvent(t, `{"type":"response.completed","response":`+response.RawJSON()+`}`),
	}}
}

func (*searchStreamTransport) Complete(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
	panic("unary request not configured")
}

func (s *searchStreamTransport) Stream(_ context.Context, request responses.ResponseNewParams) responseStream {
	s.requests = append(s.requests, request)
	next := s.streams[0]
	s.streams = s.streams[1:]
	return next
}

func (*searchCancellationTransport) Complete(context.Context, responses.ResponseNewParams) (*responses.Response, error) {
	panic("unary request not configured")
}

func (s *searchCancellationTransport) Stream(ctx context.Context, _ responses.ResponseNewParams) responseStream {
	if s.first != nil {
		first := s.first
		s.first = nil
		return first
	}
	s.next.ctx = ctx
	close(s.opening)
	if s.cancelOnOpen {
		<-ctx.Done()
	}
	return s.next
}

func (s *contextSearchStream) Next() bool {
	close(s.reading)
	<-s.ctx.Done()
	return false
}

func (*contextSearchStream) Current() responses.ResponseStreamEventUnion {
	panic("canceled stream has no events")
}

func (s *contextSearchStream) Err() error {
	return s.ctx.Err()
}

func (s *contextSearchStream) Close() error {
	s.closed.Add(1)
	return nil
}
