// Package model owns the only client implementation that exposes provider
// output to planners and runtimes. Raw providers and provider-side middleware
// remain extensible, while every consumer-facing client applies one immutable
// request contract before returning a response or stream.
package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"slices"
	"sync"
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
	// itself. Observation errors are additive:
	// Client joins them with the provider or validation result. Every prepared
	// observer receives exactly one Finish or Abort call.
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

		mu          sync.Mutex
		terminalErr error
	}

	// contextStreamObserver lets Client close the exact validated stream when
	// the prepared provider context is canceled. Middleware receives no stream
	// capability and only observes safe stream facts.
	contextStreamObserver struct {
		done chan struct{}
		once sync.Once
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
	response, err := c.provider.Complete(ctx, request)
	if err == nil {
		response, err = contract.ValidateResponse(response)
	} else {
		response = nil
	}
	for _, observer := range observers {
		observed, cloneErr := CloneResponse(response)
		observedErr := errors.Join(err, cloneErr)
		err = errors.Join(err, cloneErr, observer.ObserveClientComplete(observed, observedErr))
	}
	err = finishClientCalls(observers, err)
	if err != nil {
		return nil, err
	}
	return response, nil
}

// Stream owns one request before observer or provider work, closes any partial
// stream returned with an error, and exposes only the validated stream.
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
	stream, err := c.provider.Stream(ctx, request)
	if err != nil {
		if !isNilStreamer(stream) {
			err = errors.Join(err, stream.Close())
		}
		return c.observeClientStream(ctx, nil, err, observers)
	}
	validated, err := contract.ValidateStream(stream)
	if err != nil {
		if !isNilStreamer(stream) {
			err = errors.Join(err, stream.Close())
		}
		return c.observeClientStream(ctx, nil, err, observers)
	}
	return c.observeClientStream(ctx, validated, nil, observers)
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
			for j := len(observers) - 1; j >= 0; j-- {
				err = errors.Join(err, observers[j].Abort(err))
			}
			return ctx, nil, err
		}
		nextCtx, observer, err := c.observers[i].PrepareClientCall(ctx, observedRequest)
		if err != nil {
			for j := len(observers) - 1; j >= 0; j-- {
				err = errors.Join(err, observers[j].Abort(err))
			}
			return ctx, nil, err
		}
		ctx = nextCtx
		if observer != nil {
			observers = append(observers, observer)
		}
	}
	slices.Reverse(observers)
	return ctx, observers, nil
}

// observeClientStream gathers additive observation results, then attaches every
// returned stream observer itself. A failed setup finishes all prepared calls
// and never exposes the stream.
func (c *validatedClient) observeClientStream(
	ctx context.Context,
	stream *ValidatedStream,
	err error,
	observers []ClientCallObserver,
) (*ValidatedStream, error) {
	streamObservers := make([]StreamObserver, len(observers))
	for i, observer := range observers {
		streamObserver, observerErr := observer.ObserveClientStream(err)
		streamObservers[i] = streamObserver
		err = errors.Join(err, observerErr)
	}
	if stream == nil || err != nil {
		if stream != nil {
			err = errors.Join(err, stream.Close())
		}
		return nil, finishClientCalls(observers, err)
	}

	attached := 0
	for i, observer := range observers {
		observed, observeErr := stream.Observe(&clientCallStreamObserver{
			call:     observer,
			observer: streamObservers[i],
		})
		if observeErr != nil {
			err = errors.Join(err, observeErr)
			break
		}
		stream = observed
		attached++
	}
	if err != nil {
		err = errors.Join(err, stream.Close())
		err = finishClientCalls(observers[attached:], err)
		return nil, err
	}
	if ctx.Done() != nil {
		lifecycle := &contextStreamObserver{done: make(chan struct{})}
		observed, observeErr := stream.Observe(lifecycle)
		if observeErr != nil {
			err = errors.Join(observeErr, stream.Close())
			return nil, err
		}
		stream = observed
		go closeStreamOnContext(ctx, stream, lifecycle.done)
	}
	return stream, nil
}

// closeStreamOnContext releases a stream whose prepared provider context was
// canceled before its caller closed it. Normal stream closure stops the waiter.
func closeStreamOnContext(ctx context.Context, stream *ValidatedStream, done <-chan struct{}) {
	select {
	case <-ctx.Done():
		_ = stream.Close()
	case <-done:
	}
}

// finishClientCalls closes prepared call lifecycles in inner-to-outer order.
// Each returned cleanup failure is additive to the terminal call result.
func finishClientCalls(observers []ClientCallObserver, err error) error {
	for _, observer := range observers {
		err = errors.Join(err, observer.Finish(err))
	}
	return err
}

// ObserveStreamRecv delegates safe stream facts and retains the terminal call
// error so Finish receives it when the caller closes the stream.
func (o *clientCallStreamObserver) ObserveStreamRecv(observation StreamObservation) error {
	var observerErr error
	if o.observer != nil {
		observerErr = o.observer.ObserveStreamRecv(observation)
	}
	terminalErr := observerErr
	if observation.Err != nil && !errors.Is(observation.Err, io.EOF) {
		terminalErr = errors.Join(observation.Err, terminalErr)
	}
	if terminalErr != nil {
		o.mu.Lock()
		if o.terminalErr == nil {
			o.terminalErr = terminalErr
		}
		o.mu.Unlock()
	}
	return observerErr
}

// ObserveStreamClose finishes the prepared call after its optional stream
// observer has seen the exact provider close result.
func (o *clientCallStreamObserver) ObserveStreamClose(err error) error {
	var observerErr error
	if o.observer != nil {
		observerErr = o.observer.ObserveStreamClose(err)
	}
	o.mu.Lock()
	terminalErr := errors.Join(o.terminalErr, err, observerErr)
	o.mu.Unlock()
	finishErr := o.call.Finish(terminalErr)
	return errors.Join(observerErr, finishErr)
}

// ObserveStreamRecv does not alter validated chunks.
func (*contextStreamObserver) ObserveStreamRecv(StreamObservation) error {
	return nil
}

// ObserveStreamClose stops the context cancellation waiter.
func (o *contextStreamObserver) ObserveStreamClose(error) error {
	o.once.Do(func() {
		close(o.done)
	})
	return nil
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
