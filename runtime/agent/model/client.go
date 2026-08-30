// Package model owns the only client implementation that exposes provider
// output to planners and runtimes. Raw providers and provider-side middleware
// remain extensible, while every consumer-facing client applies one immutable
// request contract before returning a response or stream.
package model

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"

	"goa.design/goa-ai/runtime/agent/internal/modelcall"
)

type (
	// validatedClient applies the canonical request and output contract around
	// one raw provider.
	validatedClient struct {
		provider  Provider
		counter   TokenCounter
		observers []ProviderCallObserver
	}

	// clientCore identifies package-owned Client implementations and gives
	// WrapClient access to their raw provider without exporting an escape hatch.
	clientCore interface {
		Client
		rawProvider() Provider
		callObservers() []ProviderCallObserver
	}

	// ProviderCallObserver lets provider-side middleware prepare call-scoped
	// work before provider execution and observe only the final validated
	// result. NewClient owns invocation of these hooks.
	ProviderCallObserver interface {
		PrepareClientCall(context.Context, *Request) (context.Context, ClientCallObserver, error)
	}

	// ClientCallObserver receives one canonical result from the opaque Client.
	// Complete receives either a validated response or an error. Stream receives
	// the prepared call result and may return recording behavior that Client
	// attaches to the exact validated stream. Observers never receive the stream
	// itself. Every observer receives the same provider or validation result;
	// one observer's error is recorded without changing later observer input.
	// Every prepared Finish receives the same result collected before any
	// finisher ran. Every prepared observer receives exactly one Finish or
	// Abort call.
	ClientCallObserver interface {
		ObserveClientComplete(*Response, error) error
		ObserveClientStream(error) (StreamObserver, error)
		Finish(error) error
		Abort(error) error
	}

	// clientCallStreamObserver binds one prepared call lifecycle to the exact
	// validated stream observer returned for that call.
	clientCallStreamObserver struct {
		call     ClientCallObserver
		observer StreamObserver
	}

	// clientCallLifecycle collects every callback result and invokes the one
	// runtime finalizer after all prepared Finish callbacks have run.
	clientCallLifecycle struct {
		ctx            context.Context
		calls          []ClientCallObserver
		finalizer      modelcall.Finalizer
		finalizerIndex int
		outcome        modelcall.Outcome
	}
)

// ErrTokenCountingUnsupported reports that a provider has no native token
// counting operation.
var ErrTokenCountingUnsupported = errors.New("model provider does not support token counting")

// NewClient validates provider and returns the opaque client that owns canonical
// request, response, and stream validation. CountTokens validates its request
// and delegates when provider implements TokenCounter; otherwise it returns
// ErrTokenCountingUnsupported.
func NewClient(provider Provider) (Client, error) {
	if err := ValidateProvider(provider); err != nil {
		return nil, err
	}
	var observers []ProviderCallObserver
	if observer, ok := provider.(ProviderCallObserver); ok && !isNilInterface(observer) {
		observers = []ProviderCallObserver{observer}
	}
	counter, _ := provider.(TokenCounter)
	return newValidatedClient(provider, counter, observers)
}

// newValidatedClient builds the package-owned client after its provider and
// ordered middleware observers have been checked.
func newValidatedClient(
	provider Provider,
	counter TokenCounter,
	observers []ProviderCallObserver,
) (Client, error) {
	client := &validatedClient{
		provider:  provider,
		counter:   counter,
		observers: observers,
	}
	return client, nil
}

// WrapClient installs provider-side middleware beneath the canonical validation
// boundary of client and returns a newly validated Client. Middleware sees raw
// provider responses and streams; callers of the returned Client never do.
func WrapClient(client Client, wrap func(Provider) Provider) (Client, error) {
	core, err := validatedClientCore(client)
	if err != nil {
		return nil, err
	}
	if wrap == nil {
		return nil, errors.New("model provider middleware is required")
	}
	provider := wrap(core.rawProvider())
	if err := ValidateProvider(provider); err != nil {
		return nil, err
	}
	observers := slices.Clone(core.callObservers())
	if observer, ok := provider.(ProviderCallObserver); ok && !isNilInterface(observer) {
		observers = append(observers, observer)
	}
	counter, _ := provider.(TokenCounter)
	return newValidatedClient(provider, counter, observers)
}

