package vertex

import (
	"context"
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
