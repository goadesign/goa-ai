package middleware

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

type fakeClient struct {
	response    *model.Response
	completeErr error
	streamErr   error
	stream      model.Streamer
	countErr    error

	completeCalls int
	streamCalls   int
}

type closeTrackingStreamer struct {
	chunk    model.Chunk
	response *model.Response
	sent     bool
	closed   bool
	closeErr error
	recvErr  error
}

func (s *closeTrackingStreamer) Recv() (model.Chunk, error) {
	if s.recvErr != nil {
		return nil, s.recvErr
	}
	if !s.sent && s.chunk != nil {
		s.sent = true
		return s.chunk, nil
	}
	return nil, io.EOF
}

func (s *closeTrackingStreamer) Close() error {
	s.closed = true
	return s.closeErr
}

func (s *closeTrackingStreamer) Response() *model.Response {
	return s.response
}

type fakeCountingClient struct {
	fakeClient

	count      model.TokenCount
	err        error
	countCalls int
}

type fakeNoCounterClient struct{}

type blockingCountingProvider struct {
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (p *blockingCountingProvider) CountTokens(ctx context.Context, _ *model.Request) (model.TokenCount, error) {
	if err := ctx.Err(); err != nil {
		return model.TokenCount{}, err
	}
	return model.TokenCount{InputTokens: 100}, nil
}

func (p *blockingCountingProvider) Complete(context.Context, *model.Request) (*model.Response, error) {
	if p.calls.Add(1) == 1 {
		close(p.entered)
		<-p.release
	}
	return &model.Response{Usage: model.TokenUsage{InputTokens: 20, OutputTokens: 5, TotalTokens: 25}}, nil
}

func (*blockingCountingProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, model.ErrStreamingUnsupported
}

func (f *fakeClient) Complete(_ context.Context, _ *model.Request) (*model.Response, error) {
	f.completeCalls++
	return f.response, f.completeErr
}

func (f *fakeClient) Stream(_ context.Context, _ *model.Request) (model.Streamer, error) {
	f.streamCalls++
	return f.stream, f.streamErr
}

func (*fakeNoCounterClient) Complete(context.Context, *model.Request) (*model.Response, error) {
	return nil, nil
}

func (*fakeNoCounterClient) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, nil
}

func (f *fakeClient) CountTokens(_ context.Context, req *model.Request) (model.TokenCount, error) {
	if f.countErr != nil {
		return model.TokenCount{}, f.countErr
	}
	return model.TokenCount{
		InputTokens: 1,
		Model:       "test",
		ModelClass:  req.ModelClass,
		Exact:       true,
	}, nil
}

func (f *fakeCountingClient) CountTokens(context.Context, *model.Request) (model.TokenCount, error) {
	f.countCalls++
	if f.err != nil {
		return model.TokenCount{}, f.err
	}
	return f.count, nil
}

func TestAdaptiveRateLimiterRequiresExactTokenCount(t *testing.T) {
	limiter := newAdaptiveRateLimiter(60_000, 60_000)
	err := limiter.wait(t.Context(), &fakeCountingClient{
		count: model.TokenCount{InputTokens: 10, Exact: false},
	}, &model.Request{})
	require.ErrorContains(t, err, "requires an exact provider token count")
}

func TestAdaptiveRateLimiterRejectsInvalidConstruction(t *testing.T) {
	for _, tc := range []struct {
		name    string
		key     string
		initial float64
		maximum float64
	}{
		{name: "missing capacity", initial: 0, maximum: 100},
		{name: "maximum below initial", initial: 100, maximum: 99},
		{name: "nonfinite capacity", initial: 100, maximum: math.Inf(1)},
		{name: "map key without map", key: "model", initial: 100, maximum: 100},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limiter, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, tc.key, tc.initial, tc.maximum)
			require.Nil(t, limiter)
			require.Error(t, err)
		})
	}
}

