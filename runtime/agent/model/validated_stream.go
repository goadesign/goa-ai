// Package model owns the provider-neutral stream boundary. This file turns a
// provider streamer into one owned, validated stream before runtime, tracing,
// planner, or completion code can retain its output.
package model

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
)

type (
	// ValidatedStream owns one provider stream. It exposes discardable preview
	// chunks after per-chunk validation, but withholds completed tool calls and
	// structured output until the complete response passes the immutable
	// contract copied from its request.
	//
	// Concurrency contract:
	//   - Recv, Response, and Close may be called from different goroutines.
	//   - At most one of those calls operates on the wrapped Streamer at a time.
	//   - Close waits for an active Recv to return; it does not interrupt Recv.
	//     A caller shutting down a blocked receive must first cancel the context
	//     used to create the provider stream, then call Close.
	//   - One receive sequence covers the provider Recv and every observer
	//     callback for that chunk across all sibling views. The next provider
	//     chunk is not consumed until that sequence accepts or rejects the
	//     current chunk.
	//   - Close invokes the wrapped Streamer exactly once and every later Close
	//     after observer completion returns that same final result.
	//   - Observer callbacks run after the provider operation mutex is released.
	//     One observer callback is serialized across all derived views that
	//     inherit it.
	//     A callback may call Response to see the state produced by that
	//     operation. Callbacks must not call Recv or Close on the same stream;
	//     those lifecycle operations wait for the callback to finish.
	ValidatedStream struct {
		core      *validatedStreamCore
		observers *validatedStreamObservers
	}

	// validatedStreamCore serializes every operation on one owned provider
	// stream, even when callers retain both observed and unobserved views.
	validatedStreamCore struct {
		inner Streamer
		mu    sync.Mutex
		recv  sync.Mutex

		started          bool
		closed           bool
		providerCloseErr error

		closeObserved bool
		closeErr      error
	}

	// validatedStreamObservers owns the observer wrappers shared by every view
	// of one validated provider stream.
	validatedStreamObservers struct {
		mu   sync.RWMutex
		list []*observedValidatedStreamer
	}

	// StreamObserver records output from one validated stream without replacing
	// its chunks or complete response.
	StreamObserver interface {
		// ObserveStreamRecv receives safe copies and bounded evidence from one
		// receive operation. Returning an error stops the outer stream.
		ObserveStreamRecv(StreamObservation) error
		// ObserveStreamClose receives the result of closing the validated inner
		// stream. Returning an error makes Close fail.
		ObserveStreamClose(error) error
	}

	validatedStreamer struct {
		inner            Streamer
		contract         *RequestContract
		validator        *streamValidator
		response         *Response
		rejected         *Response
		rejectedDelta    *TokenUsage
		rejectedTotal    *TokenUsage
		responseEvidence ResponseEvidence
		pending          []Chunk
		finished         bool
		terminalErr      error
	}

	observedValidatedStreamer struct {
		observer   StreamObserver
		callbackMu sync.Mutex

		mu          sync.Mutex
		finished    bool
		terminalErr error
		closeDone   bool
		closeErr    error
	}
)

// ValidateStream binds one provider stream to this immutable request contract.
func (c *RequestContract) ValidateStream(streamer Streamer) (*ValidatedStream, error) {
	if isNilStreamer(streamer) {
		return nil, c.RejectResponse(nil, nilStreamerError(streamer))
	}
	return &ValidatedStream{
		core: &validatedStreamCore{inner: &validatedStreamer{
			inner:     streamer,
			contract:  c,
			validator: newStreamValidator(c),
		}},
		observers: &validatedStreamObservers{},
	}, nil
}

// Observe attaches recording behavior before the first receive or close. Every
// returned view shares the owned provider stream and observer registry, so a
// view retained before observation still closes the complete observed stream.
// The distinct views serialize provider operations and each observer callback.
func (s *ValidatedStream) Observe(observer StreamObserver) (*ValidatedStream, error) {
	if s == nil || s.core == nil || s.core.inner == nil {
		return nil, errors.New("model stream is not an intact validated stream")
	}
	if observer == nil {
		return nil, errors.New("model stream observer is nil")
	}
	s.core.recv.Lock()
	defer s.core.recv.Unlock()
	if s.core.started {
		return nil, errors.New("model stream observers must be attached before receive or close")
	}
	s.observers.mu.Lock()
	s.observers.list = append(s.observers.list, &observedValidatedStreamer{
		observer: observer,
	})
	s.observers.mu.Unlock()
	return &ValidatedStream{
		core:      s.core,
		observers: s.observers,
	}, nil
}

// IsStreamValidationError reports whether err came from chunk or complete
// response validation rather than from the provider transport.
func IsStreamValidationError(err error) bool {
	var validationErr *OutputValidationError
	return errors.As(err, &validationErr)
}

