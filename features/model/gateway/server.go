package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// Server adapts a model.Client into a composable request handler with
	// middleware support for both unary and streaming completions.
	//
	// Applications typically instantiate a Server with NewServer, configure it
	// with a provider client (WithProvider), and optionally add middleware chains
	// (WithUnary, WithStream) for cross-cutting concerns such as logging, metrics,
	// rate limiting, or request transformation. The resulting Server exposes
	// Complete and Stream methods that Goa service implementations can call.
	//
	// Middleware is applied in registration order: the first middleware registered
	// wraps all subsequent ones, forming an onion structure where the innermost
	// layer invokes the provider client.
	Server struct {
		provider model.Client
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
	// from send will abort the stream. A successful handler returns the canonical
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
		provider model.Client
		unaryMW  []UnaryMiddleware
		streamMW []StreamMiddleware
	}

	// streamValidator enforces request-wide chunk ordering and terminal
	// agreement for one provider or middleware stream boundary.
	streamValidator struct {
		stopped            bool
		stopReason         string
		completed          bool
		completionRequired bool
		toolCallIDs        map[string]struct{}
		toolCalls          []model.ToolCall
		usage              model.TokenUsage
		sawUsage           bool
	}
)

// WithProvider returns an Option that sets the underlying model client used
// by the Server to fulfill completion requests. This option is required;
// NewServer will return ErrProviderRequired if no provider is configured.
// The provider's Complete and Stream methods form the innermost layer of the
// middleware chain.
func WithProvider(p model.Client) Option {
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
// allows early middleware to observe and transform both requests and responses
// while later middleware operate closer to the provider.
func NewServer(opts ...Option) (*Server, error) {
	var cfg serverConfig
	for _, o := range opts {
		o(&cfg)
	}
	if cfg.provider == nil {
		return nil, ErrProviderRequired
	}
	// Base handlers call the provider directly.
	baseUnary := func(ctx context.Context, req *model.Request) (*model.Response, error) {
		response, err := cfg.provider.Complete(ctx, req)
		if err != nil {
			return nil, err
		}
		if err := model.ValidateResponse(response); err != nil {
			return nil, errors.Join(errors.New("gateway: provider returned invalid canonical response"), err)
		}
		return response, nil
	}
	baseStream := func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
		st, err := cfg.provider.Stream(ctx, req)
		if err != nil {
			return nil, err
		}
		validator := streamValidator{completionRequired: req.StructuredOutput != nil}
		for {
			ch, err := st.Recv()
			if err != nil {
				if errors.Is(err, io.EOF) {
					response := st.Response()
					if response == nil {
						return nil, errors.Join(
							errors.New("gateway: stream ended without canonical response"),
							st.Close(),
						)
					}
					responseErr := model.ValidateResponse(response)
					streamErr := validator.finish(response)
					closeErr := st.Close()
					if responseErr != nil || streamErr != nil {
						return nil, errors.Join(
							errors.New("gateway: provider returned invalid canonical response"),
							responseErr,
							streamErr,
							closeErr,
						)
					}
					return response, closeErr
				}
				return nil, errors.Join(err, st.Close())
			}
			if err := validator.accept(ch); err != nil {
				return nil, errors.Join(
					errors.New("gateway: provider returned invalid stream chunk"),
					err,
					st.Close(),
				)
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
// timeouts, and request-scoped values.
func (s *Server) Complete(ctx context.Context, req *model.Request) (*model.Response, error) {
	response, err := s.unary(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := model.ValidateResponse(response); err != nil {
		return nil, errors.Join(errors.New("gateway: invalid canonical response"), err)
	}
	return response, nil
}

// Stream processes a streaming model completion request through the configured
// middleware chain, invoking send for each chunk produced. The send callback
// must be called sequentially; returning an error from send or from any
// middleware aborts the stream. A successful call returns the canonical
// provider response separately from the presentation chunks. The context is
// propagated through the chain and controls the lifetime of the stream.
func (s *Server) Stream(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
	var sendErr error
	validator := streamValidator{completionRequired: req.StructuredOutput != nil}
	validatedSend := func(chunk model.Chunk) error {
		if sendErr != nil {
			return sendErr
		}
		if err := validator.accept(chunk); err != nil {
			sendErr = errors.Join(errors.New("gateway: invalid stream chunk"), err)
			return sendErr
		}
		sendErr = send(chunk)
		return sendErr
	}
	response, err := s.stream(ctx, req, validatedSend)
	if err != nil {
		return nil, err
	}
	if sendErr != nil {
		return nil, sendErr
	}
	if err := model.ValidateResponse(response); err != nil {
		return nil, errors.Join(errors.New("gateway: invalid canonical response"), err)
	}
	if err := validator.finish(response); err != nil {
		return nil, errors.Join(errors.New("gateway: invalid stream sequence"), err)
	}
	return response, nil
}

// accept validates one chunk and advances the stream state only after the
// chunk satisfies the provider-neutral sequencing contract.
func (v *streamValidator) accept(chunk model.Chunk) error {
	if err := model.ValidateChunk(chunk); err != nil {
		return err
	}
	if v.stopped {
		return fmt.Errorf("stream emitted %q after stop", chunk.Kind())
	}
	switch actual := chunk.(type) {
	case model.ToolCallChunk:
		if v.toolCallIDs == nil {
			v.toolCallIDs = make(map[string]struct{})
		}
		if _, exists := v.toolCallIDs[actual.ToolCall.ID]; exists {
			return fmt.Errorf("stream repeated finalized tool call %q", actual.ToolCall.ID)
		}
		v.toolCallIDs[actual.ToolCall.ID] = struct{}{}
		v.toolCalls = append(v.toolCalls, actual.ToolCall)
	case model.ToolCallDeltaChunk:
		if _, finalized := v.toolCallIDs[actual.Delta.ID]; finalized {
			return fmt.Errorf("stream emitted tool call delta after finalized call %q", actual.Delta.ID)
		}
	case model.CompletionChunk:
		if v.completed {
			return errors.New("stream emitted multiple canonical completion chunks")
		}
		v.completed = true
	case model.CompletionDeltaChunk:
		if v.completed {
			return errors.New("stream emitted completion delta after canonical completion")
		}
	case model.UsageChunk:
		v.sawUsage = true
		v.usage = addUsage(v.usage, actual.Usage)
	case model.StopChunk:
		v.stopped = true
		v.stopReason = actual.Reason
	}
	return nil
}

// finish verifies that the terminal response agrees with all identity-bearing
// chunks accepted at this boundary.
func (v *streamValidator) finish(response *model.Response) error {
	if !v.stopped {
		return errors.New("stream ended without stop chunk")
	}
	if v.completionRequired && !v.completed {
		return errors.New("structured-output stream ended without canonical completion chunk")
	}
	if response.StopReason != v.stopReason {
		return fmt.Errorf(
			"stream stop reason %q does not match canonical response %q",
			v.stopReason,
			response.StopReason,
		)
	}
	responseCalls := response.ToolCalls()
	if len(responseCalls) != len(v.toolCalls) {
		return fmt.Errorf(
			"stream emitted %d tool calls but canonical response contains %d",
			len(v.toolCalls),
			len(responseCalls),
		)
	}
	for index, responseCall := range responseCalls {
		streamCall := v.toolCalls[index]
		if responseCall.ID != streamCall.ID ||
			responseCall.Name != streamCall.Name ||
			!bytes.Equal(responseCall.Payload, streamCall.Payload) ||
			responseCall.ThoughtSignature != streamCall.ThoughtSignature {
			return fmt.Errorf("stream tool call %d does not match canonical response", index)
		}
	}
	if v.sawUsage && response.Usage != v.usage {
		return errors.New("stream usage deltas do not match canonical response usage")
	}
	return nil
}

// addUsage sums token deltas while preserving provider attribution.
func addUsage(current, delta model.TokenUsage) model.TokenUsage {
	if current.Model == "" {
		current.Model = delta.Model
	}
	if current.ModelClass == "" {
		current.ModelClass = delta.ModelClass
	}
	current.InputTokens += delta.InputTokens
	current.OutputTokens += delta.OutputTokens
	current.TotalTokens += delta.TotalTokens
	current.CacheReadTokens += delta.CacheReadTokens
	current.CacheWriteTokens += delta.CacheWriteTokens
	return current
}
