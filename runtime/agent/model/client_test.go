// Package model tests the opaque client boundary around raw providers.
package model

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type clientTestProvider struct {
	calls int
}

type clientTestCountingProvider struct {
	clientTestProvider
	count      TokenCount
	countErr   error
	countCalls int
}

type clientTestProviderMiddleware struct {
	Provider
}

func (p *clientTestProvider) Complete(context.Context, *Request) (*Response, error) {
	p.calls++
	return &Response{
		Content:    []Message{{Role: ConversationRoleAssistant, Parts: []Part{TextPart{Text: "ok"}}}},
		StopReason: "stop",
	}, nil
}

func (p *clientTestProvider) Stream(context.Context, *Request) (Streamer, error) {
	p.calls++
	return nil, ErrStreamingUnsupported
}

func (p *clientTestCountingProvider) CountTokens(context.Context, *Request) (TokenCount, error) {
	p.countCalls++
	return p.count, p.countErr
}

type forgedClient struct {
	Client
}

func TestNewClientRejectsNilProviders(t *testing.T) {
	var typedNil *clientTestProvider

	tests := []struct {
		name     string
		provider Provider
	}{
		{name: "nil"},
		{name: "typed nil", provider: typedNil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, err := NewClient(test.provider)

			require.Nil(t, client)
			require.EqualError(t, err, "model provider is required")
		})
	}
}

func TestClientRejectsOversizedRequestBeforeProviderCall(t *testing.T) {
	provider := &clientTestCountingProvider{}
	client, err := NewClient(provider)
	require.NoError(t, err)
	request := &Request{
		Messages: []*Message{{
			Role:  ConversationRoleUser,
			Parts: []Part{TextPart{Text: strings.Repeat("x", maxDynamicValueBytes+1)}},
		}},
	}

	_, completeErr := client.Complete(t.Context(), request)
	_, streamErr := client.Stream(t.Context(), request)
	_, countErr := client.CountTokens(t.Context(), request)

	require.ErrorContains(t, completeErr, "maximum byte size")
	require.ErrorContains(t, streamErr, "maximum byte size")
	require.ErrorContains(t, countErr, "maximum byte size")
	require.Zero(t, provider.calls)
	require.Zero(t, provider.countCalls)
}

func TestValidateClientRejectsForgedClient(t *testing.T) {
	valid, err := NewClient(&clientTestProvider{})
	require.NoError(t, err)

	forged := &forgedClient{Client: valid}
	require.EqualError(t, ValidateClient(forged), "model client is not an intact validated client")
}

func TestClientRejectsEmptyConfiguredToolNameBeforeProviderCall(t *testing.T) {
	provider := &clientTestProvider{}
	client, err := NewClient(provider)
	require.NoError(t, err)
	request := &Request{Tools: []*ToolDefinition{{}}}

	response, err := client.Complete(t.Context(), request)
	require.Nil(t, response)
	require.EqualError(t, err, "model request contains a tool definition with an empty name")
	stream, err := client.Stream(t.Context(), request)
	require.Nil(t, stream)
	require.EqualError(t, err, "model request contains a tool definition with an empty name")
	require.Zero(t, provider.calls)
}

func TestClientRejectsInvalidProviderTokenCounts(t *testing.T) {
	tests := []struct {
		name  string
		count TokenCount
		err   string
	}{
		{
			name:  "negative count",
			count: TokenCount{Model: "model", ModelClass: ModelClassDefault, InputTokens: -1, Exact: true},
			err:   "model provider returned a negative input token count",
		},
		{
			name:  "wrong model class",
			count: TokenCount{Model: "model", ModelClass: ModelClassSmall, InputTokens: 1, Exact: true},
			err:   "model provider returned a token count for the wrong model class",
		},
		{
			name:  "exact count without model",
			count: TokenCount{ModelClass: ModelClassDefault, InputTokens: 1, Exact: true},
			err:   "model provider returned an exact token count without a model identifier",
		},
		{
			name:  "invalid model UTF-8",
			count: TokenCount{Model: string([]byte{0xff}), ModelClass: ModelClassDefault, InputTokens: 1},
			err:   "model provider returned an invalid token-count model: model: token usage model is not valid UTF-8",
		},
		{
			name: "oversized model",
			count: TokenCount{
				Model:       strings.Repeat("m", maxTokenUsageModelBytes+1),
				ModelClass:  ModelClassDefault,
				InputTokens: 1,
			},
			err: "model provider returned an invalid token-count model: model: token usage model exceeds 512 bytes",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			provider := &clientTestCountingProvider{count: test.count}
			client, err := NewClient(provider)
			require.NoError(t, err)

			count, err := client.CountTokens(t.Context(), &Request{ModelClass: ModelClassDefault})

			require.Equal(t, TokenCount{}, count)
			require.EqualError(t, err, test.err)
			require.Equal(t, 1, provider.countCalls)
		})
	}
}

func TestWrapClientRequiresMiddlewareToForwardTokenCounting(t *testing.T) {
	provider := &clientTestCountingProvider{count: TokenCount{
		Model:       "model",
		ModelClass:  ModelClassDefault,
		InputTokens: 1,
		Exact:       true,
	}}
	client, err := NewClient(provider)
	require.NoError(t, err)
	wrapped, err := WrapClient(client, func(provider Provider) Provider {
		return &clientTestProviderMiddleware{Provider: provider}
	})
	require.NoError(t, err)

	count, err := wrapped.CountTokens(t.Context(), &Request{ModelClass: ModelClassDefault})

	require.Equal(t, TokenCount{}, count)
	require.ErrorIs(t, err, ErrTokenCountingUnsupported)
	require.Zero(t, provider.countCalls)
}
