// These tests verify that the model stream boundary owns mutable provider data
// and applies its contract exactly once before exposing a clean EOF.
package model

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"
)

type validatedStreamFixture struct {
	chunks   []Chunk
	response *Response
	next     int
	closed   bool
	closeErr error
}

type recordingStreamObserver struct {
	chunks       []Chunk
	observations []StreamObservation
	closeErr     error
}

type mutatingStreamObserver struct{}

type reentrantStreamObserver struct {
	stream          *ValidatedStream
	closeOnRecv     bool
	recvResponse    *Response
	recvCloseErr    error
	closeResponse   *Response
	closeReentryErr error
	closeCalls      int
}

type typedNilStreamFixture struct{}

type cancellationBlockedStreamFixture struct {
	ctx            context.Context
	recvStarted    chan struct{}
	cleanupStarted chan struct{}
	cleanupRelease chan struct{}
	closeCalled    chan struct{}
	closeCalls     int
	closeErr       error
}

type siblingStreamFixture struct {
	next       int
	secondRecv chan struct{}
}

type blockingRejectingStreamObserver struct {
	firstEntered chan struct{}
	releaseFirst chan struct{}
	rejectErr    error
	calls        atomic.Int32
}

type reentrantCloseErrorObserver struct {
	stream          *ValidatedStream
	closeOnRecv     bool
	closeErr        error
	recvCloseErr    error
	closeReentryErr error
	closeCalls      int
}

func (*typedNilStreamFixture) Recv() (Chunk, error) {
	panic("typed-nil stream Recv called")
}

func (*typedNilStreamFixture) Response() *Response {
	panic("typed-nil stream Response called")
}

func (*typedNilStreamFixture) Close() error {
	panic("typed-nil stream Close called")
}

func (s *cancellationBlockedStreamFixture) Recv() (Chunk, error) {
	close(s.recvStarted)
	<-s.ctx.Done()
	close(s.cleanupStarted)
	<-s.cleanupRelease
	return nil, s.ctx.Err()
}

func (*cancellationBlockedStreamFixture) Response() *Response {
	return nil
}

func (s *cancellationBlockedStreamFixture) Close() error {
	s.closeCalls++
	close(s.closeCalled)
	return s.closeErr
}

func TestStreamBoundariesRejectTypedNilWithoutMethodCalls(t *testing.T) {
	contract, err := NewRequestContract(&Request{})
	require.NoError(t, err)
	var raw *typedNilStreamFixture
	var validated *ValidatedStream

	for name, stream := range map[string]Streamer{
		"provider pointer": raw,
		"validated stream": validated,
	} {
		t.Run(name, func(t *testing.T) {
			got, err := contract.ValidateStream(stream)
			require.Nil(t, got)
			require.ErrorContains(t, err, "typed nil")
		})
	}
}

func TestZeroValidatedStreamReturnsContractErrors(t *testing.T) {
	stream := &ValidatedStream{}

	chunk, err := stream.Recv()
	require.Nil(t, chunk)
	require.ErrorContains(t, err, "not an intact validated stream")
	require.Nil(t, stream.Response())
	require.ErrorContains(t, stream.Close(), "not an intact validated stream")
	observed, err := stream.Observe(&recordingStreamObserver{})
	require.Nil(t, observed)
	require.ErrorContains(t, err, "not an intact validated stream")
}

func TestRequestContractRejectsInvalidRequestIdentity(t *testing.T) {
	contract, err := NewRequestContract(&Request{
		Model: string(make([]byte, maxTokenUsageModelBytes+1)),
	})

	require.Nil(t, contract)
	require.ErrorContains(t, err, "model request identity")
	require.ErrorContains(t, err, "token usage model exceeds")
}