func TestAdaptiveRateLimiterRejectsNegativeCount(t *testing.T) {
	limiter, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 300, 300)
	require.NoError(t, err)
	raw := &fakeCountingClient{
		fakeClient: fakeClient{response: &model.Response{}},
		count:      model.TokenCount{InputTokens: -1},
	}
	provider, err := limiter.WrapProvider(raw)
	require.NoError(t, err)
	_, err = provider.Complete(t.Context(), &model.Request{MaxTokens: 100})
	require.ErrorContains(t, err, "nonnegative provider token count")
	require.Equal(t, 0, raw.completeCalls)
}

func TestUsageReconciledRateLimiterCanceledRequestDoesNotReserve(t *testing.T) {
	limiter, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 300, 300)
	require.NoError(t, err)
	raw := &fakeCountingClient{
		fakeClient: fakeClient{response: &model.Response{}},
		count:      model.TokenCount{InputTokens: 100},
	}
	provider, err := limiter.WrapProvider(raw)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = provider.Complete(ctx, &model.Request{MaxTokens: 100})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 0, raw.completeCalls)
	require.InDelta(t, 300, limiter.balance, 0.1)
}

func TestUsageReconciledRateLimiterProviderOverestimateBlocksNextRequest(t *testing.T) {
	limiter, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 300, 300)
	require.NoError(t, err)
	raw := &fakeCountingClient{
		fakeClient: fakeClient{response: &model.Response{Usage: model.TokenUsage{
			InputTokens: 200, OutputTokens: 60, TotalTokens: 260,
		}}},
		count: model.TokenCount{InputTokens: 100},
	}
	provider, err := limiter.WrapProvider(raw)
	require.NoError(t, err)
	req := &model.Request{MaxTokens: 100}
	_, err = provider.Complete(t.Context(), req)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = provider.Complete(ctx, req)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, raw.completeCalls)
}

func TestUsageReconciledRateLimiterConcurrentReservations(t *testing.T) {
	l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 300, 300)
	require.NoError(t, err)
	raw := &blockingCountingProvider{entered: make(chan struct{}), release: make(chan struct{})}
	provider, err := l.WrapProvider(raw)
	require.NoError(t, err)
	req := &model.Request{MaxTokens: 100}
	firstDone := make(chan error, 1)
	go func() {
		_, callErr := provider.Complete(t.Context(), req)
		firstDone <- callErr
	}()
	<-raw.entered
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	_, err = provider.Complete(ctx, req)
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.EqualValues(t, 1, raw.calls.Load())
	close(raw.release)
	require.NoError(t, <-firstDone)
	ctx, cancel = context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = provider.Complete(ctx, req)
	require.NoError(t, err)
	require.EqualValues(t, 2, raw.calls.Load())
}

func TestUsageReconciledRateLimiterRejectsInvalidProviderUsage(t *testing.T) {
	l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 400, 400)
	require.NoError(t, err)
	raw := &fakeCountingClient{
		fakeClient: fakeClient{response: &model.Response{Usage: model.TokenUsage{TotalTokens: -1}}},
		count:      model.TokenCount{InputTokens: 100},
	}
	provider, err := l.WrapProvider(raw)
	require.NoError(t, err)
	response, err := provider.Complete(t.Context(), &model.Request{MaxTokens: 100})
	require.Nil(t, response)
	require.ErrorContains(t, err, "invalid provider usage")
	require.InDelta(t, 200, l.balance, 0.1)

	raw.stream = &closeTrackingStreamer{response: raw.response}
	stream, err := provider.Stream(t.Context(), &model.Request{MaxTokens: 100})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorContains(t, err, "invalid provider usage")
	require.ErrorContains(t, stream.Close(), "invalid provider usage")
	require.InDelta(t, 0, l.balance, 0.1)
}

func TestUsageReconciledRateLimiterUsesProviderTotal(t *testing.T) {
	for _, tc := range []struct {
		name  string
		usage model.TokenUsage
		want  float64
	}{
		{name: "reported", usage: model.TokenUsage{InputTokens: 17, OutputTokens: 8, TotalTokens: 25}, want: 375},
		{name: "unknown", want: 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 400, 400)
			require.NoError(t, err)
			provider, err := l.WrapProvider(&fakeCountingClient{
				fakeClient: fakeClient{response: &model.Response{Usage: tc.usage}},
				count:      model.TokenCount{InputTokens: 100, Exact: false},
			})
			require.NoError(t, err)
			_, err = provider.Complete(t.Context(), &model.Request{MaxTokens: 100})
			require.NoError(t, err)
			l.mu.Lock()
			balance := l.balance
			l.mu.Unlock()
			require.InDelta(t, tc.want, balance, 0.1)
		})
	}
}