// ValidateClient rejects nil, typed-nil, or forged Client implementations.
// Package model is the sole owner of valid Client implementations.
func ValidateClient(client Client) error {
	_, err := validatedClientCore(client)
	return err
}

// ValidateProvider rejects nil and typed-nil raw providers.
func ValidateProvider(provider Provider) error {
	if isNilInterface(provider) {
		return errors.New("model provider is required")
	}
	return nil
}

// Complete owns one request before observers or the raw provider can inspect
// it, then validates the translated response against that exact snapshot.
func (c *validatedClient) Complete(ctx context.Context, req *Request) (*Response, error) {
	request, err := cloneRequest(req)
	if err != nil {
		return nil, err
	}
	contract, err := newRequestContract(request)
	if err != nil {
		return nil, err
	}
	ctx, observers, err := c.prepareClientCall(ctx, request)
	if err != nil {
		return nil, err
	}
	lifecycle, err := newClientCallLifecycle(ctx, observers)
	if err != nil {
		return nil, abortClientCalls(ctx, observers, err)
	}
	request = preparedRequest(request, contract)
	response, providerErr := c.provider.Complete(ctx, request)
	var validationErr error
	switch {
	case isExactOutputValidation(providerErr):
		validationErr = providerErr
		providerErr = nil
		response = nil
	case providerErr == nil:
		response, validationErr = contract.ValidateResponse(response)
	default:
		response = nil
	}
	lifecycle.outcome.ProviderCall = modelcall.Result{Called: true, Err: providerErr}
	lifecycle.outcome.Validations = []modelcall.Result{{
		Called: providerErr == nil,
		Err:    validationErr,
	}}
	sourceErr := joinClientErrors(providerErr, validationErr)
	lifecycle.outcome.CompletionObservers = make([]modelcall.Result, len(observers))
	observedResponses := make([]*Response, len(observers))
	var cloneErr error
	for i := range observers {
		observedResponses[i], err = CloneResponse(response)
		cloneErr = errors.Join(cloneErr, err)
	}
	if cloneErr != nil {
		lifecycle.outcome.Framework = modelcall.Result{Called: true, Err: cloneErr}
		sourceErr = errors.Join(sourceErr, cloneErr)
	}
	for i, observer := range observers {
		observerErr := observer.ObserveClientComplete(observedResponses[i], sourceErr)
		lifecycle.outcome.CompletionObservers[i] = modelcall.Result{
			Called: true,
			Err:    observerErr,
		}
	}
	lifecycle.outcome.Context = contextResult(ctx)
	err = lifecycle.finalize()
	if err != nil {
		return nil, err
	}
	return response, nil
}

// Stream owns one request before observer or provider work, closes any partial
// stream returned with an error, and exposes only the validated stream. After
// Stream succeeds, the caller receives through cancellation or completion and
// then closes or finalizes the stream.
func (c *validatedClient) Stream(ctx context.Context, req *Request) (*ValidatedStream, error) {
	request, err := cloneRequest(req)
	if err != nil {
		return nil, err
	}
	contract, err := newRequestContract(request)
	if err != nil {
		return nil, err
	}
	ctx, observers, err := c.prepareClientCall(ctx, request)
	if err != nil {
		return nil, err
	}
	lifecycle, err := newClientCallLifecycle(ctx, observers)
	if err != nil {
		return nil, abortClientCalls(ctx, observers, err)
	}
	request = preparedRequest(request, contract)
	stream, err := c.provider.Stream(ctx, request)
	lifecycle.outcome.ProviderCall = modelcall.Result{Called: true, Err: err}
	if err != nil {
		if !isNilStreamer(stream) {
			closeErr := stream.Close()
			lifecycle.outcome.ProviderClose = modelcall.Result{Called: true, Err: closeErr}
			err = joinClientErrors(err, closeErr)
		}
		return c.observeClientStream(ctx, nil, err, lifecycle)
	}
	validated, err := contract.ValidateStream(stream)
	if err != nil {
		lifecycle.outcome.Validations = []modelcall.Result{{Called: true, Err: err}}
		if !isNilStreamer(stream) {
			closeErr := stream.Close()
			lifecycle.outcome.ProviderClose = modelcall.Result{Called: true, Err: closeErr}
			err = joinClientErrors(err, closeErr)
		}
		return c.observeClientStream(ctx, nil, err, lifecycle)
	}
	return c.observeClientStream(ctx, validated, nil, lifecycle)
}

