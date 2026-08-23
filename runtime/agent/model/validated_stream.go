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
	// ValidatedStream owns one provider stream and exposes output only after it
	// passes the immutable contract copied from its request.
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
	//     A receive callback may call Response to see the state produced by that
	//     receive, or Close to close the provider once. A close callback may call
	//     Response and may call Close again; that reentrant Close sees the
	//     provider close result while the observer's own result is still pending.
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

		closed           bool
		providerCloseErr error

		closeMu      sync.Mutex
		closeStarted bool
		closeErr     error
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
		rejectedUsage    *TokenUsage
		responseEvidence ResponseEvidence
		finished         bool
		terminalErr      error
	}

	observedValidatedStreamer struct {
		observer   StreamObserver
		core       *validatedStreamCore
		callbackMu sync.Mutex

		mu                sync.Mutex
		finished          bool
		terminalErr       error
		recvCallback      bool
		closeCallback     bool
		closeCallbackDone chan struct{}
		closePending      bool
		closeDone         bool
		closeErr          error
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

// Observe attaches recording behavior to this validated stream. Every returned
// view shares the owned provider core and observer registry, so a view retained
// before observation still closes the complete observed stream. The distinct
// views serialize their provider operations through the core and serialize each
// inherited observer through that observer's callback mutex.
func (s *ValidatedStream) Observe(observer StreamObserver) (*ValidatedStream, error) {
	if s == nil || s.core == nil || s.core.inner == nil {
		return nil, errors.New("model stream is not an intact validated stream")
	}
	if observer == nil {
		return nil, errors.New("model stream observer is nil")
	}
	s.observers.mu.Lock()
	s.observers.list = append(s.observers.list, &observedValidatedStreamer{
		observer: observer,
		core:     s.core,
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
	s.core.mu.Lock()
	if !s.core.closed {
		s.core.providerCloseErr = s.core.inner.Close()
		s.core.closed = true
	}
	providerErr := s.core.providerCloseErr
	s.core.mu.Unlock()
	observers := s.streamObservers()
	started, err := s.core.beginObserverClose(providerErr)
	if !started {
		return err
	}
	for _, observer := range observers {
		err = s.core.currentCloseErr()
		err = observer.observeClose(err)
		s.core.recordCloseErr(err)
	}
	return s.core.finishObserverClose()
}

// beginObserverClose gives one caller ownership of the lineage-wide observer
// close chain. Reentrant calls return the complete error prefix accumulated so
// far instead of waiting on the callback that invoked them.
func (s *validatedStreamCore) beginObserverClose(providerErr error) (bool, error) {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closeStarted {
		return false, s.closeErr
	}
	s.closeStarted = true
	s.closeErr = providerErr
	return true, providerErr
}

// currentCloseErr returns every provider and observer failure recorded so far.
func (s *validatedStreamCore) currentCloseErr() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
}

// recordCloseErr retains the full incoming error chain returned by one
// observer before the next observer runs.
func (s *validatedStreamCore) recordCloseErr(err error) {
	s.closeMu.Lock()
	s.closeErr = err
	s.closeMu.Unlock()
}

// completeDeferredObserverClose merges a reentrant observer result after its
// receive callback returns.
func (s *validatedStreamCore) completeDeferredObserverClose(err error) {
	s.closeMu.Lock()
	s.closeErr = err
	s.closeMu.Unlock()
}

// finishObserverClose marks dispatch complete. A deferred callback will publish
// the final result when its active receive callback returns.
func (s *validatedStreamCore) finishObserverClose() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	return s.closeErr
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
func rejectedStreamUsage(streamer Streamer) *TokenUsage {
	switch actual := streamer.(type) {
	case *ValidatedStream:
		if actual == nil || actual.core == nil {
			return nil
		}
		actual.core.mu.Lock()
		defer actual.core.mu.Unlock()
		return rejectedStreamUsage(actual.core.inner)
	case *validatedStreamer:
		if actual.rejectedUsage != nil {
			usage := *actual.rejectedUsage
			return &usage
		}
		return rejectedStreamUsage(actual.inner)
	}
	return nil
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
	if s.finished {
		if s.terminalErr != nil {
			return nil, s.terminalErr
		}
		return nil, io.EOF
	}
	chunk, err := s.inner.Recv()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.finished = true
			s.terminalErr = err
			return nil, err
		}
		rawResponse := s.inner.Response()
		s.responseEvidence.Present = rawResponse != nil
		s.rejectedUsage = s.contract.validatedUsageEvidence(rawResponse)
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
		return nil, io.EOF
	}
	if preflightErr := s.validator.preflightStreamChunk(chunk); preflightErr != nil {
		return nil, s.failValidation(preflightErr)
	}
	owned, err := cloneChunk(chunk)
	if err != nil {
		return nil, s.failValidation(err)
	}
	if usage, ok := owned.(UsageChunk); ok {
		s.validator.stampUsageIdentity(&usage.Usage)
		owned = usage
	}
	if err := validateCanonicalChunk(owned); err != nil {
		s.rejectedUsage = rejectedUsageEvidence(owned)
		return nil, s.failValidation(err)
	}
	if err := s.validator.acceptOwned(owned); err != nil {
		s.rejectedUsage = rejectedUsageEvidence(owned)
		return nil, s.failValidation(err)
	}
	return owned, nil
}