func TestUsageReconciledRateLimiterAdmitsNextRequestFromReportedUsage(t *testing.T) {
	l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 300, 300)
	require.NoError(t, err)
	provider, err := l.WrapProvider(&fakeCountingClient{
		fakeClient: fakeClient{response: &model.Response{Usage: model.TokenUsage{
			InputTokens: 17, OutputTokens: 8, TotalTokens: 25,
		}}},
		count: model.TokenCount{InputTokens: 100, Exact: false},
	})
	require.NoError(t, err)
	request := &model.Request{MaxTokens: 100}
	_, err = provider.Complete(t.Context(), request)
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = provider.Complete(ctx, request)
	require.NoError(t, err)
}

func TestUsageReconciledRateLimiterRetainsUsageOnFailure(t *testing.T) {
	l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 400, 400)
	require.NoError(t, err)
	provider, err := l.WrapProvider(&fakeCountingClient{
		fakeClient: fakeClient{completeErr: model.RetainUsage(errors.New("provider failed"), model.TokenUsage{
			InputTokens: 17, OutputTokens: 8, TotalTokens: 25,
		})},
		count: model.TokenCount{InputTokens: 100, Exact: false},
	})
	require.NoError(t, err)
	_, err = provider.Complete(t.Context(), &model.Request{MaxTokens: 100})
	require.ErrorContains(t, err, "provider failed")
	l.mu.Lock()
	balance := l.balance
	l.mu.Unlock()
	require.InDelta(t, 375, balance, 0.1)
}

func TestUsageReconciledRateLimiterStreamFinalTotalReplacesChunks(t *testing.T) {
	l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 400, 400)
	require.NoError(t, err)
	stream := &closeTrackingStreamer{
		chunk:    model.UsageChunk{Usage: model.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}},
		response: &model.Response{Usage: model.TokenUsage{InputTokens: 17, OutputTokens: 8, TotalTokens: 25}},
	}
	provider, err := l.WrapProvider(&fakeCountingClient{
		fakeClient: fakeClient{stream: stream},
		count:      model.TokenCount{InputTokens: 100, Exact: false},
	})
	require.NoError(t, err)
	wrapped, err := provider.Stream(t.Context(), &model.Request{MaxTokens: 100})
	require.NoError(t, err)
	_, err = wrapped.Recv()
	require.NoError(t, err)
	_, err = wrapped.Recv()
	require.ErrorIs(t, err, io.EOF)
	require.NoError(t, wrapped.Close())
	l.mu.Lock()
	balance := l.balance
	l.mu.Unlock()
	require.InDelta(t, 375, balance, 0.1)
}

func TestUsageReconciledRateLimiterEarlyCloseUsesReportedChunks(t *testing.T) {
	l, err := NewUsageReconciledAdaptiveRateLimiter(t.Context(), nil, "", 400, 400)
	require.NoError(t, err)
	provider, err := l.WrapProvider(&fakeCountingClient{
		fakeClient: fakeClient{stream: &closeTrackingStreamer{
			chunk: model.UsageChunk{Usage: model.TokenUsage{InputTokens: 10, OutputTokens: 2, TotalTokens: 12}},
		}},
		count: model.TokenCount{InputTokens: 100, Exact: false},
	})
	require.NoError(t, err)
	stream, err := provider.Stream(t.Context(), &model.Request{MaxTokens: 100})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.NoError(t, err)
	require.NoError(t, stream.Close())
	l.mu.Lock()
	balance := l.balance
	l.mu.Unlock()
	require.InDelta(t, 388, balance, 0.1)
}

