package gateway

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	stubStreamer struct {
		response             *model.Response
		chunks               []model.Chunk
		recvErr              error
		closeErr             error
		index                int
		closed               bool
		responseClearedClose bool
	}

	stubProvider struct {
		response *model.Response
		streamer model.Streamer
	}

	countingProvider struct {
		calls    int
		countErr error
	}

	recordedSpanEvent struct {
		name  string
		attrs []attribute.KeyValue
	}

	recordingSpan struct {
		trace.Span
		events []recordedSpanEvent
	}
)

func (s *stubStreamer) Recv() (model.Chunk, error) {
	if s.index < len(s.chunks) {
		chunk := s.chunks[s.index]
		s.index++
		return chunk, nil
	}
	if s.recvErr != nil {
		return nil, s.recvErr
	}
	return nil, errors.New("eof")
}

func (s *stubStreamer) Close() error {
	s.closed = true
	return s.closeErr
}

func (s *stubStreamer) Response() *model.Response {
	if s.responseClearedClose && s.closed {
		return nil
	}
	return s.response
}

func (s *recordingSpan) AddEvent(name string, options ...trace.EventOption) {
	config := trace.NewEventConfig(options...)
	s.events = append(s.events, recordedSpanEvent{
		name:  name,
		attrs: config.Attributes(),
	})
}

func (p stubProvider) Complete(_ context.Context, req *model.Request) (*model.Response, error) {
	if p.response != nil {
		return p.response, nil
	}
	return &model.Response{
		Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
		StopReason: "done",
	}, nil
}
func (p stubProvider) Stream(_ context.Context, _ *model.Request) (model.Streamer, error) {
	if p.streamer != nil {
		return p.streamer, nil
	}
	return &stubStreamer{}, nil
}

func (p *countingProvider) Complete(context.Context, *model.Request) (*model.Response, error) {
	p.calls++
	return &model.Response{}, nil
}

func (p *countingProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	p.calls++
	return &stubStreamer{}, nil
}

func (p *countingProvider) CountTokens(_ context.Context, req *model.Request) (model.TokenCount, error) {
	p.calls++
	return model.TokenCount{
		Model:       req.Model,
		ModelClass:  req.ModelClass,
		InputTokens: 42,
		Exact:       true,
	}, p.countErr
}

