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

	"goa.design/goa-ai/runtime/agent/internal/modelcall"
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
		terminal         bool
		terminalErr      error
		outcome          modelcall.Outcome
		lifecycle        *clientCallLifecycle

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
		// receive operation. Every observer receives the same source result;
		// returning an error stops the outer stream without changing later
		// observer input.
		ObserveStreamRecv(StreamObservation) error
		// ObserveStreamClose receives the result of closing the validated inner
		// stream. Every observer receives the same close result; returning an
		// error makes Close fail without changing later observer input.
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
		observer StreamObserver
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
	s.core.mu.Lock()
	if s.core.terminal {
		err := s.core.terminalErr
		s.core.mu.Unlock()
		return nil, err
	}
	chunk, sourceErr := s.core.inner.Recv()
	observation := observeStreamResult(s.core.inner, chunk, sourceErr)
	sourceErr = observation.Err
	receive, validation := classifyReceive(sourceErr)
	s.core.outcome.ProviderReceives = append(s.core.outcome.ProviderReceives, receive)
	s.core.outcome.Validations = append(s.core.outcome.Validations, validation)
	if sourceErr != nil {
		s.core.outcome.Completed = true
	}
	s.core.mu.Unlock()

	frozen := make([]StreamObservation, len(observers))
	var copyErr error
	for i := range observers {
		frozen[i], copyErr = cloneStreamObservation(observation)
		if copyErr != nil {
			break
		}
	}
	if copyErr != nil {
		s.core.mu.Lock()
		s.core.outcome.Framework = modelcall.Result{
			Called: true,
			Err:    errors.Join(s.core.outcome.Framework.Err, copyErr),
		}
		s.core.mu.Unlock()
		sourceErr = errors.Join(sourceErr, copyErr)
	}
	results := make([]modelcall.Result, len(observers))
	for i, observer := range observers {
		results[i] = modelcall.Result{
			Called: true,
			Err:    observer.observeRecv(frozen[i]),
		}
	}
	err := joinResultErrors(sourceErr, "stream receive observer", results)
	s.core.mu.Lock()
	s.core.outcome.ReceiveObservers = append(s.core.outcome.ReceiveObservers, results)
	if err != nil {
		s.core.terminal = true
		s.core.terminalErr = err
	}
	s.core.mu.Unlock()
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
	if s.core.closeObserved {
		err := s.core.closeErr
		s.core.mu.Unlock()
		return err
	}
	if !s.core.closed {
		s.core.providerCloseErr = s.core.inner.Close()
		s.core.closed = true
	}
	providerErr := s.core.providerCloseErr
	s.core.outcome.ProviderClose = modelcall.Result{Called: true, Err: providerErr}
	s.core.outcome.Incomplete = !s.core.outcome.Completed
	s.core.mu.Unlock()
	observers := s.streamObservers()
	results := make([]modelcall.Result, len(observers))
	for i, observer := range observers {
		results[i] = modelcall.Result{
			Called: true,
			Err:    observer.observeClose(providerErr),
		}
	}
	s.core.mu.Lock()
	s.core.outcome.CloseObservers = results
	lifecycle := s.core.lifecycle
	if lifecycle != nil {
		lifecycle.outcome = s.core.outcome.Clone()
		lifecycle.outcome.Context = contextResult(lifecycle.ctx)
	}
	s.core.mu.Unlock()
	var err error
	if lifecycle == nil {
		err = joinResultErrors(providerErr, "stream close observer", results)
	} else {
		err = lifecycle.finalizeClose()
	}
	s.core.mu.Lock()
	s.core.closeObserved = true
	s.core.closeErr = err
	s.core.mu.Unlock()
	return err
}

// registerClientCallLifecycle stores the prepared calls and setup results that
// Close finalizes after every receive and close observer has returned.
func (s *ValidatedStream) registerClientCallLifecycle(lifecycle *clientCallLifecycle) {
	s.core.recv.Lock()
	defer s.core.recv.Unlock()
	s.core.mu.Lock()
	defer s.core.mu.Unlock()
	s.core.lifecycle = lifecycle
	s.core.outcome = lifecycle.outcome.Clone()
}

