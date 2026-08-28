// Package model tests additive observer results and prepared-call lifecycle
// cleanup at the opaque client boundary.
package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/runtime/agent/internal/modelcall"
)

type (
	observerTestProvider struct {
		completeResponse *Response
		completeErr      error
		stream           Streamer
		streamErr        error
		request          *Request
		calls            int
	}

	observerTestPreparer struct {
		name       string
		events     *[]string
		prepareErr error
		call       ClientCallObserver
		mutate     func(*Request)
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
		finishObserved    error
		finishStarted     chan struct{}
		finishRelease     chan struct{}
		finishCalls       int
		abortCalls        int
	}

	observerTestStreamObserver struct {
		recvCalls  int
		closeCalls int
		closeErr   error
	}

	orderedFailingObserver struct {
		events   *[]string
		closeErr error
	}

	observerTestFinalizingCall struct {
		*observerTestCall
		outcome         modelcall.Outcome
		finalizeErr     error
		finalizeStarted chan struct{}
		finalizeRelease chan struct{}
		finalizeCalls   int
	}
)

func (p *observerTestProvider) Complete(_ context.Context, request *Request) (*Response, error) {
	p.calls++
	p.request = request
	return p.completeResponse, p.completeErr
}

func (p *observerTestProvider) Stream(_ context.Context, request *Request) (Streamer, error) {
	p.calls++
	p.request = request
	return p.stream, p.streamErr
}

func (p *observerTestPreparer) PrepareClientCall(
	ctx context.Context,
	request *Request,
) (context.Context, ClientCallObserver, error) {
	*p.events = append(*p.events, "prepare "+p.name)
	if p.mutate != nil {
		p.mutate(request)
	}
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

func (c *observerTestCall) Finish(err error) error {
	c.finishObserved = err
	c.finishCalls++
	if c.finishStarted != nil {
		close(c.finishStarted)
		<-c.finishRelease
	}
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
	return o.closeErr
}

func (*orderedFailingObserver) ObserveStreamRecv(StreamObservation) error {
	return nil
}

func (o *orderedFailingObserver) ObserveStreamClose(error) error {
	*o.events = append(*o.events, "later close")
	return o.closeErr
}

func (c *observerTestFinalizingCall) FinalizeModelCall(outcome modelcall.Outcome) error {
	c.outcome = outcome
	c.finalizeCalls++
	if c.finalizeStarted != nil {
		close(c.finalizeStarted)
		<-c.finalizeRelease
	}
	*c.events = append(*c.events, "finalize "+c.name)
	return c.finalizeErr
}

func TestClientObserverCannotChangeProviderOrValidationRequest(t *testing.T) {
	provider := &observerTestProvider{completeResponse: &Response{
		Content: []Message{{
			Role: ConversationRoleAssistant,
			Parts: []Part{ToolUsePart{
				ID:    "call-1",
				Name:  "lookup",
				Input: []byte(`{"id":"one"}`),
			}},
		}},
		StopReason: "tool_use",
	}}
	request := &Request{
		Tools:      []*ToolDefinition{advertisedTool("lookup")},
		ToolChoice: &ToolChoice{Mode: ToolChoiceModeTool, Name: "lookup"},
	}
	client, err := newValidatedClient(provider, nil, []ProviderCallObserver{
		&observerTestPreparer{
			events: &[]string{},
			call:   &observerTestCall{},
			mutate: func(observed *Request) {
				observed.Tools[0].Name = "changed"
				observed.ToolChoice.Name = "changed"
			},
		},
	})
	require.NoError(t, err)

	response, err := client.Complete(t.Context(), request)

	require.NoError(t, err)
	require.NotNil(t, response)
	require.Equal(t, "lookup", provider.request.Tools[0].Name)
	require.Equal(t, "lookup", provider.request.ToolChoice.Name)
	require.Equal(t, "lookup", request.Tools[0].Name)
	require.Equal(t, "lookup", request.ToolChoice.Name)
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

func TestClientCompleteClassifiesOnlyExactOutputValidation(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
	providerErr := errors.New("provider failed")
	tests := []struct {
		name               string
		returned           error
		wantProvider       bool
		wantValidationSlot bool
	}{
		{name: "exact", returned: validationErr, wantValidationSlot: true},
		{
			name:               "wrapped",
			returned:           fmt.Errorf("translated: %w", validationErr),
			wantValidationSlot: true,
		},
		{
			name:               "duplicate joined",
			returned:           errors.Join(validationErr, validationErr),
			wantValidationSlot: true,
		},
		{
			name: "nested duplicate joined",
			returned: fmt.Errorf(
				"translated: %w",
				errors.Join(validationErr, errors.Join(validationErr, validationErr)),
			),
			wantValidationSlot: true,
		},
		{
			name:         "unrelated joined",
			returned:     errors.Join(validationErr, providerErr),
			wantProvider: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			events := []string{}
			call := &observerTestFinalizingCall{observerTestCall: &observerTestCall{
				name:   "runtime",
				events: &events,
			}}
			client, clientErr := newValidatedClient(
				&observerTestProvider{completeErr: test.returned},
				nil,
				[]ProviderCallObserver{
					&observerTestPreparer{name: "runtime", events: &events, call: call},
				},
			)
			require.NoError(t, clientErr)

			response, completeErr := client.Complete(t.Context(), &Request{})

			require.Nil(t, response)
			require.ErrorIs(t, completeErr, validationErr)
			require.Equal(t, test.wantProvider, call.outcome.ProviderCall.Err != nil)
			require.Equal(t, test.wantValidationSlot, call.outcome.Validations[0].Err != nil)
			if test.wantProvider {
				require.ErrorIs(t, completeErr, providerErr)
				require.ErrorIs(t, call.outcome.ProviderCall.Err, providerErr)
			}
		})
	}
}

func TestClientFinalizeRetainsIndependentLifecycleFailures(t *testing.T) {
	closeObserverErr := errors.New("close observer failed")
	finisherErr := errors.New("finisher failed")
	finalizerErr := errors.New("finalizer failed")
	providerCloseErr := errors.New("provider close failed")
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
	raw := &validatedStreamFixture{recvErr: validationErr, closeErr: providerCloseErr}
	streamObserver := &observerTestStreamObserver{closeErr: closeObserverErr}
	events := []string{}
	call := &observerTestFinalizingCall{
		observerTestCall: &observerTestCall{
			name:           "runtime",
			events:         &events,
			streamObserver: streamObserver,
			finishErr:      finisherErr,
		},
		finalizeErr: finalizerErr,
	}
	client, err := newValidatedClient(
		&observerTestProvider{stream: raw},
		nil,
		[]ProviderCallObserver{
			&observerTestPreparer{name: "runtime", events: &events, call: call},
		},
	)
	require.NoError(t, err)
	stream, err := client.Stream(t.Context(), &Request{})
	require.NoError(t, err)
	_, primaryErr := stream.Recv()
	require.Same(t, validationErr, primaryErr)

	operationErr := stream.Finalize(primaryErr)

	require.ErrorIs(t, operationErr, validationErr)
	require.ErrorIs(t, operationErr, closeObserverErr)
	require.ErrorIs(t, operationErr, finisherErr)
	require.ErrorIs(t, operationErr, finalizerErr)
	require.ErrorIs(t, operationErr, providerCloseErr)
	require.Equal(t, 1, streamObserver.closeCalls)
	require.Equal(t, 1, call.finishCalls)
	require.Equal(t, 1, call.finalizeCalls)
	require.Equal(t, 1, raw.closeCalls)
}

func TestClientFinalizeRetainsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
	providerCloseErr := errors.New("provider close failed")
	raw := &validatedStreamFixture{recvErr: validationErr, closeErr: providerCloseErr}
	client, err := NewClient(&observerTestProvider{stream: raw})
	require.NoError(t, err)
	stream, err := client.Stream(ctx, &Request{})
	require.NoError(t, err)
	_, primaryErr := stream.Recv()
	require.Same(t, validationErr, primaryErr)
	cancel()

	operationErr := stream.Finalize(primaryErr)

	require.ErrorIs(t, operationErr, validationErr)
	require.ErrorIs(t, operationErr, context.Canceled)
	require.ErrorIs(t, operationErr, providerCloseErr)
	require.Equal(t, 1, raw.closeCalls)
}