func TestValidatedStreamRetainsImmutableToolCallEvidence(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallChunk{ToolCall: ToolCall{
				Name:    "search",
				Payload: []byte(`{"query":"original"}`),
				ID:      "call-1",
			}},
			StopChunk{Reason: "tool_use"},
		},
		response: &Response{
			Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID:    "call-1",
					Name:  "search",
					Input: []byte(`{"query":"original"}`),
				}},
			}},
			StopReason: "tool_use",
		},
	}
	stream := mustValidateStream(t, raw, requestWithTool("search"))

	chunk, err := stream.Recv()
	require.NoError(t, err)
	toolCall := chunk.(ToolCallChunk)
	providerChunk := raw.chunks[0].(ToolCallChunk)
	providerChunk.ToolCall.Payload[10] = 'Y'
	require.JSONEq(t, `{"query":"original"}`, string(toolCall.ToolCall.Payload))
	toolCall.ToolCall.Payload[10] = 'X'

	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
	providerPart := raw.response.Content[0].Parts[0].(ToolUsePart)
	providerPart.Input[10] = 'Z'
	response := stream.Response()
	require.NotNil(t, response)
	require.JSONEq(t, `{"query":"original"}`, string(response.Content[0].Parts[0].(ToolUsePart).Input))
}

func TestValidatedStreamSharesBudgetAcrossChunks(t *testing.T) {
	text := strings.Repeat("x", maxDynamicValueBytes/2)
	raw := &validatedStreamFixture{chunks: []Chunk{
		TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: text}},
		}},
		TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: text}},
		}},
	}}
	stream := mustValidateStream(t, raw, &Request{})

	_, err := stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()

	require.ErrorContains(t, err, "exceeds maximum byte size")
}

func TestValidatedStreamBoundsToolDeltaAccumulator(t *testing.T) {
	delta := strings.Repeat("x", maxDynamicValueBytes/2)
	raw := &validatedStreamFixture{chunks: []Chunk{
		ToolCallDeltaChunk{Delta: ToolCallDelta{Name: "search", ID: "call-1", Delta: delta}},
		ToolCallDeltaChunk{Delta: ToolCallDelta{Name: "search", ID: "call-1", Delta: delta}},
	}}
	stream := mustValidateStream(t, raw, requestWithTool("search"))
	inner := stream.core.inner.(*validatedStreamer)

	_, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, len(delta), inner.validator.toolDeltaPayloads["call-1"].Len())
	_, err = stream.Recv()

	require.ErrorContains(t, err, "exceeds maximum byte size")
	require.Equal(t, len(delta), inner.validator.toolDeltaPayloads["call-1"].Len())
}

func TestValidatedStreamDoesNotDoubleChargeFinalizedToolDeltas(t *testing.T) {
	payload := []byte(`{"value":"` + strings.Repeat("x", maxDynamicValueBytes/2) + `"}`)
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallDeltaChunk{Delta: ToolCallDelta{
				Name:  "search",
				ID:    "call-1",
				Delta: string(payload),
			}},
			ToolCallChunk{ToolCall: ToolCall{
				Name:    "search",
				ID:      "call-1",
				Payload: payload,
			}},
			StopChunk{Reason: "tool_use"},
		},
		response: &Response{
			Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID:    "call-1",
					Name:  "search",
					Input: payload,
				}},
			}},
			StopReason: "tool_use",
		},
	}
	stream := mustValidateStream(t, raw, requestWithTool("search"))

	for range 3 {
		_, err := stream.Recv()
		require.NoError(t, err)
	}
	_, err := stream.Recv()

	require.ErrorIs(t, err, io.EOF)
}

func TestValidatedStreamSharesVisitBudgetAcrossChunks(t *testing.T) {
	chunks := make([]Chunk, maxDynamicValueVisits+1)
	for index := range chunks {
		chunks[index] = UsageChunk{}
	}
	stream := mustValidateStream(t, &validatedStreamFixture{chunks: chunks}, &Request{})

	for range maxDynamicValueVisits {
		_, err := stream.Recv()
		require.NoError(t, err)
	}
	_, err := stream.Recv()

	require.ErrorContains(t, err, "exceeds maximum visited values")
}