func TestServerRejectsInvalidRequestBeforeProviderCall(t *testing.T) {
	provider := &countingProvider{}
	server, err := NewServer(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	response, err := server.Complete(t.Context(), nil)
	if response != nil || err == nil || !strings.Contains(err.Error(), "model request is required") {
		t.Fatalf("Complete response, error = %v, %v; want nil, request error", response, err)
	}
	response, err = server.Stream(t.Context(), nil, func(model.Chunk) error { return nil })
	if response != nil || err == nil || !strings.Contains(err.Error(), "model request is required") {
		t.Fatalf("Stream response, error = %v, %v; want nil, request error", response, err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

func TestNewServerRejectsTypedNilProvider(t *testing.T) {
	var provider *countingProvider

	server, err := NewServer(WithProvider(provider))

	if server != nil {
		t.Fatalf("server = %#v, want nil", server)
	}
	if !errors.Is(err, ErrProviderRequired) {
		t.Fatalf("NewServer error = %v, want ErrProviderRequired", err)
	}
}

func TestServerCountTokensPreservesOptionalProviderCapability(t *testing.T) {
	provider := &countingProvider{}
	server, err := NewServer(WithProvider(provider))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	count, err := server.CountTokens(t.Context(), &model.Request{
		Model:      "remote-model",
		ModelClass: model.ModelClassDefault,
	})

	if err != nil {
		t.Fatalf("CountTokens error: %v", err)
	}
	if count.InputTokens != 42 || !count.Exact || provider.calls != 1 {
		t.Fatalf("CountTokens result, calls = %#v, %d; want exact 42, 1", count, provider.calls)
	}
}

func TestServerCountTokensReportsUnsupportedProvider(t *testing.T) {
	server, err := NewServer(WithProvider(stubProvider{}))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	_, err = server.CountTokens(t.Context(), &model.Request{})

	if !errors.Is(err, model.ErrTokenCountingUnsupported) {
		t.Fatalf("CountTokens error = %v; want ErrTokenCountingUnsupported", err)
	}
}

func TestServerStreamRequiresSendBeforeMiddlewareOrProvider(t *testing.T) {
	provider := &countingProvider{}
	middlewareCalled := false
	server, err := NewServer(
		WithProvider(provider),
		WithStream(func(next StreamHandler) StreamHandler {
			return func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
				middlewareCalled = true
				return next(ctx, req, send)
			}
		}),
	)
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	response, err := server.Stream(t.Context(), nil, nil)

	if response != nil || err == nil || !strings.Contains(err.Error(), "send function is required") {
		t.Fatalf("Stream response, error = %v, %v; want nil, callback error", response, err)
	}
	if middlewareCalled {
		t.Fatal("stream middleware was invoked")
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d, want 0", provider.calls)
	}
}

func TestServerStreamRejectsTypedNilProviderStream(t *testing.T) {
	var upstream *stubStreamer
	server, err := NewServer(WithProvider(stubProvider{streamer: upstream}))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	response, err := server.Stream(t.Context(), &model.Request{}, func(model.Chunk) error {
		return nil
	})

	if response != nil || err == nil || !strings.Contains(err.Error(), "typed nil") {
		t.Fatalf("Stream response, error = %v, %v; want nil, typed-nil error", response, err)
	}
}

func TestNewServer_BuildsChains(t *testing.T) {
	prov := stubProvider{}
	calledUnary := false
	calledStream := false

	u := func(next UnaryHandler) UnaryHandler {
		return func(ctx context.Context, req *model.Request) (*model.Response, error) {
			calledUnary = true
			return next(ctx, req)
		}
	}
	s := func(next StreamHandler) StreamHandler {
		return func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
			calledStream = true
			return next(ctx, req, send)
		}
	}

	srv, err := NewServer(WithProvider(prov), WithUnary(u), WithStream(s))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	if _, err := srv.Complete(context.Background(), &model.Request{Model: "m"}); err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if _, err := srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error { return errors.New("eof") }); err == nil {
		t.Fatal("expected error from stream")
	}

	if !calledUnary {
		t.Fatal("unary middleware not invoked")
	}
	if !calledStream {
		t.Fatal("stream middleware not invoked")
	}
}

func TestServerStreamTreatsEOFAsSuccessAndPropagatesCloseFailure(t *testing.T) {
	tests := []struct {
		name     string
		closeErr error
	}{
		{name: "clean close"},
		{name: "close failure", closeErr: errors.New("close failed")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv, err := NewServer(WithProvider(stubProvider{streamer: &stubStreamer{
				recvErr:              io.EOF,
				closeErr:             test.closeErr,
				responseClearedClose: true,
				chunks:               []model.Chunk{model.StopChunk{Reason: "done"}},
				response: &model.Response{
					Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
					StopReason: "done",
				},
			}}))
			if err != nil {
				t.Fatalf("NewServer error: %v", err)
			}

			var sent int
			_, err = srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error {
				sent++
				return nil
			})

			if !errors.Is(err, test.closeErr) {
				t.Fatalf("stream error = %v, want %v", err, test.closeErr)
			}
			if sent != 1 {
				t.Fatalf("sent chunks = %d, want 1", sent)
			}
		})
	}
}

func TestServerStreamReportsWrappedEOFAsProviderFailure(t *testing.T) {
	wrappedEOF := fmt.Errorf("provider stream failed: %w", io.EOF)
	upstream := &stubStreamer{
		recvErr: wrappedEOF,
		response: &model.Response{
			Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "invalid"}}}},
			StopReason: "done",
		},
	}
	server, err := NewServer(WithProvider(stubProvider{streamer: upstream}))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	response, err := server.Stream(t.Context(), &model.Request{Model: "m"}, func(model.Chunk) error {
		return nil
	})

	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	if !errors.Is(err, wrappedEOF) {
		t.Fatalf("stream error = %v, want wrapped EOF failure", err)
	}
	if !upstream.closed {
		t.Fatal("provider stream was not closed")
	}
}