// CountTokens validates the provider input contract before forwarding the
// optional raw counting capability.
func (c *validatedClient) CountTokens(ctx context.Context, req *Request) (TokenCount, error) {
	request, err := cloneRequest(req)
	if err != nil {
		return TokenCount{}, err
	}
	contract, err := newRequestContract(request)
	if err != nil {
		return TokenCount{}, err
	}
	if isNilInterface(c.counter) {
		return TokenCount{}, ErrTokenCountingUnsupported
	}
	request = preparedRequest(request, contract)
	count, err := c.counter.CountTokens(ctx, request)
	if err != nil {
		return TokenCount{}, err
	}
	if count.InputTokens < 0 {
		return TokenCount{}, errors.New("model provider returned a negative input token count")
	}
	if err := validateTokenUsageModel(count.Model); err != nil {
		return TokenCount{}, fmt.Errorf("model provider returned an invalid token-count model: %w", err)
	}
	if count.ModelClass != contract.stream.modelClass {
		return TokenCount{}, errors.New("model provider returned a token count for the wrong model class")
	}
	if count.Exact && count.Model == "" {
		return TokenCount{}, errors.New("model provider returned an exact token count without a model identifier")
	}
	return count, nil
}

func (*validatedClient) validatedModelClient() {}

// rawProvider returns the provider chain immediately below canonical
// validation. Only package model can call this method.
func (c *validatedClient) rawProvider() Provider {
	return c.provider
}

// callObservers returns the middleware hooks in inner-to-outer order.
func (c *validatedClient) callObservers() []ProviderCallObserver {
	return c.observers
}

// prepareClientCall runs outer middleware first and returns completed hooks in
// inner-to-outer order for final-result observation.
func (c *validatedClient) prepareClientCall(
	ctx context.Context,
	req *Request,
) (context.Context, []ClientCallObserver, error) {
	observers := make([]ClientCallObserver, 0, len(c.observers))
	for i := len(c.observers) - 1; i >= 0; i-- {
		observedRequest, err := cloneRequest(req)
		if err != nil {
			return ctx, nil, abortClientCalls(ctx, observers, err)
		}
		nextCtx, observer, err := c.observers[i].PrepareClientCall(ctx, observedRequest)
		if err != nil {
			return ctx, nil, abortClientCalls(ctx, observers, err)
		}
		ctx = nextCtx
		if observer != nil {
			observers = append(observers, observer)
		}
	}
	slices.Reverse(observers)
	return ctx, observers, nil
}

// abortClientCalls gives every prepared call the same setup failure, records
// each abort independently, and invokes an internal finalizer last when one was
// already prepared.
func abortClientCalls(ctx context.Context, calls []ClientCallObserver, setupErr error) error {
	results := make([]modelcall.Result, len(calls))
	for i := len(calls) - 1; i >= 0; i-- {
		results[i] = modelcall.Result{Called: true, Err: calls[i].Abort(setupErr)}
	}
	outcome := modelcall.Outcome{
		ProviderCall: modelcall.Result{Called: true, Err: setupErr},
		Aborts:       results,
		Context:      contextResult(ctx),
	}
	var finalizerErr error
	for i, call := range calls {
		finalizer, ok := call.(modelcall.Finalizer)
		if !ok {
			continue
		}
		outcome.HasFinalizer = true
		outcome.FinalizerIndex = i
		finalizerErr = errors.Join(finalizerErr, finalizer.FinalizeModelCall(outcome.Clone()))
	}
	return joinClientErrors(outcome.Error(), finalizerErr)
}

