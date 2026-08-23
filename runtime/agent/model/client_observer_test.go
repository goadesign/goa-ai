// Package model tests additive observer results and prepared-call lifecycle
// cleanup at the opaque client boundary.
package model

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type (
	observerTestProvider struct {
		completeResponse *Response
		completeErr      error
		stream           Streamer
		streamErr        error
		calls            int
	}

	observerTestPreparer struct {
		name       string
		events     *[]string
		prepareErr error
		call       *observerTestCall
	}

	observerTestCall struct {
		name              string
		events            *[]string
		completeErr       error
		streamSetupErr    error
		streamObserver    StreamObserver
		finishErr         error
		abortErr          error
		mutateResponse    func(*Response)
		completeResponse  *Response
		completeObserved  error
		streamObservedErr error
		finishCalls       int
		abortCalls        int
	}

	observerTestStreamObserver struct {
		recvCalls  int
		closeCalls int
	}
)

func (p *observerTestProvider) Complete(context.Context, *Request) (*Response, error) {
	p.calls++
	return p.completeResponse, p.completeErr
}

func (p *observerTestProvider) Stream(context.Context, *Request) (Streamer, error) {
	p.calls++
	return p.stream, p.streamErr
}

func (p *observerTestPreparer) PrepareClientCall(
	ctx context.Context,
	_ *Request,
) (context.Context, ClientCallObserver, error) {
	*p.events = append(*p.events, "prepare "+p.name)
	if p.prepareErr != nil {
		return ctx, nil, p.prepareErr
	}
	return ctx, p.call, nil
}

func (c *observerTestCall) ObserveClientComplete(response *Response, err error) error {
	c.completeResponse = response
	c.completeObserved = err
	if c.mutateResponse != nil {
		c.mutateResponse(response)
	}
	return c.completeErr
}

func (c *observerTestCall) ObserveClientStream(err error) (StreamObserver, error) {
	c.streamObservedErr = err
	return c.streamObserver, c.streamSetupErr
}

func (c *observerTestCall) Finish(error) error {
	c.finishCalls++
	if c.events != nil {
		*c.events = append(*c.events, "finish "+c.name)
	}
	return c.finishErr
}

func (c *observerTestCall) Abort(error) error {
	c.abortCalls++
	if c.events != nil {
		*c.events = append(*c.events, "abort "+c.name)
	}
	return c.abortErr
}

func (o *observerTestStreamObserver) ObserveStreamRecv(StreamObservation) error {
	o.recvCalls++
	return nil
}

func (o *observerTestStreamObserver) ObserveStreamClose(error) error {
	o.closeCalls++
	return nil
}

func TestClientCompleteJoinsProviderObserverAndFinishErrors(t *testing.T) {
	providerErr := errors.New("provider failed")
	observerErr := errors.New("observer failed")
	finishErr := errors.New("finish failed")
	providerResponse := canonicalTextResponse()
	provider := &observerTestProvider{
		completeResponse: providerResponse,
		completeErr:      providerErr,
	}
	call := &observerTestCall{completeErr: observerErr, finishErr: finishErr}
	client, err := newValidatedClient(provider, nil, []ProviderCallObserver{
		&observerTestPreparer{events: &[]string{}, call: call},
	})
	require.NoError(t, err)

	response, err := client.Complete(t.Context(), &Request{})

	require.Nil(t, response)
	require.ErrorIs(t, err, providerErr)
	require.ErrorIs(t, err, observerErr)
	require.ErrorIs(t, err, finishErr)
	require.Nil(t, call.completeResponse)
	require.ErrorIs(t, call.completeObserved, providerErr)
	require.Equal(t, 1, call.finishCalls)
	require.Zero(t, call.abortCalls)
}

func TestClientPreparationFailureAbortsPreparedObserversInReverseOrder(t *testing.T) {
	setupErr := errors.New("inner setup failed")
	middleAbortErr := errors.New("middle abort failed")
	outerAbortErr := errors.New("outer abort failed")
	var events []string
	outerCall := &observerTestCall{name: "outer", events: &events, abortErr: outerAbortErr}
	middleCall := &observerTestCall{name: "middle", events: &events, abortErr: middleAbortErr}
	provider := &observerTestProvider{}
	client, err := newValidatedClient(provider, nil, []ProviderCallObserver{
		&observerTestPreparer{name: "inner", events: &events, prepareErr: setupErr},
		&observerTestPreparer{name: "middle", events: &events, call: middleCall},
		&observerTestPreparer{name: "outer", events: &events, call: outerCall},
	})
	require.NoError(t, err)

	response, err := client.Complete(t.Context(), &Request{})

	require.Nil(t, response)
	require.ErrorIs(t, err, setupErr)
	require.ErrorIs(t, err, middleAbortErr)
	require.ErrorIs(t, err, outerAbortErr)
	require.Equal(t, []string{
		"prepare outer",
		"prepare middle",
		"prepare inner",
		"abort middle",
		"abort outer",
	}, events)
	require.Zero(t, provider.calls)
	require.Equal(t, 1, middleCall.abortCalls)
	require.Equal(t, 1, outerCall.abortCalls)
	require.Zero(t, middleCall.finishCalls)
	require.Zero(t, outerCall.finishCalls)
}