func TestServerCompleteReturnsResponseChangedByMiddleware(t *testing.T) {
	changesResponse := func(next UnaryHandler) UnaryHandler {
		return func(ctx context.Context, req *model.Request) (*model.Response, error) {
			response, err := next(ctx, req)
			if err != nil {
				return nil, err
			}
			response.Content[0].Parts[0] = model.TextPart{Text: `{"answer":42}`}
			return response, nil
		}
	}
	srv, err := NewServer(
		WithProvider(stubProvider{response: structuredGatewayResponse(`{"answer":"valid"}`)}),
		WithUnary(changesResponse),
	)
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}
	request := structuredGatewayRequest(t)

	response, err := srv.Complete(t.Context(), request)

	if err != nil {
		t.Fatalf("Complete error: %v", err)
	}
	if got := response.Content[0].Parts[0].(model.TextPart).Text; got != `{"answer":42}` {
		t.Fatalf("response text = %q, want middleware output", got)
	}
}

func TestServerStreamLetsMiddlewareDropProviderChunk(t *testing.T) {
	closeErr := errors.New("provider close failed")
	dropsChunks := func(next StreamHandler) StreamHandler {
		return func(ctx context.Context, req *model.Request, _ func(model.Chunk) error) (*model.Response, error) {
			return next(ctx, req, func(model.Chunk) error { return nil })
		}
	}
	srv, err := NewServer(
		WithProvider(stubProvider{streamer: &stubStreamer{
			chunks:   []model.Chunk{model.ToolCallChunk{ToolCall: model.ToolCall{Name: "svc.lookup"}}},
			recvErr:  io.EOF,
			closeErr: closeErr,
			response: &model.Response{
				Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
				StopReason: "done",
			},
		}}),
		WithStream(dropsChunks),
	)
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	_, err = srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error { return nil })
	if !errors.Is(err, closeErr) {
		t.Fatalf("stream error = %v, want provider close failure", err)
	}
}

func TestServerStreamPreservesValidationAndRecordsCloseFailure(t *testing.T) {
	closeErr := errors.New("provider close failed")
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(
		&model.TokenUsage{InputTokens: 1, OutputTokens: 1, TotalTokens: 2},
		model.NewUnadvertisedToolNameError("unlisted_tool"),
	)
	upstream := &stubStreamer{
		recvErr:  validationErr,
		closeErr: closeErr,
	}
	server, err := NewServer(WithProvider(stubProvider{streamer: upstream}))
	require.NoError(t, err)
	span := &recordingSpan{Span: trace.SpanFromContext(context.Background())}
	ctx := trace.ContextWithSpan(context.Background(), span)

	response, err := server.Stream(ctx, &model.Request{}, func(model.Chunk) error {
		return nil
	})

	require.Nil(t, response)
	require.Same(t, validationErr, err)
	require.NotErrorIs(t, err, closeErr)
	require.True(t, upstream.closed)
	require.Equal(t, []recordedSpanEvent{{
		name: "model.stream_cleanup_failed",
		attrs: []attribute.KeyValue{
			attribute.String("exception.message", closeErr.Error()),
		},
	}}, span.events)
	require.NotContains(t, fmt.Sprint(span.events), validationErr.Error())
	require.NotContains(t, fmt.Sprint(span.events), "unlisted_tool")
}

func TestServerStreamRetainsCleanupForMixedValidationFailure(t *testing.T) {
	closeErr := errors.New("provider close failed")
	unrelatedErr := errors.New("provider receive failed")
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(
		nil,
		model.NewUnadvertisedToolNameError("unlisted_tool"),
	)
	upstream := &stubStreamer{
		recvErr:  errors.Join(validationErr, unrelatedErr),
		closeErr: closeErr,
	}
	server, err := NewServer(WithProvider(stubProvider{streamer: upstream}))
	require.NoError(t, err)
	span := &recordingSpan{Span: trace.SpanFromContext(context.Background())}
	ctx := trace.ContextWithSpan(context.Background(), span)

	response, err := server.Stream(ctx, &model.Request{}, func(model.Chunk) error {
		return nil
	})

	require.Nil(t, response)
	require.ErrorIs(t, err, validationErr)
	require.ErrorIs(t, err, unrelatedErr)
	require.ErrorIs(t, err, closeErr)
	require.Empty(t, span.events)
}