func TestValidatedStreamDoesNotDoubleChargeTerminalResponse(t *testing.T) {
	text := strings.Repeat("x", maxDynamicValueBytes/2)
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: text}},
			}},
			StopChunk{Reason: "stop"},
		},
		response: &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: text}},
			}},
			StopReason: "stop",
		},
	}
	stream := mustValidateStream(t, raw, &Request{})

	_, err := stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()

	require.ErrorIs(t, err, io.EOF)
}

func TestValidatedStreamChargesMismatchedTerminalDataBeforeCopy(t *testing.T) {
	chunkText := strings.Repeat("x", maxDynamicValueBytes/2)
	responseText := strings.Repeat("y", maxDynamicValueBytes/2)
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: chunkText}},
			}},
			StopChunk{Reason: "stop"},
		},
		response: &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: responseText}},
			}},
			StopReason: "stop",
		},
	}
	stream := mustValidateStream(t, raw, &Request{})
	_, err := stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	_, err = stream.Recv()

	require.ErrorContains(t, err, "exceeds maximum byte size")
	require.Nil(t, stream.Response())
}

func TestValidatedStreamRejectsPrematureCompletionStop(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{StopChunk{Reason: "stop"}},
	}
	stream := mustValidateStream(t, raw, &Request{
		StructuredOutput: &StructuredOutput{Name: "answer"},
	})

	_, firstErr := stream.Recv()
	_, secondErr := stream.Recv()

	var validationErr *OutputValidationError
	require.ErrorAs(t, firstErr, &validationErr)
	require.ErrorContains(t, firstErr, "structured output stream stopped before a completion")
	require.Same(t, firstErr, secondErr)
	require.True(t, IsStreamValidationError(firstErr))
}

func TestValidatedStreamObserveUsesExactSource(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			TextChunk{Message: Message{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "validated"}},
			}},
			StopChunk{Reason: "stop"},
		},
		response: &Response{
			Content: []Message{{
				Role:  ConversationRoleAssistant,
				Parts: []Part{TextPart{Text: "validated"}},
			}},
			StopReason: "stop",
		},
	}
	validated := mustValidateStream(t, raw, &Request{})
	observer := &recordingStreamObserver{}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)

	chunk, err := observed.Recv()

	require.NoError(t, err)
	require.Equal(t, "validated", chunk.(TextChunk).Message.Parts[0].(TextPart).Text)
	require.Equal(t, []Chunk{chunk}, observer.chunks)
}

func TestValidatedStreamObserveDoesNotExposeMutableOutput(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{
			ToolCallChunk{ToolCall: ToolCall{
				Name:    "search",
				Payload: []byte(`{"query":"original"}`),
				ID:      "call-1",
			}},
			StopChunk{Reason: "tool_use"},
		},
		response: &Response{
			Content: []Message{{
				Role: ConversationRoleAssistant,
				Parts: []Part{ToolUsePart{
					ID:    "call-1",
					Name:  "search",
					Input: []byte(`{"query":"original"}`),
				}},
			}},
			StopReason: "tool_use",
		},
	}
	validated := mustValidateStream(t, raw, requestWithTool("search"))
	observed, err := validated.Observe(&mutatingStreamObserver{})
	require.NoError(t, err)

	chunk, err := observed.Recv()
	require.NoError(t, err)
	require.JSONEq(t, `{"query":"original"}`, string(chunk.(ToolCallChunk).ToolCall.Payload))
	_, err = observed.Recv()
	require.NoError(t, err)
	_, err = observed.Recv()
	require.ErrorIs(t, err, io.EOF)
	require.JSONEq(
		t,
		`{"query":"original"}`,
		string(observed.Response().Content[0].Parts[0].(ToolUsePart).Input),
	)
}

