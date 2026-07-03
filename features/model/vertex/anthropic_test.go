package vertex

import (
	"context"
	"io"
	"net/http"
	"testing"

	sdkerr "github.com/anthropics/anthropic-sdk-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

type stubModelClient struct {
	err  error
	resp *model.Response
}

func (s *stubModelClient) Complete(context.Context, *model.Request) (*model.Response, error) {
	return s.resp, s.err
}

func (s *stubModelClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, s.err
}

func TestNewAnthropicClientValidates(t *testing.T) {
	_, err := NewAnthropicClient(context.Background(), AnthropicOptions{Region: "global"})
	require.Error(t, err) // missing project
	_, err = NewAnthropicClient(context.Background(), AnthropicOptions{ProjectID: "p"})
	require.Error(t, err) // missing region
	_, err = NewAnthropicClient(context.Background(), AnthropicOptions{ProjectID: "p", Region: "global"})
	require.Error(t, err) // missing default model
}

func TestAnthropicErrorMapperMaps429(t *testing.T) {
	apiErr := &sdkerr.Error{StatusCode: http.StatusTooManyRequests}
	mapped := &anthropicErrorMapper{next: &stubModelClient{err: apiErr}}
	_, err := mapped.Complete(context.Background(), &model.Request{})
	require.ErrorIs(t, err, model.ErrRateLimited)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindRateLimited, pe.Kind())
}

func TestAnthropicErrorMapperPassesSuccess(t *testing.T) {
	want := &model.Response{StopReason: "end_turn"}
	mapped := &anthropicErrorMapper{next: &stubModelClient{resp: want}}
	got, err := mapped.Complete(context.Background(), &model.Request{})
	require.NoError(t, err)
	assert.Same(t, want, got)
}

func TestAnthropicErrorMapperStreamMaps429(t *testing.T) {
	apiErr := &sdkerr.Error{StatusCode: http.StatusTooManyRequests}
	mapped := &anthropicErrorMapper{next: &stubModelClient{err: apiErr}}
	_, err := mapped.Stream(context.Background(), &model.Request{})
	require.ErrorIs(t, err, model.ErrRateLimited)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindRateLimited, pe.Kind())
}

type stubStreamer struct {
	err    error
	closed bool
}

func (s *stubStreamer) Recv() (model.Chunk, error) { return model.Chunk{}, s.err }
func (s *stubStreamer) Close() error               { s.closed = true; return nil }
func (s *stubStreamer) Metadata() map[string]any   { return map[string]any{"k": "v"} }

func TestAnthropicStreamerMapperMapsMidStream429(t *testing.T) {
	apiErr := &sdkerr.Error{StatusCode: http.StatusTooManyRequests}
	s := &anthropicStreamerMapper{next: &stubStreamer{err: apiErr}}
	_, err := s.Recv()
	require.ErrorIs(t, err, model.ErrRateLimited)
	pe, ok := model.AsProviderError(err)
	require.True(t, ok)
	assert.Equal(t, model.ProviderErrorKindRateLimited, pe.Kind())
}

func TestAnthropicStreamerMapperPassesEOFAndContextErrors(t *testing.T) {
	for _, sentinel := range []error{io.EOF, context.Canceled, context.DeadlineExceeded} {
		s := &anthropicStreamerMapper{next: &stubStreamer{err: sentinel}}
		_, err := s.Recv()
		assert.Equal(t, sentinel, err)
		_, mapped := model.AsProviderError(err)
		assert.False(t, mapped)
	}
}

func TestAnthropicStreamerMapperDelegatesCloseAndMetadata(t *testing.T) {
	next := &stubStreamer{}
	s := &anthropicStreamerMapper{next: next}
	require.NoError(t, s.Close())
	assert.True(t, next.closed)
	assert.Equal(t, map[string]any{"k": "v"}, s.Metadata())
}

func TestAnthropicErrorMapperStreamWrapsStreamer(t *testing.T) {
	inner := &stubStreamer{err: io.EOF}
	mapped := &anthropicErrorMapper{next: &streamingStubClient{streamer: inner}}
	s, err := mapped.Stream(context.Background(), &model.Request{})
	require.NoError(t, err)
	_, ok := s.(*anthropicStreamerMapper)
	assert.True(t, ok, "Stream must wrap the underlying streamer in anthropicStreamerMapper")
}

type streamingStubClient struct {
	streamer model.Streamer
}

func (s *streamingStubClient) Complete(context.Context, *model.Request) (*model.Response, error) {
	return nil, nil
}

func (s *streamingStubClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return s.streamer, nil
}