// observeClientStream gathers additive observation results, then attaches every
// returned stream observer itself. A failed setup finishes all prepared calls
// and never exposes the stream.
func (c *validatedClient) observeClientStream(
	ctx context.Context,
	stream *ValidatedStream,
	err error,
	lifecycle *clientCallLifecycle,
) (*ValidatedStream, error) {
	streamObservers := make([]StreamObserver, len(lifecycle.calls))
	lifecycle.outcome.StreamSetupObservers = make([]modelcall.Result, len(lifecycle.calls))
	for i, observer := range lifecycle.calls {
		streamObserver, observerErr := observer.ObserveClientStream(err)
		streamObservers[i] = streamObserver
		lifecycle.outcome.StreamSetupObservers[i] = modelcall.Result{Called: true, Err: observerErr}
	}
	setupErr := joinClientErrors(err, modelcallResultsError(lifecycle.outcome.StreamSetupObservers))
	if stream == nil || setupErr != nil {
		if stream != nil {
			closeErr := stream.Close()
			lifecycle.outcome.ProviderClose = modelcall.Result{Called: true, Err: closeErr}
		}
		lifecycle.outcome.Context = contextResult(ctx)
		return nil, lifecycle.finalize()
	}

	for i, observer := range lifecycle.calls {
		callObserver := &clientCallStreamObserver{
			call:     observer,
			observer: streamObservers[i],
		}
		observed, observeErr := stream.Observe(callObserver)
		if observeErr != nil {
			lifecycle.outcome.Framework = modelcall.Result{Called: true, Err: observeErr}
			break
		}
		stream = observed
	}
	if lifecycle.outcome.Framework.Err != nil {
		closeErr := stream.Close()
		lifecycle.outcome.ProviderClose = modelcall.Result{Called: true, Err: closeErr}
		lifecycle.outcome.Context = contextResult(ctx)
		return nil, lifecycle.finalize()
	}
	stream.registerClientCallLifecycle(lifecycle)
	return stream, nil
}

// newClientCallLifecycle finds the one internal runtime finalizer among the
// prepared calls. More than one finalizer would make call ownership ambiguous.
func newClientCallLifecycle(ctx context.Context, calls []ClientCallObserver) (*clientCallLifecycle, error) {
	lifecycle := &clientCallLifecycle{ctx: ctx, calls: calls}
	for i, call := range calls {
		finalizer, ok := call.(modelcall.Finalizer)
		if !ok {
			continue
		}
		if lifecycle.finalizer != nil {
			return nil, errors.New("model call has multiple runtime finalizers")
		}
		lifecycle.finalizer = finalizer
		lifecycle.finalizerIndex = i
		lifecycle.outcome.HasFinalizer = true
		lifecycle.outcome.FinalizerIndex = i
	}
	return lifecycle, nil
}

// finalize gives every prepared call the same frozen pre-finisher error,
// records every returned result, and invokes the runtime finalizer last.
func (l *clientCallLifecycle) finalize() error {
	finalizerErr := l.runFinishers()
	return joinClientErrors(l.outcome.Error(), finalizerErr)
}