func TestClientFinalizeResamplesCancellationDuringLifecycle(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*observerTestFinalizingCall) (started, release chan struct{})
	}{
		{
			name: "finisher",
			configure: func(call *observerTestFinalizingCall) (chan struct{}, chan struct{}) {
				call.finishStarted = make(chan struct{})
				call.finishRelease = make(chan struct{})
				return call.finishStarted, call.finishRelease
			},
		},
		{
			name: "finalizer",
			configure: func(call *observerTestFinalizingCall) (chan struct{}, chan struct{}) {
				call.finalizeStarted = make(chan struct{})
				call.finalizeRelease = make(chan struct{})
				return call.finalizeStarted, call.finalizeRelease
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			contract, err := NewRequestContract(&Request{})
			require.NoError(t, err)
			validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
			providerCloseErr := errors.New("provider close failed")
			raw := &validatedStreamFixture{recvErr: validationErr, closeErr: providerCloseErr}
			events := []string{}
			call := &observerTestFinalizingCall{observerTestCall: &observerTestCall{
				name:   "runtime",
				events: &events,
			}}
			started, release := test.configure(call)
			client, err := newValidatedClient(
				&observerTestProvider{stream: raw},
				nil,
				[]ProviderCallObserver{
					&observerTestPreparer{name: "runtime", events: &events, call: call},
				},
			)
			require.NoError(t, err)
			stream, err := client.Stream(ctx, &Request{})
			require.NoError(t, err)
			_, primaryErr := stream.Recv()
			require.Same(t, validationErr, primaryErr)
			result := make(chan error, 1)
			go func() {
				result <- stream.Finalize(primaryErr)
			}()
			<-started

			cancel()
			close(release)
			operationErr := <-result

			require.ErrorIs(t, operationErr, validationErr)
			require.ErrorIs(t, operationErr, context.Canceled)
			require.ErrorIs(t, operationErr, providerCloseErr)
			require.Equal(t, 1, raw.closeCalls)
			require.Equal(t, 1, call.finishCalls)
			require.Equal(t, 1, call.finalizeCalls)
		})
	}
}