func TestClientAttachesObserverToExactValidatedStreamAndFinishesOnClose(t *testing.T) {
	response := canonicalTextResponse()
	provider := &observerTestProvider{stream: &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: response.Content[0]},
			StopChunk{Reason: response.StopReason},
		},
		response: response,
	}}
	streamObserver := &observerTestStreamObserver{}
	call := &observerTestCall{streamObserver: streamObserver}
	client, err := newValidatedClient(provider, nil, []ProviderCallObserver{
		&observerTestPreparer{events: &[]string{}, call: call},
	})
	require.NoError(t, err)

	stream, err := client.Stream(t.Context(), &Request{})
	require.NoError(t, err)
	for {
		_, err = stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}
	require.NoError(t, stream.Close())
	require.NoError(t, stream.Close())

	require.Equal(t, 3, streamObserver.recvCalls)
	require.Equal(t, 1, streamObserver.closeCalls)
	require.Equal(t, 1, call.finishCalls)
	require.Zero(t, call.abortCalls)
}

func TestClientStreamObserverCannotCreateSuccessFromProviderError(t *testing.T) {
	providerErr := errors.New("provider stream failed")
	provider := &observerTestProvider{streamErr: providerErr}
	streamObserver := &observerTestStreamObserver{}
	call := &observerTestCall{streamObserver: streamObserver}
	client, err := newValidatedClient(provider, nil, []ProviderCallObserver{
		&observerTestPreparer{events: &[]string{}, call: call},
	})
	require.NoError(t, err)

	stream, err := client.Stream(t.Context(), &Request{})

	require.Nil(t, stream)
	require.ErrorIs(t, err, providerErr)
	require.ErrorIs(t, call.streamObservedErr, providerErr)
	require.Zero(t, streamObserver.recvCalls)
	require.Zero(t, streamObserver.closeCalls)
	require.Equal(t, 1, call.finishCalls)
	require.Zero(t, call.abortCalls)
}

func TestClientCompleteIsolatesEachObserverAndCallerResponse(t *testing.T) {
	providerResponse := &Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{
				TextPart{Text: "canonical"},
				ToolUsePart{ID: "call-1", Name: "lookup", Input: []byte(`{"id":"original"}`)},
			},
			Meta: map[string]any{
				"nested": map[string]any{"value": "original"},
			},
		}},
		StopReason: "tool_use",
	}
	first := &observerTestCall{mutateResponse: func(response *Response) {
		response.StopReason = "mutated"
		response.Content[0].Parts[0] = TextPart{Text: "mutated"}
		tool := response.Content[0].Parts[1].(ToolUsePart)
		tool.Input[7] = 'X'
		response.Content[0].Parts[1] = tool
		response.Content[0].Meta["nested"].(map[string]any)["value"] = "mutated"
	}}
	second := &observerTestCall{}
	client, err := newValidatedClient(
		&observerTestProvider{completeResponse: providerResponse},
		nil,
		[]ProviderCallObserver{
			&observerTestPreparer{events: &[]string{}, call: first},
			&observerTestPreparer{events: &[]string{}, call: second},
		},
	)
	require.NoError(t, err)

	response, err := client.Complete(t.Context(), &Request{
		Tools: []*ToolDefinition{advertisedTool("lookup")},
	})

	require.NoError(t, err)
	require.NotSame(t, response, first.completeResponse)
	require.NotSame(t, response, second.completeResponse)
	require.NotSame(t, first.completeResponse, second.completeResponse)
	require.Equal(t, "tool_use", response.StopReason)
	require.Equal(t, TextPart{Text: "canonical"}, response.Content[0].Parts[0])
	require.Equal(t, `{"id":"original"}`, string(response.Content[0].Parts[1].(ToolUsePart).Input))
	require.Equal(t, "original", response.Content[0].Meta["nested"].(map[string]any)["value"])
	require.Equal(t, "tool_use", second.completeResponse.StopReason)
	require.Equal(t, TextPart{Text: "canonical"}, second.completeResponse.Content[0].Parts[0])
	require.Equal(t, `{"id":"original"}`, string(second.completeResponse.Content[0].Parts[1].(ToolUsePart).Input))
	require.Equal(t, "original", second.completeResponse.Content[0].Meta["nested"].(map[string]any)["value"])
}