func TestValidatedStreamObserveBoundsRejectedUsageEvidence(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{UsageChunk{Usage: TokenUsage{
			Model:            string(make([]byte, maxTokenUsageModelBytes+1)),
			InputTokens:      1,
			OutputTokens:     2,
			TotalTokens:      3,
			CacheReadTokens:  4,
			CacheWriteTokens: 5,
		}}},
	}
	validated := mustValidateStream(t, raw, &Request{})
	observer := &recordingStreamObserver{}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)

	chunk, err := observed.Recv()

	require.Nil(t, chunk)
	require.ErrorContains(t, err, "token usage model exceeds")
	require.Len(t, observer.observations, 1)
	require.Nil(t, observer.observations[0].Chunk)
	require.Equal(t, &TokenUsage{
		InputTokens:      1,
		OutputTokens:     2,
		TotalTokens:      3,
		CacheReadTokens:  4,
		CacheWriteTokens: 5,
	}, observer.observations[0].RejectedUsage)
}

func TestObservedStreamCloseJoinsProviderAndObserverFailures(t *testing.T) {
	providerErr := errors.New("provider close failed")
	observerErr := errors.New("observer close failed")
	raw := &validatedStreamFixture{closeErr: providerErr}
	validated := mustValidateStream(t, raw, &Request{})
	observed, err := validated.Observe(
		&recordingStreamObserver{closeErr: observerErr},
	)
	require.NoError(t, err)

	err = observed.Close()

	require.ErrorIs(t, err, providerErr)
	require.ErrorIs(t, err, observerErr)
	require.Equal(t, err, observed.Close())
	require.True(t, raw.closed)
}

func TestValidatedStreamRecvObserverMayReenterResponseAndClose(t *testing.T) {
	raw := &validatedStreamFixture{
		chunks: []Chunk{TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "visible"}},
		}}},
	}
	validated := mustValidateStream(t, raw, &Request{})
	observer := &reentrantStreamObserver{closeOnRecv: true}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)
	observer.stream = observed

	chunk, err := observed.Recv()

	require.NoError(t, err)
	require.Equal(t, "visible", chunk.(TextChunk).Message.Parts[0].(TextPart).Text)
	require.Nil(t, observer.recvResponse)
	require.NoError(t, observer.recvCloseErr)
	require.Nil(t, observer.closeResponse)
	require.NoError(t, observer.closeReentryErr)
	require.Equal(t, 1, observer.closeCalls)
	require.True(t, raw.closed)
}

func TestValidatedStreamCloseObserverMayReenterResponseAndClose(t *testing.T) {
	providerErr := errors.New("provider close failed")
	raw := &validatedStreamFixture{closeErr: providerErr}
	validated := mustValidateStream(t, raw, &Request{})
	observer := &reentrantStreamObserver{}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)
	observer.stream = observed

	err = observed.Close()

	require.ErrorIs(t, err, providerErr)
	require.Nil(t, observer.closeResponse)
	require.ErrorIs(t, observer.closeReentryErr, providerErr)
	require.Equal(t, 1, observer.closeCalls)
	require.Equal(t, err, observed.Close())
}

func TestValidatedStreamObserverConcurrentOperations(t *testing.T) {
	const chunkCount = 100
	chunks := make([]Chunk, chunkCount)
	for index := range chunks {
		chunks[index] = TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "x"}},
		}}
	}
	raw := &validatedStreamFixture{chunks: chunks}
	validated := mustValidateStream(t, raw, &Request{})
	observer := &recordingStreamObserver{}
	observed, err := validated.Observe(observer)
	require.NoError(t, err)

	var operations sync.WaitGroup
	recvResult := make(chan error, 1)
	closeResult := make(chan error, 1)
	operations.Add(3)
	go func() {
		defer operations.Done()
		for range chunkCount {
			_, recvErr := observed.Recv()
			if recvErr != nil {
				recvResult <- recvErr
				return
			}
		}
		recvResult <- nil
	}()
	go func() {
		defer operations.Done()
		for range chunkCount {
			_ = observed.Response()
		}
	}()
	go func() {
		defer operations.Done()
		for range chunkCount {
			if closeErr := observed.Close(); closeErr != nil {
				closeResult <- closeErr
				return
			}
		}
		closeResult <- nil
	}()
	operations.Wait()

	require.NoError(t, <-recvResult)
	require.NoError(t, <-closeResult)
	require.Len(t, observer.observations, chunkCount)
	require.True(t, raw.closed)
}