// Recv returns the next chunk from the owned validated stream.
func (s *ValidatedStream) Recv() (Chunk, error) {
	if s == nil || s.core == nil || s.core.inner == nil {
		return nil, errors.New("model stream is not an intact validated stream")
	}
	s.core.recv.Lock()
	defer s.core.recv.Unlock()
	s.core.started = true
	observers := s.streamObservers()
	if len(observers) > 0 {
		if finished, terminalErr := observers[len(observers)-1].terminal(); finished {
			if terminalErr != nil {
				return nil, terminalErr
			}
			return nil, io.EOF
		}
	}

	s.core.mu.Lock()
	chunk, err := s.core.inner.Recv()
	var observation StreamObservation
	if len(observers) > 0 {
		observation = observeStreamResult(s.core.inner, chunk, err)
	}
	s.core.mu.Unlock()
	if len(observers) == 0 {
		return chunk, err
	}
	for index, observer := range observers {
		chunk, err = observer.observeRecv(chunk, err, observation)
		if index+1 < len(observers) {
			s.core.mu.Lock()
			observation = observeStreamResult(s.core.inner, chunk, err)
			s.core.mu.Unlock()
		}
	}
	return chunk, err
}

// Response returns the owned complete response after a clean EOF.
func (s *ValidatedStream) Response() *Response {
	if s == nil || s.core == nil || s.core.inner == nil {
		return nil
	}
	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	return s.core.inner.Response()
}

// Close waits for any active receive, then closes the owned provider stream
// exactly once.
func (s *ValidatedStream) Close() error {
	if s == nil || s.core == nil || s.core.inner == nil {
		return errors.New("model stream is not an intact validated stream")
	}
	s.core.recv.Lock()
	defer s.core.recv.Unlock()
	s.core.started = true
	s.core.mu.Lock()
	if !s.core.closed {
		s.core.providerCloseErr = s.core.inner.Close()
		s.core.closed = true
	}
	providerErr := s.core.providerCloseErr
	s.core.mu.Unlock()
	if s.core.closeObserved {
		return s.core.closeErr
	}
	observers := s.streamObservers()
	err := providerErr
	for _, observer := range observers {
		err = observer.observeClose(err)
	}
	s.core.closeObserved = true
	s.core.closeErr = err
	return err
}

// streamObservers snapshots the observers attached before one Recv or Close
// operation starts.
func (s *ValidatedStream) streamObservers() []*observedValidatedStreamer {
	s.observers.mu.RLock()
	defer s.observers.mu.RUnlock()
	return append([]*observedValidatedStreamer(nil), s.observers.list...)
}

// rejectedStreamResponse returns the owned complete provider response that
// failed stream reconciliation. Chunk-level failures occur before a complete
// response exists and return nil.
func rejectedStreamResponse(streamer Streamer) (*Response, error) {
	var response *Response
	switch actual := streamer.(type) {
	case *ValidatedStream:
		if actual == nil || actual.core == nil {
			return nil, nil
		}
		actual.core.mu.Lock()
		defer actual.core.mu.Unlock()
		return rejectedStreamResponse(actual.core.inner)
	case *validatedStreamer:
		if actual.rejected == nil {
			return rejectedStreamResponse(actual.inner)
		}
		response = actual.rejected
	}
	return cloneResponseForValidation(response)
}

// rejectedStreamUsage returns valid numeric counts from a rejected usage chunk
// without returning its invalid identity fields.
func rejectedStreamUsage(streamer Streamer) (delta, total *TokenUsage) {
	switch actual := streamer.(type) {
	case *ValidatedStream:
		if actual == nil || actual.core == nil {
			return nil, nil
		}
		actual.core.mu.Lock()
		defer actual.core.mu.Unlock()
		return rejectedStreamUsage(actual.core.inner)
	case *validatedStreamer:
		if actual.rejectedDelta != nil {
			usage := *actual.rejectedDelta
			delta = &usage
		}
		if actual.rejectedTotal != nil {
			usage := *actual.rejectedTotal
			total = &usage
		}
		if delta != nil || total != nil {
			return delta, total
		}
		return rejectedStreamUsage(actual.inner)
	}
	return nil, nil
}

// streamResponseEvidence returns evidence captured from the raw complete
// response before copying.
func streamResponseEvidence(streamer Streamer) ResponseEvidence {
	switch actual := streamer.(type) {
	case *ValidatedStream:
		if actual == nil || actual.core == nil {
			return ResponseEvidence{}
		}
		actual.core.mu.Lock()
		defer actual.core.mu.Unlock()
		return streamResponseEvidence(actual.core.inner)
	case *validatedStreamer:
		if actual.responseEvidence.Present {
			return actual.responseEvidence
		}
		return streamResponseEvidence(actual.inner)
	}
	return ResponseEvidence{}
}