func TestServerStreamRetainsCancellationDuringValidationFinalization(t *testing.T) {
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
	closeErr := errors.New("provider close failed")
	for _, test := range []struct {
		name     string
		closeErr error
	}{
		{name: "validation only"},
		{name: "validation and provider cleanup", closeErr: closeErr},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			upstream := &stubStreamer{recvErr: validationErr, closeErr: test.closeErr}
			server, serverErr := NewServer(WithProvider(stubProvider{streamer: upstream}))
			require.NoError(t, serverErr)
			span := &recordingSpan{Span: trace.SpanFromContext(context.Background())}
			ctx = trace.ContextWithSpan(ctx, span)

			response, streamErr := server.Stream(ctx, &model.Request{}, func(model.Chunk) error {
				return nil
			})

			require.Nil(t, response)
			require.ErrorIs(t, streamErr, validationErr)
			require.ErrorIs(t, streamErr, context.Canceled)
			if test.closeErr != nil {
				require.ErrorIs(t, streamErr, test.closeErr)
			}
			require.Empty(t, span.events)
		})
	}
}

func TestServerStreamSuppressesCleanupForWrappedDuplicateValidation(t *testing.T) {
	closeErr := errors.New("provider close failed")
	contract, err := model.NewRequestContract(&model.Request{})
	require.NoError(t, err)
	validationErr := contract.RejectProviderOutput(nil, errors.New("rejected output"))
	primaryErr := fmt.Errorf("translated: %w", errors.Join(validationErr, validationErr))
	upstream := &stubStreamer{recvErr: primaryErr, closeErr: closeErr}
	server, err := NewServer(WithProvider(stubProvider{streamer: upstream}))
	require.NoError(t, err)

	response, err := server.Stream(t.Context(), &model.Request{}, func(model.Chunk) error {
		return nil
	})

	require.Nil(t, response)
	require.Same(t, primaryErr, err)
	require.NotErrorIs(t, err, closeErr)
}

func TestServerStreamJoinsCallerSendAndProviderCloseFailures(t *testing.T) {
	callerErr := errors.New("caller send failed")
	closeErr := errors.New("provider close failed")
	upstream := &stubStreamer{
		chunks: []model.Chunk{
			model.TextChunk{Message: model.Message{
				Role:  model.ConversationRoleAssistant,
				Parts: []model.Part{model.TextPart{Text: "hello"}},
			}},
			model.StopChunk{Reason: "done"},
		},
		recvErr:  io.EOF,
		closeErr: closeErr,
		response: &model.Response{
			Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "hello"}}}},
			StopReason: "done",
		},
	}
	server, err := NewServer(WithProvider(stubProvider{streamer: upstream}))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	response, err := server.Stream(t.Context(), &model.Request{Model: "m"}, func(model.Chunk) error {
		return callerErr
	})

	if response != nil {
		t.Fatalf("response = %#v, want nil", response)
	}
	if !errors.Is(err, callerErr) {
		t.Fatalf("stream error = %v, want caller send failure", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("stream error = %v, want provider close failure", err)
	}
	if !upstream.closed {
		t.Fatal("provider stream was not closed")
	}
}

func TestServerStreamSendsCompletionChangedByMiddleware(t *testing.T) {
	changesCompletion := func(next StreamHandler) StreamHandler {
		return func(ctx context.Context, req *model.Request, send func(model.Chunk) error) (*model.Response, error) {
			return next(ctx, req, func(chunk model.Chunk) error {
				if completion, ok := chunk.(model.CompletionChunk); ok {
					completion.Completion.Payload = []byte(`{"answer":42}`)
					chunk = completion
				}
				return send(chunk)
			})
		}
	}
	srv, err := NewServer(
		WithProvider(stubProvider{streamer: &stubStreamer{
			chunks: []model.Chunk{
				model.CompletionChunk{Completion: model.Completion{
					Name:    "answer",
					Payload: []byte(`{"answer":"valid"}`),
				}},
				model.StopChunk{Reason: "done"},
			},
			recvErr:  io.EOF,
			response: structuredGatewayResponse(`{"answer":"valid"}`),
		}}),
		WithStream(changesCompletion),
	)
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}
	request := structuredGatewayRequest(t)
	sent := 0

	response, err := srv.Stream(t.Context(), request, func(model.Chunk) error {
		sent++
		return nil
	})

	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if response == nil {
		t.Fatal("response is nil")
	}
	if sent != 2 {
		t.Fatalf("sent chunks = %d, want 2", sent)
	}
}

