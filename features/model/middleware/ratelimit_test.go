package middleware

import (
	"context"
	"errors"
	"testing"

	"golang.org/x/time/rate"

	"goa.design/goa-ai/runtime/agent/model"
)

type fakeClient struct {
	completeErr error
	streamErr   error

	completeCalls int
	streamCalls   int
}

type fakeCountingClient struct {
	fakeClient

	count model.TokenCount
	err   error
}

func (f *fakeClient) Complete(_ context.Context, _ *model.Request) (*model.Response, error) {
	f.completeCalls++
	return nil, f.completeErr
}

func (f *fakeClient) Stream(_ context.Context, _ *model.Request) (model.Streamer, error) {
	f.streamCalls++
	return nil, f.streamErr
}

func (f *fakeCountingClient) CountTokens(context.Context, *model.Request) (model.TokenCount, error) {
	if f.err != nil {
		return model.TokenCount{}, f.err
	}
	return f.count, nil
}

func TestAdaptiveRateLimiter_BackoffOnRateLimited(t *testing.T) {
	t.Helper()

	limiter := newAdaptiveRateLimiter(60000, 60000)

	initialTPM := limiter.currentTPM

	client := &fakeClient{
		completeErr: model.ErrRateLimited,
	}
	wrapped := limiter.Middleware()(client)

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

func TestAdaptiveRateLimiter_ProbeOnSuccess(t *testing.T) {
	t.Helper()

	limiter := newAdaptiveRateLimiter(60000, 120000)

	limiter.mu.Lock()
	initialTPM := limiter.currentTPM
	limiter.recoveryRate = 1000
	limiter.mu.Unlock()

	client := &fakeClient{}
	wrapped := limiter.Middleware()(client)

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
	// Configure an impossible limiter so any non-zero token request fails
	// immediately. This exercises the error path without relying on timing.
	limiter.limiter = rate.NewLimiter(0, 0)
	limiter.mu.Unlock()

	client := &fakeClient{}
	wrapped := limiter.Middleware()(client)

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

	_, err := wrapped.Complete(context.Background(), &req)
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
	wrapped := limiter.Middleware()(client)

	count, err := wrapped.(model.TokenCounter).CountTokens(context.Background(), &model.Request{})
	if err != nil {
		t.Fatalf("count tokens: %v", err)
	}
	if count.InputTokens != 42 || !count.Exact {
		t.Fatalf("expected delegated exact count, got %#v", count)
	}
}

func TestAdaptiveRateLimiterCountTokensRequiresWrappedCounter(t *testing.T) {
	limiter := newAdaptiveRateLimiter(60000, 60000)
	wrapped := limiter.Middleware()(&fakeClient{})

	_, err := wrapped.(model.TokenCounter).CountTokens(context.Background(), &model.Request{})
	if err == nil {
		t.Fatal("expected missing token counter error")
	}
}