// finalizeClose runs every finisher and returns the complete private phase
// record alongside Close's existing combined error.
func (l *clientCallLifecycle) finalizeClose() validatedStreamCloseResult {
	finalizerErr := l.runFinishers()
	closeOutcome := modelcall.Outcome{
		ProviderClose: l.outcome.ProviderClose,
		CloseObservers: append(
			[]modelcall.Result(nil),
			l.outcome.CloseObservers...,
		),
		Finishers: append([]modelcall.Result(nil), l.outcome.Finishers...),
		Context:   l.outcome.Context,
		Framework: l.outcome.Framework,
	}
	return validatedStreamCloseResult{
		outcome:      l.outcome.Clone(),
		finalizerErr: finalizerErr,
		err:          joinClientErrors(closeOutcome.Error(), finalizerErr),
	}
}

// runFinishers records every prepared Finish result against one frozen input,
// then invokes the internal finalizer with the complete outcome.
func (l *clientCallLifecycle) runFinishers() error {
	l.outcome.Finishers = make([]modelcall.Result, len(l.calls))
	preFinishErr := l.outcome.Error()
	for i, call := range l.calls {
		l.outcome.Finishers[i] = modelcall.Result{
			Called: true,
			Err:    call.Finish(preFinishErr),
		}
	}
	l.outcome.Context = contextResult(l.ctx)
	if err := l.outcome.ValidateFinalized(); err != nil {
		l.outcome.Framework = modelcall.Result{Called: true, Err: err}
	}
	var finalizerErr error
	if l.finalizer != nil {
		finalizerErr = l.finalizer.FinalizeModelCall(l.outcome.Clone())
	}
	l.outcome.Context = contextResult(l.ctx)
	return finalizerErr
}

// contextResult records cancellation or deadline state without changing a
// callback's frozen phase input.
func contextResult(ctx context.Context) modelcall.Result {
	return modelcall.Result{Called: true, Err: ctx.Err()}
}

// modelcallResultsError derives callback errors for outward setup handling.
func modelcallResultsError(results []modelcall.Result) error {
	var err error
	for _, result := range results {
		err = joinClientErrors(err, result.Err)
	}
	return err
}

// joinClientErrors preserves an exact lone phase error and joins distinct
// failures only when more than one operation failed.
func joinClientErrors(errs ...error) error {
	var joined error
	for _, err := range errs {
		if err == nil {
			continue
		}
		if joined == nil {
			joined = err
			continue
		}
		joined = errors.Join(joined, err)
	}
	return joined
}

// isExactOutputValidation reports whether every error leaf resolves to one
// exact OutputValidationError. Unary provider failures use this classification
// so an unrelated error can never be reclassified or discarded.
func isExactOutputValidation(err error) bool {
	_, ok := exactOutputValidation(err)
	return ok
}

// ObserveStreamRecv delegates safe stream facts without changing the validated
// chunk or error passed to later observers.
func (o *clientCallStreamObserver) ObserveStreamRecv(observation StreamObservation) error {
	if o.observer == nil {
		return nil
	}
	return o.observer.ObserveStreamRecv(observation)
}

// ObserveStreamClose delegates the close result. ValidatedStream.Close finishes
// the prepared call only after every observer has returned.
func (o *clientCallStreamObserver) ObserveStreamClose(err error) error {
	if o.observer == nil {
		return nil
	}
	return o.observer.ObserveStreamClose(err)
}

// validatedClientCore checks the opaque client invariant before another public
// boundary accepts a Client.
func validatedClientCore(client Client) (clientCore, error) {
	if isNilInterface(client) {
		return nil, errors.New("model client is required")
	}
	core, ok := client.(clientCore)
	if !ok || isNilInterface(core) || isNilInterface(core.rawProvider()) {
		return nil, errors.New("model client is not an intact validated client")
	}
	return core, nil
}

// isNilInterface detects typed nil pointers and other nil-capable interface
// values without calling methods on them.
func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	kind := reflected.Kind()
	nilable := kind == reflect.Chan ||
		kind == reflect.Func ||
		kind == reflect.Interface ||
		kind == reflect.Map ||
		kind == reflect.Pointer ||
		kind == reflect.Slice
	return nilable && reflected.IsNil()
}

var (
	_ Client       = (*validatedClient)(nil)
	_ TokenCounter = (*validatedClient)(nil)
)