func TestAdaptiveRateLimiterPublicConstructorsSelectRequestCost(t *testing.T) {
	tests := []struct {
		name            string
		newLimiter      func() *AdaptiveRateLimiter
		maxTokens       int
		expectedBalance float64
	}{
		{
			name: "input only",
			newLimiter: func() *AdaptiveRateLimiter {
				l, err := NewAdaptiveRateLimiter(t.Context(), nil, "", 1_000, 1_000)
				require.NoError(t, err)
				return l
			},
			expectedBalance: 900,
		},
		{
			name: "input and requested output",
			newLimiter: func() *AdaptiveRateLimiter {
				l, err := NewOutputReservationAdaptiveRateLimiter(
					t.Context(),
					nil,
					"",
					1_000,
					1_000,
				)
				require.NoError(t, err)
				return l
			},
			maxTokens:       50,
			expectedBalance: 850,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw := &fakeCountingClient{
				fakeClient: fakeClient{
					response: &model.Response{
						Content: []model.Message{{
							Role:  model.ConversationRoleAssistant,
							Parts: []model.Part{model.TextPart{Text: "ok"}},
						}},
						StopReason: "end_turn",
					},
				},
				count: model.TokenCount{
					Model:       "provider-model",
					InputTokens: 100,
					Exact:       true,
				},
			}
			client, err := model.NewClient(raw)
			require.NoError(t, err)
			limiter := test.newLimiter()
			wrapped, err := limiter.Middleware()(client)
			require.NoError(t, err)

			_, err = wrapped.Complete(t.Context(), &model.Request{
				Messages: []*model.Message{{
					Role:  model.ConversationRoleUser,
					Parts: []model.Part{model.TextPart{Text: "hello"}},
				}},
				MaxTokens: test.maxTokens,
			})

			require.NoError(t, err)
			require.Equal(t, 1, raw.countCalls)
			require.Equal(t, 1, raw.completeCalls)
			require.InDelta(t, test.expectedBalance, limiter.balance, 0.1)
		})
	}
}

func TestAdaptiveRateLimiterWrapProviderPreservesRawOutputAndTokenCounting(t *testing.T) {
	rawResponse := &model.Response{}
	raw := &fakeCountingClient{
		fakeClient: fakeClient{response: rawResponse},
		count: model.TokenCount{
			Model:       "provider-model",
			InputTokens: 100,
			Exact:       true,
		},
	}
	limiter, err := NewAdaptiveRateLimiter(t.Context(), nil, "", 1_000, 1_000)
	require.NoError(t, err)
	provider, err := limiter.WrapProvider(raw)
	require.NoError(t, err)
	counter, ok := provider.(model.TokenCounter)
	require.True(t, ok)
	count, err := counter.CountTokens(t.Context(), &model.Request{})
	require.NoError(t, err)

	response, err := provider.Complete(t.Context(), &model.Request{})

	require.NoError(t, err)
	require.Same(t, rawResponse, response)
	require.Equal(t, raw.count, count)
	require.Equal(t, 2, raw.countCalls)
	require.Equal(t, 1, raw.completeCalls)
}

func TestAdaptiveRateLimiterWrapProviderPreservesRawStream(t *testing.T) {
	rawChunk := model.TextChunk{Message: model.Message{
		Role:  model.ConversationRoleAssistant,
		Parts: []model.Part{model.TextPart{Text: "raw"}},
	}}
	rawResponse := &model.Response{
		Content: []model.Message{rawChunk.Message},
	}
	rawStream := &closeTrackingStreamer{
		chunk:    rawChunk,
		response: rawResponse,
	}
	raw := &fakeCountingClient{
		fakeClient: fakeClient{stream: rawStream},
		count: model.TokenCount{
			Model:       "provider-model",
			InputTokens: 100,
			Exact:       true,
		},
	}
	limiter, err := NewAdaptiveRateLimiter(t.Context(), nil, "", 1_000, 1_000)
	require.NoError(t, err)
	provider, err := limiter.WrapProvider(raw)
	require.NoError(t, err)

	stream, err := provider.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)
	chunk, err := stream.Recv()
	require.NoError(t, err)
	require.Equal(t, rawChunk, chunk)
	chunk, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
	require.Nil(t, chunk)
	require.Same(t, rawResponse, stream.Response())
	require.NoError(t, stream.Close())

	require.Equal(t, 1, raw.countCalls)
	require.Equal(t, 1, raw.streamCalls)
	require.True(t, rawStream.closed)
}