func TestServerStreamSendsMiddlewareChunkWithoutFinalValidation(t *testing.T) {
	ignoresSendError := func(StreamHandler) StreamHandler {
		return func(_ context.Context, _ *model.Request, send func(model.Chunk) error) (*model.Response, error) {
			if err := send(model.ToolCallChunk{ToolCall: model.ToolCall{Name: "svc.lookup"}}); err != nil {
				return nil, err
			}
			return &model.Response{
				Content:    []model.Message{{Role: "assistant", Parts: []model.Part{model.TextPart{Text: "ok"}}}},
				StopReason: "done",
			}, nil
		}
	}
	srv, err := NewServer(WithProvider(stubProvider{}), WithStream(ignoresSendError))
	if err != nil {
		t.Fatalf("NewServer error: %v", err)
	}

	sent := 0
	_, err = srv.Stream(context.Background(), &model.Request{Model: "m"}, func(model.Chunk) error {
		sent++
		return nil
	})
	if err != nil {
		t.Fatalf("Stream error: %v", err)
	}
	if sent != 1 {
		t.Fatalf("sent chunks = %d, want 1", sent)
	}
}

func TestRemoteClientDropsResponseReturnedWithError(t *testing.T) {
	providerErr := errors.New("provider failed")
	client, err := NewRemoteClient(
		func(context.Context, *model.Request) (*model.Response, error) {
			return &model.Response{StopReason: "should-not-escape"}, providerErr
		},
		func(context.Context, *model.Request) (model.Streamer, error) {
			return nil, errors.New("unexpected stream call")
		},
	)
	if err != nil {
		t.Fatalf("NewRemoteClient error: %v", err)
	}

	response, err := client.Complete(t.Context(), &model.Request{})

	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if !errors.Is(err, providerErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func TestRemoteClientRejectsStructuredOutputWithoutNameBeforeGatewayCall(t *testing.T) {
	called := false
	client, err := NewRemoteClient(
		func(context.Context, *model.Request) (*model.Response, error) {
			called = true
			return nil, errors.New("unexpected gateway call")
		},
		func(context.Context, *model.Request) (model.Streamer, error) {
			return nil, errors.New("unexpected stream call")
		},
	)
	if err != nil {
		t.Fatalf("NewRemoteClient error: %v", err)
	}

	response, err := client.Complete(t.Context(), &model.Request{
		StructuredOutput: &model.StructuredOutput{
			Schema: []byte(`{"type":"object"}`),
		},
	})

	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if err == nil || err.Error() != "model request structured output name is required" {
		t.Fatalf("expected structured output name error, got %v", err)
	}
	if called {
		t.Fatal("gateway handler was called for invalid request")
	}
}

func structuredGatewayRequest(t *testing.T) *model.Request {
	t.Helper()
	request := &model.Request{StructuredOutput: &model.StructuredOutput{
		Name:   "answer",
		Schema: []byte(`{"type":"object"}`),
	}}
	err := model.SetCompletionValidator(request, func(response *model.Response, completion *model.Completion) error {
		payload := ""
		if completion != nil {
			payload = string(completion.Payload)
		} else if response != nil && len(response.Content) > 0 && len(response.Content[0].Parts) > 0 {
			text, ok := response.Content[0].Parts[0].(model.TextPart)
			if ok {
				payload = text.Text
			}
		}
		if payload != `{"answer":"valid"}` {
			return errors.New("gateway completion payload is invalid")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("SetCompletionValidator error: %v", err)
	}
	return request
}

func structuredGatewayResponse(payload string) *model.Response {
	return &model.Response{
		Content: []model.Message{{
			Role:  model.ConversationRoleAssistant,
			Parts: []model.Part{model.TextPart{Text: payload}},
		}},
		StopReason: "done",
	}
}