// Response returns the owned complete response after a clean EOF.
func (s *validatedStreamer) Response() *Response {
	return s.response
}

// Close closes the provider stream.
func (s *validatedStreamer) Close() error {
	return s.inner.Close()
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
	var rejectedUsage *TokenUsage
	evidence := streamResponseEvidence(inner)
	if errors.Is(err, io.EOF) {
		observedResponse, copyErr = CloneResponse(inner.Response())
	} else if IsStreamValidationError(err) {
		rejectedUsage = rejectedStreamUsage(inner)
		observedResponse, copyErr = rejectedStreamResponse(inner)
	}
	if copyErr != nil {
		err = newOutputValidationError(
			errors.Join(err, copyErr),
			evidence,
			observedResponse,
			rejectedUsage,
		)
	}
	return StreamObservation{
		Chunk:            observedChunk,
		RejectedUsage:    rejectedUsage,
		Response:         observedResponse,
		ResponseEvidence: evidence,
		Err:              err,
	}
}

// terminal returns the result latched after the observer saw EOF or an error.
func (s *observedValidatedStreamer) terminal() (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finished, s.terminalErr
}

// observeRecv invokes one receive callback without holding the provider mutex.
// Close requests made by that callback close the provider immediately and defer
// the close callback until this receive callback returns.
func (s *observedValidatedStreamer) observeRecv(
	chunk Chunk,
	err error,
	observation StreamObservation,
) (Chunk, error) {
	s.beginRecvCallback()
	observerErr := s.observer.ObserveStreamRecv(observation)
	runClose := s.finishRecvCallback(err, observerErr)
	s.callbackMu.Unlock()
	var closeErr error
	if runClose {
		closeErr = s.completeCloseCallback(s.core.currentCloseErr(), true)
	}
	if resultErr := errors.Join(err, observerErr, closeErr); resultErr != nil {
		return nil, resultErr
	}
	return chunk, nil
}

// beginRecvCallback serializes this observer across every derived view. It
// waits for an externally started close callback, then marks the observer busy
// so a reentrant Close can defer its own callback safely.
func (s *observedValidatedStreamer) beginRecvCallback() {
	for {
		s.callbackMu.Lock()
		s.mu.Lock()
		if !s.closeCallback {
			s.recvCallback = true
			s.mu.Unlock()
			return
		}
		done := s.closeCallbackDone
		s.mu.Unlock()
		s.callbackMu.Unlock()
		<-done
	}
}

// finishRecvCallback latches stream termination and transfers a reentrant Close
// request to the caller for callback delivery.
func (s *observedValidatedStreamer) finishRecvCallback(
	streamErr error,
	observerErr error,
) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if observerErr != nil || (streamErr != nil && !errors.Is(streamErr, io.EOF)) {
		s.finished = true
		s.terminalErr = errors.Join(streamErr, observerErr)
	} else if streamErr != nil {
		s.finished = true
	}
	s.recvCallback = false
	if !s.closePending || s.closeDone || s.closeCallback {
		return false
	}
	s.closePending = false
	s.closeCallback = true
	s.closeCallbackDone = make(chan struct{})
	return true
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

// observeClose invokes the close callback once without holding the provider
// mutex. A reentrant close callback sees providerErr; subsequent calls see the
// final joined provider and observer result.
func (s *observedValidatedStreamer) observeClose(providerErr error) error {
	s.mu.Lock()
	switch {
	case s.closeDone:
		err := s.closeErr
		s.mu.Unlock()
		return err
	case s.closeCallback:
		s.mu.Unlock()
		return providerErr
	case s.recvCallback:
		s.closePending = true
		s.mu.Unlock()
		return providerErr
	default:
		s.closeCallback = true
		s.closeCallbackDone = make(chan struct{})
		s.mu.Unlock()
		return s.completeCloseCallback(providerErr, false)
	}
}

// completeCloseCallback records the observer result and releases Recv callers
// waiting to deliver their next callback.
func (s *observedValidatedStreamer) completeCloseCallback(providerErr error, deferred bool) error {
	s.callbackMu.Lock()
	defer s.callbackMu.Unlock()
	observerErr := s.observer.ObserveStreamClose(providerErr)
	finalErr := errors.Join(providerErr, observerErr)
	s.mu.Lock()
	s.closeErr = finalErr
	s.closeDone = true
	s.closeCallback = false
	close(s.closeCallbackDone)
	s.closeCallbackDone = nil
	s.mu.Unlock()
	if deferred {
		s.core.completeDeferredObserverClose(finalErr)
	}
	return finalErr
}

// failValidation latches the first stream contract failure.
func (s *validatedStreamer) failValidation(err error) error {
	s.finished = true
	s.terminalErr = newOutputValidationError(
		err,
		s.responseEvidence,
		s.rejected,
		s.rejectedUsage,
	)
	return s.terminalErr
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