func TestAdaptiveRateLimiterWrapProviderRequiresTokenCounter(t *testing.T) {
	limiter, err := NewAdaptiveRateLimiter(t.Context(), nil, "", 1_000, 1_000)
	require.NoError(t, err)

	provider, err := limiter.WrapProvider(&fakeNoCounterClient{})

	require.Nil(t, provider)
	require.ErrorContains(t, err, "requires provider token counting")
}

func TestAdaptiveRateLimiterOutputReservationRejectsBeforeCounting(t *testing.T) {
	raw := &fakeCountingClient{
		fakeClient: fakeClient{
			stream: &closeTrackingStreamer{},
		},
		count: model.TokenCount{
			Model:       "provider-model",
			InputTokens: 100,
			Exact:       true,
		},
	}
	client, err := model.NewClient(raw)
	require.NoError(t, err)
	limiter, err := NewOutputReservationAdaptiveRateLimiter(
		t.Context(),
		nil,
		"",
		1_000,
		1_000,
	)
	require.NoError(t, err)
	wrapped, err := limiter.Middleware()(client)
	require.NoError(t, err)

	stream, err := wrapped.Stream(t.Context(), &model.Request{})

	require.Nil(t, stream)
	require.ErrorContains(t, err, "requires positive max tokens")
	require.Zero(t, raw.countCalls)
	require.Zero(t, raw.streamCalls)
}

func TestAdaptiveRateLimiterOutputReservationRejectsOverflow(t *testing.T) {
	raw := &fakeCountingClient{
		fakeClient: fakeClient{
			response: &model.Response{
				Content: []model.Message{{
					Role:  model.ConversationRoleAssistant,
					Parts: []model.Part{model.TextPart{Text: "ok"}},
				}},
				StopReason: "end_turn",
			},
		},
		count: model.TokenCount{
			Model:       "provider-model",
			InputTokens: math.MaxInt,
			Exact:       true,
		},
	}
	client, err := model.NewClient(raw)
	require.NoError(t, err)
	limiter, err := NewOutputReservationAdaptiveRateLimiter(
		t.Context(),
		nil,
		"",
		1_000,
		1_000,
	)
	require.NoError(t, err)
	wrapped, err := limiter.Middleware()(client)
	require.NoError(t, err)

	response, err := wrapped.Complete(t.Context(), &model.Request{
		Messages: []*model.Message{{
			Role:  model.ConversationRoleUser,
			Parts: []model.Part{model.TextPart{Text: "hello"}},
		}},
		MaxTokens: 1,
	})

	require.Nil(t, response)
	require.ErrorContains(t, err, "exceeds integer range")
	require.Equal(t, 1, raw.countCalls)
	require.Zero(t, raw.completeCalls)
}

func TestOutputReservationClusterKeySeparatesAccountingModes(t *testing.T) {
	require.Empty(t, outputReservationClusterKey(""))
	require.Equal(
		t,
		"model"+outputReservationClusterKeySuffix,
		outputReservationClusterKey("model"),
	)
}

func TestAdaptiveRateLimiter_BackoffOnRateLimited(t *testing.T) {
	t.Helper()

	limiter := newAdaptiveRateLimiter(60000, 60000)

	initialTPM := limiter.currentTPM

	client := &fakeClient{
		completeErr: model.ErrRateLimited,
	}
	wrapped := limitedTestClient(t, limiter, client)

	req := model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
		},
		MaxTokens: 10,
	}

	_, err := wrapped.Complete(context.Background(), &req)
	if err == nil || !errors.Is(err, model.ErrRateLimited) {
		t.Fatalf("expected ErrRateLimited, got %v", err)
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if limiter.currentTPM >= initialTPM {
		t.Fatalf("expected TPM to decrease, got %f (initial %f)",
			limiter.currentTPM, initialTPM)
	}
}