func TestClientFinishesCallsAfterLaterStreamObserver(t *testing.T) {
	laterErr := errors.New("later observer failed")
	var events []string
	provider := &observerTestProvider{stream: &validatedStreamFixture{
		chunks: []Chunk{StopChunk{Reason: "end_turn"}},
		response: &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{nil},
			}},
			StopReason: "end_turn",
		},
	}}
	call := &observerTestCall{name: "client call", events: &events}
	client, err := newValidatedClient(provider, nil, []ProviderCallObserver{
		&observerTestPreparer{name: "client call", events: &events, call: call},
	})
	require.NoError(t, err)
	stream, err := client.Stream(t.Context(), &Request{})
	require.NoError(t, err)
	stream, err = stream.Observe(&orderedFailingObserver{
		events:   &events,
		closeErr: laterErr,
	})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	var validationErr *OutputValidationError
	require.ErrorAs(t, err, &validationErr)

	err = stream.Close()

	require.ErrorIs(t, err, laterErr)
	require.ErrorIs(t, call.finishObserved, laterErr)
	require.ErrorAs(t, call.finishObserved, &validationErr)
	require.Equal(t, []string{"prepare client call", "later close", "finish client call"}, events)
	require.Equal(t, 1, call.finishCalls)
}

func TestClientStreamFinishersReceiveFrozenInputAndFinalizerRunsLast(t *testing.T) {
	closeErr := errors.New("close observer failed")
	beforeErr := errors.New("before finisher failed")
	afterErr := errors.New("after finisher failed")
	var events []string
	before := &observerTestCall{name: "before", events: &events, finishErr: beforeErr}
	runtimeCall := &observerTestFinalizingCall{observerTestCall: &observerTestCall{
		name: "runtime", events: &events,
	}}
	after := &observerTestCall{name: "after", events: &events, finishErr: afterErr}
	provider := &observerTestProvider{stream: &validatedStreamFixture{
		chunks:   []Chunk{StopChunk{Reason: "end_turn"}},
		response: canonicalTextResponse(),
	}}
	client, err := newValidatedClient(provider, nil, []ProviderCallObserver{
		&observerTestPreparer{name: "before", events: &events, call: before},
		&observerTestPreparer{name: "runtime", events: &events, call: runtimeCall},
		&observerTestPreparer{name: "after", events: &events, call: after},
	})
	require.NoError(t, err)
	stream, err := client.Stream(t.Context(), &Request{})
	require.NoError(t, err)
	stream, err = stream.Observe(&orderedFailingObserver{events: &events, closeErr: closeErr})
	require.NoError(t, err)
	for {
		_, err = stream.Recv()
		if modelcall.Exact(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	err = stream.Close()
	repeatedErr := stream.Close()

	require.ErrorIs(t, err, closeErr)
	require.Equal(t, err, repeatedErr)
	require.ErrorIs(t, err, beforeErr)
	require.ErrorIs(t, err, afterErr)
	require.ErrorIs(t, before.finishObserved, closeErr)
	require.True(t, modelcall.Exact(before.finishObserved, runtimeCall.finishObserved))
	require.True(t, modelcall.Exact(runtimeCall.finishObserved, after.finishObserved))
	require.NotErrorIs(t, after.finishObserved, beforeErr)
	require.Equal(t, []string{
		"prepare after",
		"prepare runtime",
		"prepare before",
		"later close",
		"finish before",
		"finish runtime",
		"finish after",
		"finalize runtime",
	}, events)
	require.Len(t, runtimeCall.outcome.Finishers, 3)
	require.ErrorIs(t, runtimeCall.outcome.Finishers[0].Err, beforeErr)
	require.NoError(t, runtimeCall.outcome.Finishers[1].Err)
	require.ErrorIs(t, runtimeCall.outcome.Finishers[2].Err, afterErr)
	require.Equal(t, 1, runtimeCall.finalizeCalls)
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
	require.JSONEq(t, `{"id":"original"}`, string(response.Content[0].Parts[1].(ToolUsePart).Input))
	require.Equal(t, "original", response.Content[0].Meta["nested"].(map[string]any)["value"])
	require.Equal(t, "tool_use", second.completeResponse.StopReason)
	require.Equal(t, TextPart{Text: "canonical"}, second.completeResponse.Content[0].Parts[0])
	require.JSONEq(t, `{"id":"original"}`, string(second.completeResponse.Content[0].Parts[1].(ToolUsePart).Input))
	require.Equal(t, "original", second.completeResponse.Content[0].Meta["nested"].(map[string]any)["value"])
}
