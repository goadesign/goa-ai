package gateway

import (
	"context"
	"errors"
	"io"
	"reflect"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// Server adapts a raw model.Provider into a composable transport handler with
	// middleware support for both unary and streaming completions. CountTokens
	// is available when the configured provider implements model.TokenCounter.
	//
	// Applications typically instantiate a Server with NewServer, configure it
	// with a provider client (WithProvider), and optionally add middleware chains
	// (WithUnary, WithStream) for cross-cutting concerns such as logging, metrics,
	// rate limiting, or request transformation. Middleware runs before the
	// caller's final model.Client validation boundary, so it may inspect or
	// transform raw provider output. Complete and Stream are deliberately raw
	// provider operations; callers that require canonical output use
	// NewRemoteClient on the consuming side.
	//
	// Middleware is applied in registration order: the first middleware registered
	// wraps all subsequent ones, forming an onion structure where the innermost
	// layer invokes the provider client.
	Server struct {
		provider model.Provider
		unary    UnaryHandler
		stream   StreamHandler
	}

	// UnaryHandler processes a single unary model completion request and returns
	// the complete response. Implementations receive the request context and a
	// *model.Request, and must return a *model.Response or an error. This
	// signature is used both by the base provider handler and by middleware that
	// compose additional behavior around it.
	UnaryHandler func(ctx context.Context, req *model.Request) (*model.Response, error)

	// StreamHandler processes a streaming model completion request by invoking
	// the provided send callback for each chunk produced by the model. The send
	// function must be called sequentially for each chunk; returning an error
	// from send will abort the stream. A successful handler returns the raw
	// provider response after all chunks have been sent. Implementations are
	// responsible for managing the underlying stream lifecycle, including
	// cleanup on errors.
	StreamHandler func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error)

	// UnaryMiddleware wraps a UnaryHandler to add behavior before, after, or
	// around the handler invocation. Middleware receives the next handler in
	// the chain and returns a new handler that typically calls next after
	// performing setup, or delegates to next conditionally. Common uses include
	// logging, metrics, retries, request validation, and response transformation.
	UnaryMiddleware func(next UnaryHandler) UnaryHandler

	// StreamMiddleware wraps a StreamHandler to add behavior around streaming
	// completions. Middleware receives the next handler and returns a new handler
	// that can intercept or transform chunks via the send callback, add logging
	// or telemetry, implement backpressure, or handle errors. The middleware must
	// preserve the sequential semantics of the send function.
	StreamMiddleware func(next StreamHandler) StreamHandler

	// Option configures a Server during construction. Options are applied in the
	// order they are passed to NewServer. Use WithProvider to set the underlying
	// model client, and WithUnary or WithStream to register middleware chains.
	Option func(*serverConfig)

	// serverConfig holds the configuration accumulated during Server construction.
	serverConfig struct {
		provider model.Provider
		unaryMW  []UnaryMiddleware
		streamMW []StreamMiddleware
	}
)

// WithProvider returns an Option that sets the underlying raw provider used
// by the Server to fulfill completion requests. This option is required;
// NewServer will return ErrProviderRequired if no provider is configured.
// The provider's Complete and Stream methods form the innermost layer of the
// middleware chain.
func WithProvider(p model.Provider) Option {
	return func(c *serverConfig) { c.provider = p }
}

// WithUnary returns an Option that appends one or more UnaryMiddleware to the
// Server's unary completion chain. Middleware are applied in the order they
// are registered across all WithUnary calls, with the first middleware forming
// the outermost layer. Each middleware wraps the next, allowing pre-processing,
// post-processing, and conditional delegation.
func WithUnary(mw ...UnaryMiddleware) Option {
	return func(c *serverConfig) { c.unaryMW = append(c.unaryMW, mw...) }
}

// WithStream returns an Option that appends one or more StreamMiddleware to the
// Server's streaming completion chain. Middleware are applied in the order they
// are registered across all WithStream calls, with the first middleware forming
// the outermost layer. Each middleware can intercept chunks, add telemetry,
// implement backpressure, or handle errors.
func WithStream(mw ...StreamMiddleware) Option {
	return func(c *serverConfig) { c.streamMW = append(c.streamMW, mw...) }
}