func TestLimitedClientClosesStreamReturnedWithError(t *testing.T) {
	callErr := errors.New("stream call failed")
	closeErr := errors.New("stream close failed")
	raw := &closeTrackingStreamer{closeErr: closeErr}
	limiter := newAdaptiveRateLimiter(60000, 60000)
	client := limitedTestClient(t, limiter, &fakeClient{stream: raw, streamErr: callErr})

	got, err := client.Stream(t.Context(), &model.Request{})

	if got != nil {
		t.Fatalf("stream = %v, want nil", got)
	}
	if !errors.Is(err, callErr) {
		t.Fatalf("error = %v, want call error", err)
	}
	if !errors.Is(err, closeErr) {
		t.Fatalf("error = %v, want close error", err)
	}
	if !raw.closed {
		t.Fatal("stream was not closed")
	}
}

func TestAdaptiveRateLimiterObservesTerminalStreamRateLimit(t *testing.T) {
	limiter := newAdaptiveRateLimiter(60_000, 60_000)
	initialTPM := limiter.currentTPM
	client := &fakeClient{
		stream: &closeTrackingStreamer{recvErr: model.ErrRateLimited},
	}
	provider := &limitedProvider{next: client, counter: client, limiter: limiter}

	stream, err := provider.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, model.ErrRateLimited)

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	require.Less(t, limiter.currentTPM, initialTPM)
}

func TestAdaptiveRateLimiterObservesCleanStreamOnce(t *testing.T) {
	limiter := newAdaptiveRateLimiter(60_000, 120_000)
	limiter.recoveryRate = 1_000
	initialTPM := limiter.currentTPM
	client := &fakeClient{
		stream: &closeTrackingStreamer{},
	}
	provider := &limitedProvider{next: client, counter: client, limiter: limiter}

	stream, err := provider.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)
	_, err = stream.Recv()
	require.ErrorIs(t, err, io.EOF)

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	require.InDelta(t, initialTPM+limiter.recoveryRate, limiter.currentTPM, 0.001)
}

func TestAdaptiveRateLimiterDoesNotTreatWrappedEOFAsSuccess(t *testing.T) {
	wrappedEOF := fmt.Errorf("provider stream failed: %w", io.EOF)
	limiter := newAdaptiveRateLimiter(60_000, 120_000)
	limiter.recoveryRate = 1_000
	initialTPM := limiter.currentTPM
	client := &fakeClient{
		stream: &closeTrackingStreamer{recvErr: wrappedEOF},
	}
	provider := &limitedProvider{next: client, counter: client, limiter: limiter}
	stream, err := provider.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)

	_, err = stream.Recv()

	require.Equal(t, wrappedEOF, err)
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	require.InDelta(t, initialTPM, limiter.currentTPM, 0.001)
}

func TestAdaptiveRateLimiterCloseDoesNotInventStreamOutcome(t *testing.T) {
	closeErr := errors.New("stream close failed")
	limiter := newAdaptiveRateLimiter(60_000, 120_000)
	initialTPM := limiter.currentTPM
	client := &fakeClient{
		stream: &closeTrackingStreamer{closeErr: closeErr},
	}
	provider := &limitedProvider{next: client, counter: client, limiter: limiter}

	stream, err := provider.Stream(t.Context(), &model.Request{})
	require.NoError(t, err)
	require.ErrorIs(t, stream.Close(), closeErr)

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	require.InDelta(t, initialTPM, limiter.currentTPM, 0.001)
}

func TestAdaptiveRateLimiter_ProbeOnSuccess(t *testing.T) {
	t.Helper()

	limiter := newAdaptiveRateLimiter(60000, 120000)

	limiter.mu.Lock()
	initialTPM := limiter.currentTPM
	limiter.recoveryRate = 1000
	limiter.mu.Unlock()

	client := &fakeClient{response: &model.Response{
		Content:    []model.Message{{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "ok"}}}},
		StopReason: "stop",
	}}
	wrapped := limitedTestClient(t, limiter, client)

	req := model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "hello"},
				},
			},
		},
		MaxTokens: 10,
	}

	_, err := wrapped.Complete(context.Background(), &req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	limiter.mu.Lock()
	defer limiter.mu.Unlock()

	if limiter.currentTPM <= initialTPM {
		t.Fatalf("expected TPM to increase, got %f (initial %f)",
			limiter.currentTPM, initialTPM)
	}
}

