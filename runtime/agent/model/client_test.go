// Package model tests the opaque client boundary around raw providers.
package model

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type clientTestProvider struct {
	calls int
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