// Recv owns and validates each provider chunk before returning it. At EOF it
// owns and validates the complete response before exposing a clean end.
func (s *validatedStreamer) Recv() (Chunk, error) {
	if len(s.pending) > 0 {
		chunk := s.pending[0]
		s.pending = s.pending[1:]
		return chunk, nil
	}
	if s.finished {
		if s.terminalErr != nil {
			return nil, s.terminalErr
		}
		return nil, io.EOF
	}
	for {
		chunk, err := s.inner.Recv()
		if err != nil {
			return s.finishReceive(err)
		}
		if preflightErr := s.validator.preflightStreamChunk(chunk); preflightErr != nil {
			return nil, s.failValidation(preflightErr)
		}
		owned, cloneErr := cloneChunk(chunk)
		if cloneErr != nil {
			return nil, s.failValidation(cloneErr)
		}
		if usage, ok := owned.(UsageChunk); ok {
			s.validator.stampUsageIdentity(&usage.Usage)
			owned = usage
		}
		if validateErr := validateCanonicalChunk(owned); validateErr != nil {
			s.rejectedDelta = rejectedUsageEvidence(owned)
			return nil, s.failValidation(validateErr)
		}
		if validateErr := s.validator.acceptOwned(owned); validateErr != nil {
			s.rejectedDelta = rejectedUsageEvidence(owned)
			return nil, s.failValidation(validateErr)
		}
		if _, ok := owned.(UsageChunk); ok {
			usage := s.validator.usage
			s.rejectedDelta = &usage
		}
		if streamChunkRequiresTerminalValidation(owned) || len(s.pending) > 0 {
			s.pending = append(s.pending, owned)
			continue
		}
		return owned, nil
	}
}

// finishReceive reconciles a provider's terminal result before any retained
// completed tool call, structured completion, or later chunk can leave the
// validated stream.
func (s *validatedStreamer) finishReceive(err error) (Chunk, error) {
	if !errors.Is(err, io.EOF) {
		err = s.captureProviderRejection(err)
		s.finished = true
		s.terminalErr = err
		s.pending = nil
		return nil, err
	}
	rawResponse := s.inner.Response()
	s.responseEvidence.Present = rawResponse != nil
	s.rejectedTotal = s.contract.validatedUsageEvidence(rawResponse)
	var responseBudget dynamicValueWalk
	if preflightErr := preflightResponse(
		rawResponse,
		&responseBudget,
		dynamicCloneEvidence,
	); preflightErr != nil {
		return nil, s.failValidation(preflightErr)
	}
	if preflightErr := s.validator.preflightTerminalResponse(rawResponse); preflightErr != nil {
		return nil, s.failValidation(preflightErr)
	}
	s.responseEvidence = responseEvidencePreflighted(rawResponse)
	response, cloneErr := ownPreflightedResponse(rawResponse)
	if cloneErr != nil {
		return nil, s.failValidation(cloneErr)
	}
	if response != nil {
		s.validator.stampUsageIdentity(&response.Usage)
	}
	if finishErr := s.validator.finish(response); finishErr != nil {
		s.rejected = response
		return nil, s.failValidation(finishErr)
	}
	s.response = response
	s.finished = true
	if len(s.pending) == 0 {
		return nil, io.EOF
	}
	chunk := s.pending[0]
	s.pending = s.pending[1:]
	return chunk, nil
}

// Response returns the owned complete response after a clean EOF.
func (s *validatedStreamer) Response() *Response {
	return s.response
}

// Close closes the provider stream.
func (s *validatedStreamer) Close() error {
	return s.inner.Close()
}

// streamChunkRequiresTerminalValidation reports whether a completed semantic
// value must wait for the provider's complete response. Preview-only text,
// thinking, and argument deltas can remain visible while callers discard them
// if the later terminal validation fails.
func streamChunkRequiresTerminalValidation(chunk Chunk) bool {
	switch chunk.(type) {
	case ToolCallChunk, CompletionChunk:
		return true
	default:
		return false
	}
}

// observeStreamResult copies the completed provider operation into the
// observer contract while the provider state remains serialized.
func observeStreamResult(inner Streamer, chunk Chunk, err error) StreamObservation {
	observedChunk, copyErr := cloneChunk(chunk)
	if chunk == nil {
		observedChunk = nil
		copyErr = nil
	}
	var observedResponse *Response
	var rejectedUsageDelta *TokenUsage
	var rejectedUsageTotal *TokenUsage
	evidence := streamResponseEvidence(inner)
	if errors.Is(err, io.EOF) {
		observedResponse, copyErr = CloneResponse(inner.Response())
	} else if IsStreamValidationError(err) {
		rejectedUsageDelta, rejectedUsageTotal = rejectedStreamUsage(inner)
		observedResponse, copyErr = rejectedStreamResponse(inner)
	}
	if copyErr != nil {
		err = newOutputValidationError(
			errors.Join(err, copyErr),
			evidence,
			observedResponse,
			firstTokenUsage(rejectedUsageTotal, rejectedUsageDelta),
		)
	}
	return StreamObservation{
		Chunk:              observedChunk,
		RejectedUsageDelta: rejectedUsageDelta,
		RejectedUsageTotal: rejectedUsageTotal,
		Response:           observedResponse,
		ResponseEvidence:   evidence,
		Err:                err,
	}
}