// NewServer constructs a Server with the provided options. The resulting Server
// has no built-in policy; all behavior is composed via middleware registered
// through WithUnary and WithStream. A provider client must be configured via
// WithProvider or NewServer returns ErrProviderRequired.
//
// Middleware chains are built during construction and applied in registration
// order: the first registered middleware becomes the outermost layer, wrapping
// all subsequent middleware and eventually the base provider handler. This
// allows early middleware to observe requests and responses while later
// middleware operate closer to the provider. A middleware may change transport
// details, but it must preserve the caller's original output contract: returned
// tool calls, structured output, and model class are validated against the
// request the gateway client sent.
func NewServer(opts ...Option) (*Server, error) {
	var cfg serverConfig
	for _, o := range opts {
		o(&cfg)
	}
	if err := model.ValidateProvider(cfg.provider); err != nil {
		return nil, ErrProviderRequired
	}
	// Base handlers call the provider directly.
	baseUnary := func(ctx context.Context, req *model.Request) (*model.Response, error) {
		response, err := cfg.provider.Complete(ctx, req)
		if err != nil {
			return nil, err
		}
		return response, nil
	}
	baseStream := func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
		st, err := cfg.provider.Stream(ctx, req)
		if err != nil {
			if !isNilStreamer(st) {
				err = errors.Join(err, st.Close())
			}
			return nil, err
		}
		if isNilStreamer(st) {
			return nil, errors.New("gateway: provider returned a nil or typed nil stream")
		}
		for {
			ch, err := st.Recv()
			if err != nil {
				// Only literal EOF completes a model stream. A wrapped EOF reports
				// the provider failure that added the wrapper.
				//nolint:errorlint // Exact equality is required by the model stream contract.
				if err == io.EOF {
					return st.Response(), st.Close()
				}
				return nil, errors.Join(err, st.Close())
			}
			if err := send(ch); err != nil {
				return nil, errors.Join(err, st.Close())
			}
		}
	}
	// Wrap with middlewares (in registration order).
	unary := baseUnary
	for i := len(cfg.unaryMW) - 1; i >= 0; i-- {
		unary = cfg.unaryMW[i](unary)
	}
	stream := baseStream
	for i := len(cfg.streamMW) - 1; i >= 0; i-- {
		stream = cfg.streamMW[i](stream)
	}
	return &Server{provider: cfg.provider, unary: unary, stream: stream}, nil
}

// Complete processes a unary model completion request through the configured
// middleware chain and returns the complete response. The request flows through
// all registered UnaryMiddleware in order before reaching the provider client.
// The context is propagated through the chain and can be used for cancellation,
// timeouts, and request-scoped values. The request shape is checked before the
// chain runs, but the returned provider response is not canonicalized or output
// validated by Server.
func (s *Server) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	if _, err := model.NewRequestContract(req); err != nil {
		return nil, err
	}
	return s.unary(ctx, req)
}

// Stream processes a streaming model completion request through the configured
// middleware chain, invoking send for each chunk produced. The send callback
// must be called sequentially; returning an error from send or from any
// middleware aborts the stream. A successful call returns the raw provider
// response separately from the presentation chunks. Server checks the request
// shape but leaves chunk and response output validation to the consuming
// model.Client. The context controls the lifetime of the stream.
func (s *Server) Stream(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
	if send == nil {
		return nil, errors.New("gateway: stream send function is required")
	}
	if _, err := model.NewRequestContract(req); err != nil {
		return nil, err
	}
	return s.stream(ctx, req, send)
}

// CountTokens validates and forwards one exact count request when the
// configured provider supports native token counting.
func (s *Server) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	if _, err := model.NewRequestContract(req); err != nil {
		return model.TokenCount{}, err
	}
	counter, ok := s.provider.(model.TokenCounter)
	if !ok {
		return model.TokenCount{}, model.ErrTokenCountingUnsupported
	}
	return counter.CountTokens(ctx, req)
}

// isNilStreamer rejects typed nil stream implementations before the gateway
// invokes provider-owned methods.
func isNilStreamer(stream model.Streamer) bool {
	if stream == nil {
		return true
	}
	value := reflect.ValueOf(stream)
	kind := value.Kind()
	nilable := kind == reflect.Chan ||
		kind == reflect.Func ||
		kind == reflect.Interface ||
		kind == reflect.Map ||
		kind == reflect.Ptr ||
		kind == reflect.Slice
	return nilable && value.IsNil()
}