func TestAdaptiveRateLimiter_RespectsContextWhenQueued(t *testing.T) {
	t.Helper()

	limiter := newAdaptiveRateLimiter(60, 60)

	limiter.mu.Lock()
	limiter.currentTPM = 60
	// Exhaust the budget so the request must wait until its context ends.
	limiter.balance = 0
	limiter.mu.Unlock()

	client := &fakeClient{}
	wrapped := limitedTestClient(t, limiter, client)

	longText := make([]byte, 600)
	for i := range longText {
		longText[i] = 'a'
	}

	req := model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: string(longText)},
				},
			},
		},
		MaxTokens: 10,
	}

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	_, err := wrapped.Complete(ctx, &req)
	if err == nil {
		t.Fatal("expected limiter error")
	}
	if client.completeCalls != 0 {
		t.Fatalf("expected underlying client not to be called, got %d calls",
			client.completeCalls)
	}
}

func TestTokenEstimatorMonotonic(t *testing.T) {
	t.Helper()

	smallReq := &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "short"},
				},
			},
		},
	}
	bigReq := &model.Request{
		Messages: []*model.Message{
			{
				Role: model.ConversationRoleUser,
				Parts: []model.Part{
					model.TextPart{Text: "this is a much longer message"},
				},
			},
		},
	}

	estimator := model.TokenEstimator{
		CharactersPerToken: 1,
		MinimumTokens:      1,
		OverheadTokens:     1,
	}
	smallCount, err := estimator.CountTokens(context.Background(), smallReq)
	if err != nil {
		t.Fatalf("small estimate: %v", err)
	}
	bigCount, err := estimator.CountTokens(context.Background(), bigReq)
	if err != nil {
		t.Fatalf("big estimate: %v", err)
	}
	small := smallCount.InputTokens
	big := bigCount.InputTokens

	if small <= 0 {
		t.Fatalf("expected positive token estimate for small request, got %d",
			small)
	}
	if big <= small {
		t.Fatalf("expected larger estimate for larger request, small=%d big=%d",
			small, big)
	}
}

func TestAdaptiveRateLimiterDelegatesTokenCounting(t *testing.T) {
	limiter := newAdaptiveRateLimiter(60000, 60000)
	client := &fakeCountingClient{
		count: model.TokenCount{
			Model:       "provider-model",
			ModelClass:  model.ModelClassSmall,
			InputTokens: 42,
			Exact:       true,
		},
	}
	wrapped := limitedTestClient(t, limiter, client)

	count, err := wrapped.(model.TokenCounter).CountTokens(context.Background(), &model.Request{
		ModelClass: model.ModelClassSmall,
	})
	if err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count.InputTokens != 42 || !count.Exact {
		t.Fatalf("expected delegated exact count, got %#v", count)
	}
}

func TestAdaptiveRateLimiterCountTokensRequiresWrappedCounter(t *testing.T) {
	limiter := newAdaptiveRateLimiter(60000, 60000)
	wrapped := limitedTestClient(t, limiter, &fakeNoCounterClient{})

	_, err := wrapped.(model.TokenCounter).CountTokens(context.Background(), &model.Request{})
	if err == nil {
		t.Fatal("expected missing token counter error")
	}
}

func TestAdaptiveRateLimiterDropsResponseReturnedWithError(t *testing.T) {
	providerErr := errors.New("provider failed")
	limiter := newAdaptiveRateLimiter(60000, 60000)
	wrapped := limitedTestClient(t, limiter, &fakeClient{
		response:    &model.Response{StopReason: "should-not-escape"},
		completeErr: providerErr,
	})

	response, err := wrapped.Complete(t.Context(), &model.Request{})

	if response != nil {
		t.Fatalf("expected nil response, got %#v", response)
	}
	if !errors.Is(err, providerErr) {
		t.Fatalf("expected provider error, got %v", err)
	}
}

func limitedTestClient(t *testing.T, limiter *AdaptiveRateLimiter, provider model.Provider) model.Client {
	t.Helper()
	client, err := model.NewClient(provider)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	wrapped, err := limiter.Middleware()(client)
	if err != nil {
		t.Fatalf("rate limit middleware: %v", err)
	}
	return wrapped
}