func TestValidatedStreamDoesNotReceiveSiblingChunkBeforeObserverDecision(t *testing.T) {
	rejected := errors.New("first chunk rejected")
	raw := &siblingStreamFixture{secondRecv: make(chan struct{})}
	base := mustValidateStream(t, raw, &Request{})
	shared := &blockingRejectingStreamObserver{
		firstEntered: make(chan struct{}),
		releaseFirst: make(chan struct{}),
		rejectErr:    rejected,
	}
	observed, err := base.Observe(shared)
	require.NoError(t, err)

	results := make(chan error, 2)
	go func() {
		_, recvErr := observed.Recv()
		results <- recvErr
	}()
	<-shared.firstEntered
	go func() {
		_, recvErr := base.Recv()
		results <- recvErr
	}()

	select {
	case <-raw.secondRecv:
		t.Fatal("sibling view consumed chunk 2 before observers decided chunk 1")
	default:
	}
	close(shared.releaseFirst)
	require.ErrorIs(t, <-results, rejected)
	require.ErrorIs(t, <-results, rejected)
	select {
	case <-raw.secondRecv:
		t.Fatal("sibling view consumed chunk 2 after chunk 1 was rejected")
	default:
	}
	require.Equal(t, 1, raw.next)
	require.EqualValues(t, 1, shared.calls.Load())
}

func TestValidatedStreamReentrantClosePreservesEveryObserverError(t *testing.T) {
	providerErr := errors.New("provider close failed")
	firstErr := errors.New("first observer close failed")
	secondErr := errors.New("second observer close failed")
	raw := &validatedStreamFixture{
		chunks: []Chunk{TextChunk{Message: Message{
			Role:  ConversationRoleAssistant,
			Parts: []Part{TextPart{Text: "visible"}},
		}}},
		closeErr: providerErr,
	}
	base := mustValidateStream(t, raw, &Request{})
	firstObserver := &reentrantCloseErrorObserver{closeOnRecv: true, closeErr: firstErr}
	first, err := base.Observe(firstObserver)
	require.NoError(t, err)
	secondObserver := &reentrantCloseErrorObserver{closeErr: secondErr}
	observed, err := first.Observe(secondObserver)
	require.NoError(t, err)
	firstObserver.stream = observed
	secondObserver.stream = observed

	chunk, err := observed.Recv()

	require.Nil(t, chunk)
	require.ErrorIs(t, err, providerErr)
	require.ErrorIs(t, err, firstErr)
	require.ErrorIs(t, err, secondErr)
	require.ErrorIs(t, firstObserver.recvCloseErr, providerErr)
	require.ErrorIs(t, firstObserver.recvCloseErr, secondErr)
	require.ErrorIs(t, firstObserver.closeReentryErr, providerErr)
	require.ErrorIs(t, firstObserver.closeReentryErr, secondErr)
	require.ErrorIs(t, secondObserver.closeReentryErr, providerErr)
	require.Equal(t, 1, firstObserver.closeCalls)
	require.Equal(t, 1, secondObserver.closeCalls)

	finalErr := observed.Close()
	require.ErrorIs(t, finalErr, providerErr)
	require.ErrorIs(t, finalErr, firstErr)
	require.ErrorIs(t, finalErr, secondErr)
}

func TestValidatedStreamSerializesCancellationAndIdempotentClose(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	providerCloseErr := errors.New("provider close failed")
	raw := &cancellationBlockedStreamFixture{
		ctx:            ctx,
		recvStarted:    make(chan struct{}),
		cleanupStarted: make(chan struct{}),
		cleanupRelease: make(chan struct{}),
		closeCalled:    make(chan struct{}),
		closeErr:       providerCloseErr,
	}
	stream := mustValidateStream(t, raw, &Request{})
	recvErr := make(chan error, 1)
	go func() {
		_, err := stream.Recv()
		recvErr <- err
	}()
	<-raw.recvStarted

	closeErr := make(chan error, 1)
	go func() {
		closeErr <- stream.Close()
	}()
	select {
	case <-raw.closeCalled:
		t.Fatal("Close operated on the provider while Recv was active")
	default:
	}

	cancel()
	<-raw.cleanupStarted
	select {
	case <-raw.closeCalled:
		t.Fatal("Close ran before provider receive cleanup finished")
	default:
	}
	select {
	case <-closeErr:
		t.Fatal("Close returned before provider receive cleanup finished")
	default:
	}
	close(raw.cleanupRelease)

	require.ErrorIs(t, <-recvErr, context.Canceled)
	require.ErrorIs(t, <-closeErr, providerCloseErr)
	require.ErrorIs(t, stream.Close(), providerCloseErr)
	require.Equal(t, 1, raw.closeCalls)
}

