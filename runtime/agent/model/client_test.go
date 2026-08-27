// Package model tests the opaque client boundary around raw providers.
package model

import (
	"context"
	"io"
	"strings"
	"sync"
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

type clientTestContractProvider struct {
	completeContract *RequestContract
	completePrepared *RequestContract
	streamContract   *RequestContract
	streamPrepared   *RequestContract
	countContract    *RequestContract
	countPrepared    *RequestContract
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

func (p *clientTestContractProvider) Complete(_ context.Context, request *Request) (*Response, error) {
	contract, err := NewRequestContract(request)
	if err != nil {
		return nil, err
	}
	p.completeContract = contract
	if request.preparedContract != nil {
		p.completePrepared = request.preparedContract.contract
	}
	return structuredOutputResponse(`{}`), nil
}

func (p *clientTestContractProvider) Stream(_ context.Context, request *Request) (Streamer, error) {
	contract, err := NewRequestContract(request)
	if err != nil {
		return nil, err
	}
	p.streamContract = contract
	if request.preparedContract != nil {
		p.streamPrepared = request.preparedContract.contract
	}
	return &validatedStreamFixture{
		chunks: []Chunk{
			CompletionChunk{Completion: Completion{Name: "answer", Payload: []byte(`{}`)}},
			StopChunk{Reason: "stop"},
		},
		response: structuredOutputResponse(`{}`),
	}, nil
}

func (p *clientTestContractProvider) CountTokens(_ context.Context, request *Request) (TokenCount, error) {
	contract, err := NewRequestContract(request)
	if err != nil {
		return TokenCount{}, err
	}
	p.countContract = contract
	if request.preparedContract != nil {
		p.countPrepared = request.preparedContract.contract
	}
	return TokenCount{
		Model:       "test-model",
		ModelClass:  request.ModelClass,
		InputTokens: 7,
		Exact:       true,
	}, nil
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

func TestClientRejectsStructuredOutputSchemaBeforeProviderCall(t *testing.T) {
	schemas := []string{
		`{"type":"not-a-json-type"}`,
		`{"$ref":"https://example.com/external.json"}`,
	}
	for _, schema := range schemas {
		provider := &clientTestCountingProvider{}
		client, err := NewClient(provider)
		require.NoError(t, err)
		request := structuredOutputRequest(schema)

		response, completeErr := client.Complete(t.Context(), request)
		stream, streamErr := client.Stream(t.Context(), request)
		count, countErr := client.CountTokens(t.Context(), request)

		require.Nil(t, response)
		require.Nil(t, stream)
		require.Equal(t, TokenCount{}, count)
		require.ErrorContains(t, completeErr, "structured output schema")
		require.ErrorContains(t, streamErr, "structured output schema")
		require.ErrorContains(t, countErr, "structured output schema")
		require.Zero(t, provider.calls)
		require.Zero(t, provider.countCalls)
	}
}

func TestClientReturnsOutputValidationErrorForSchemaInvalidResponse(t *testing.T) {
	provider := &clientTestProvider{}
	client, err := NewClient(provider)
	require.NoError(t, err)

	response, err := client.Complete(
		t.Context(),
		structuredOutputRequest(`{"type":"object"}`),
	)

	require.Nil(t, response)
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)
	require.Equal(t, 1, provider.calls)
}

func TestClientPassesPreparedContractThroughEveryProviderOperation(t *testing.T) {
	provider := &clientTestContractProvider{}
	client, err := NewClient(provider)
	require.NoError(t, err)
	request := structuredOutputRequest(`{"type":"object"}`)
	request.ModelClass = ModelClassDefault

	response, err := client.Complete(t.Context(), request)

	require.NoError(t, err)
	require.NotNil(t, response)
	require.Same(t, provider.completePrepared, provider.completeContract)
	require.Nil(t, request.preparedContract)

	stream, err := client.Stream(t.Context(), request)
	require.NoError(t, err)
	for {
		_, recvErr := stream.Recv()
		if recvErr != nil {
			require.ErrorIs(t, recvErr, io.EOF)
			break
		}
	}
	require.Same(t, provider.streamPrepared, provider.streamContract)
	require.Nil(t, request.preparedContract)

	count, err := client.CountTokens(t.Context(), request)
	require.NoError(t, err)
	require.Equal(t, 7, count.InputTokens)
	require.Same(t, provider.countPrepared, provider.countContract)
	require.Nil(t, request.preparedContract)
}

func TestRequestContractHandoffDoesNotSurviveCopies(t *testing.T) {
	request := structuredOutputRequest(`{"type":"object"}`)
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	prepared := preparedRequest(request, contract)

	cloned, err := cloneRequest(prepared)
	require.NoError(t, err)
	require.Nil(t, cloned.preparedContract)
	clonedContract, err := NewRequestContract(cloned)
	require.NoError(t, err)
	require.NotSame(t, contract, clonedContract)

	copied := *prepared
	copiedContract, err := NewRequestContract(&copied)
	require.NoError(t, err)
	require.NotSame(t, contract, copiedContract)
}

func TestPreparedRequestContractIsRaceSafe(t *testing.T) {
	request := structuredOutputRequest(`{"type":"object"}`)
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	prepared := preparedRequest(request, contract)

	type contractResult struct {
		contract *RequestContract
		err      error
	}
	results := make(chan contractResult, 32)
	var wait sync.WaitGroup
	for range 32 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			actual, contractErr := NewRequestContract(prepared)
			results <- contractResult{contract: actual, err: contractErr}
		}()
	}
	wait.Wait()
	close(results)
	for result := range results {
		require.NoError(t, result.err)
		require.Same(t, contract, result.contract)
	}
}

func TestDirectProviderCompilesItsOwnRequestContract(t *testing.T) {
	provider := &clientTestContractProvider{}
	request := structuredOutputRequest(`{"type":"object"}`)
	response, err := provider.Complete(t.Context(), request)

	require.NoError(t, err)
	require.NotNil(t, response)
	require.NotNil(t, provider.completeContract)
	require.Nil(t, provider.completePrepared)
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