// terminal returns the result latched after the observer saw EOF or an error.
func (s *observedValidatedStreamer) terminal() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished, s.terminalErr
}

// observeRecv invokes one receive callback while the shared stream lock keeps
// Close or another Recv from running before that callback finishes.
func (s *observedValidatedStreamer) observeRecv(
	chunk Chunk,
	err error,
	observation StreamObservation,
) (Chunk, error) {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	observerErr := s.observer.ObserveStreamRecv(observation)
	s.mu.Lock()
	if observerErr != nil || (err != nil && !errors.Is(err, io.EOF)) {
		s.finished = true
		s.terminalErr = errors.Join(err, observerErr)
	} else if err != nil {
		s.finished = true
	}
	s.mu.Unlock()
	if resultErr := errors.Join(err, observerErr); resultErr != nil {
		return nil, resultErr
	}
	return chunk, nil
}

// rejectedUsageEvidence projects only nonnegative token counts from a usage
// chunk that failed another part of validation.
func rejectedUsageEvidence(chunk Chunk) *TokenUsage {
	usage, ok := chunk.(UsageChunk)
	if !ok ||
		usage.Usage.InputTokens < 0 ||
		usage.Usage.OutputTokens < 0 ||
		usage.Usage.TotalTokens < 0 ||
		usage.Usage.CacheReadTokens < 0 ||
		usage.Usage.CacheWriteTokens < 0 {
		return nil
	}
	return &TokenUsage{
		InputTokens:      usage.Usage.InputTokens,
		OutputTokens:     usage.Usage.OutputTokens,
		TotalTokens:      usage.Usage.TotalTokens,
		CacheReadTokens:  usage.Usage.CacheReadTokens,
		CacheWriteTokens: usage.Usage.CacheWriteTokens,
	}
}

// observeClose invokes the close callback once after all receive observations
// finish. Every later call returns the same joined provider and observer error.
func (s *observedValidatedStreamer) observeClose(providerErr error) error {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	s.mu.Lock()
	if s.closeDone {
		err := s.closeErr
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	observerErr := s.observer.ObserveStreamClose(providerErr)
	finalErr := errors.Join(providerErr, observerErr)
	s.mu.Lock()
	s.closeErr = finalErr
	s.closeDone = true
	s.mu.Unlock()
	return finalErr
}

// failValidation latches the first stream contract failure.
func (s *validatedStreamer) failValidation(err error) error {
	s.pending = nil
	s.finished = true
	s.terminalErr = newOutputValidationError(
		err,
		s.responseEvidence,
		s.rejected,
		firstTokenUsage(s.rejectedTotal, s.rejectedDelta),
	)
	return s.terminalErr
}

// captureProviderRejection retains the bounded usage and response evidence
// carried by a provider's OutputValidationError before the stream latches that
// terminal error.
func (s *validatedStreamer) captureProviderRejection(err error) error {
	var outputErr *OutputValidationError
	if !errors.As(err, &outputErr) {
		return err
	}
	s.responseEvidence = outputErr.Evidence()
	s.rejectedTotal = outputErr.Usage()
	rejected, cloneErr := outputErr.RejectedResponse()
	if cloneErr != nil {
		return errors.Join(err, cloneErr)
	}
	s.rejected = rejected
	return err
}

// firstTokenUsage prefers a complete usage total and otherwise returns the
// available delta for OutputValidationError's single usage field.
func firstTokenUsage(values ...*TokenUsage) *TokenUsage {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

// isNilStreamer detects nil and typed-nil interface values at the model
// boundary before any Streamer method can be invoked.
func isNilStreamer(streamer Streamer) bool {
	if streamer == nil {
		return true
	}
	value := reflect.ValueOf(streamer)
	kind := value.Kind()
	if kind < reflect.Chan || kind > reflect.Slice {
		return false
	}
	return value.IsNil()
}

// nilStreamerError identifies the concrete typed-nil stream when one was
// supplied through the Streamer interface.
func nilStreamerError(streamer Streamer) error {
	if streamer == nil {
		return errors.New("model stream is nil")
	}
	return fmt.Errorf("model stream is typed nil %T", streamer)
}