// streamObservers snapshots the observers attached before one Recv or Close
// operation starts.
func (s *ValidatedStream) streamObservers() []*observedValidatedStreamer {
	s.observers.mu.RLock()
	defer s.observers.mu.RUnlock()
	return append([]*observedValidatedStreamer(nil), s.observers.list...)
}

// rejectedStreamResponse returns a complete provider response only for terminal
// failures that may retain one. A generated payload correction suppresses the
// response at this validation layer and every nested layer so observers cannot
// receive rejected tool arguments.
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
		if recoveryCorrectionFromError(actual.terminalErr) != "" {
			return nil, nil
		}
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
	if !modelcall.Exact(err, io.EOF) {
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

// Close discards any accepted chunks the caller did not consume, then closes
// the provider stream.
func (s *validatedStreamer) Close() error {
	clear(s.pending)
	s.pending = nil
	return s.inner.Close()
}

// streamChunkRequiresTerminalValidation reports whether a model-authored value
// must wait for the provider's complete response. Tool argument fragments stay
// private until the completed call passes its generated decoder; completed
// calls and structured output likewise wait for terminal reconciliation.
func streamChunkRequiresTerminalValidation(chunk Chunk) bool {
	switch chunk.(type) {
	case ToolCallDeltaChunk, ToolCallChunk, CompletionChunk:
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
	if modelcall.Exact(err, io.EOF) {
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

// classifyReceive separates provider completion, provider failure, and typed
// validation before any observer callback can add another error.
func classifyReceive(err error) (modelcall.Result, modelcall.Result) {
	provider := modelcall.Result{Called: true}
	validation := modelcall.Result{Called: true}
	if err == nil || modelcall.Exact(err, io.EOF) {
		return provider, validation
	}
	var outputErr *OutputValidationError
	if errors.As(err, &outputErr) {
		validation.Err = err
		return provider, validation
	}
	provider.Err = err
	return provider, validation
}

// cloneStreamObservation gives one observer private model values while
// preserving the exact source error and immutable evidence.
func cloneStreamObservation(observation StreamObservation) (StreamObservation, error) {
	cloned := observation
	var err error
	if observation.Chunk != nil {
		cloned.Chunk, err = cloneChunk(observation.Chunk)
		if err != nil {
			return StreamObservation{}, err
		}
	}
	cloned.Response, err = cloneResponseForValidationUnchecked(observation.Response)
	if err != nil {
		return StreamObservation{}, err
	}
	if observation.RejectedUsageDelta != nil {
		usage := *observation.RejectedUsageDelta
		cloned.RejectedUsageDelta = &usage
	}
	if observation.RejectedUsageTotal != nil {
		usage := *observation.RejectedUsageTotal
		cloned.RejectedUsageTotal = &usage
	}
	return cloned, nil
}

// joinResultErrors retains an exact source error when every observer succeeds.
func joinResultErrors(source error, observer string, results []modelcall.Result) error {
	err := source
	for _, result := range results {
		if result.Err == nil {
			continue
		}
		if err == nil {
			err = fmt.Errorf("%s failed: %w", observer, result.Err)
		} else {
			err = errors.Join(err, result.Err)
		}
	}
	return err
}

// observeRecv invokes one receive callback with its private copy of the frozen
// source observation. Its error is recorded without changing later inputs.
func (s *observedValidatedStreamer) observeRecv(observation StreamObservation) error {
	return s.observer.ObserveStreamRecv(observation)
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

// observeClose invokes one close callback with the unchanged provider result.
func (s *observedValidatedStreamer) observeClose(providerErr error) error {
	return s.observer.ObserveStreamClose(providerErr)
}

// failValidation latches the first stream contract failure.
func (s *validatedStreamer) failValidation(err error) error {
	s.pending = nil
	s.finished = true
	validationErr := newOutputValidationError(
		err,
		s.responseEvidence,
		s.rejected,
		firstTokenUsage(s.rejectedTotal, s.rejectedDelta),
	)
	if validationErr.RecoveryCorrection() != "" {
		s.rejected = nil
	}
	s.terminalErr = validationErr
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