func (s *validatedStreamFixture) Recv() (Chunk, error) {
	if s.next == len(s.chunks) {
		return nil, io.EOF
	}
	chunk := s.chunks[s.next]
	s.next++
	return chunk, nil
}

func (s *validatedStreamFixture) Response() *Response {
	return s.response
}

func (s *validatedStreamFixture) Close() error {
	s.closed = true
	return s.closeErr
}

func (s *siblingStreamFixture) Recv() (Chunk, error) {
	s.next++
	if s.next == 2 {
		close(s.secondRecv)
	}
	return TextChunk{Message: Message{
		Role:  ConversationRoleAssistant,
		Parts: []Part{TextPart{Text: "visible"}},
	}}, nil
}

func (*siblingStreamFixture) Response() *Response {
	return nil
}

func (*siblingStreamFixture) Close() error {
	return nil
}

func mustValidateStream(t *testing.T, raw Streamer, request *Request) *ValidatedStream {
	t.Helper()
	if request.StructuredOutput != nil {
		require.NoError(t, SetCompletionValidator(
			request,
			func(*Response, *Completion) error { return nil },
		))
	}
	contract, err := NewRequestContract(request)
	require.NoError(t, err)
	stream, err := contract.ValidateStream(raw)
	require.NoError(t, err)
	return stream
}

func (o *recordingStreamObserver) ObserveStreamRecv(observation StreamObservation) error {
	o.observations = append(o.observations, observation)
	if observation.Err == nil {
		o.chunks = append(o.chunks, observation.Chunk)
	}
	return nil
}

func (o *recordingStreamObserver) ObserveStreamClose(error) error {
	return o.closeErr
}

func (*mutatingStreamObserver) ObserveStreamRecv(observation StreamObservation) error {
	if tool, ok := observation.Chunk.(ToolCallChunk); ok {
		tool.ToolCall.Payload[10] = 'X'
	}
	if observation.Response != nil {
		tool := observation.Response.Content[0].Parts[0].(ToolUsePart)
		tool.Input[10] = 'Y'
	}
	return nil
}

func (*mutatingStreamObserver) ObserveStreamClose(error) error {
	return nil
}

func (o *reentrantStreamObserver) ObserveStreamRecv(StreamObservation) error {
	o.recvResponse = o.stream.Response()
	if o.closeOnRecv {
		o.recvCloseErr = o.stream.Close()
	}
	return nil
}

func (o *reentrantStreamObserver) ObserveStreamClose(error) error {
	o.closeCalls++
	o.closeResponse = o.stream.Response()
	o.closeReentryErr = o.stream.Close()
	return nil
}

func (o *blockingRejectingStreamObserver) ObserveStreamRecv(StreamObservation) error {
	if o.calls.Add(1) == 1 {
		close(o.firstEntered)
		<-o.releaseFirst
		return o.rejectErr
	}
	return nil
}

func (*blockingRejectingStreamObserver) ObserveStreamClose(error) error {
	return nil
}

func (o *reentrantCloseErrorObserver) ObserveStreamRecv(StreamObservation) error {
	if o.closeOnRecv {
		o.recvCloseErr = o.stream.Close()
	}
	return nil
}

func (o *reentrantCloseErrorObserver) ObserveStreamClose(error) error {
	o.closeCalls++
	o.closeReentryErr = o.stream.Close()
	return o.closeErr
}
