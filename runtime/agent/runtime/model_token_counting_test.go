// Package runtime tests that model middleware preserves provider token counting
// without running inference preparation, saving responses, or opening inference spans.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

type (
	// countingTestProvider records the exact request and context passed through
	// runtime middleware, then returns the configured count or error unchanged.
	countingTestProvider struct {
		countingTestInferenceProvider
		count   model.TokenCount
		err     error
		calls   int
		ctx     context.Context
		request *model.Request
	}

	// countingTestInferenceProvider deliberately has no counting method so tests
	// can distinguish an unsupported provider from a failed counting operation.
	countingTestInferenceProvider struct {
		calls int
	}
)

func TestRuntimeProvidersForwardTokenCounting(t *testing.T) {
	countErr := errors.New("provider counting failed")
	for _, middleware := range []struct {
		name string
		wrap func(model.Provider) model.TokenCounter
	}{
		{
			name: "response recording",
			wrap: func(provider model.Provider) model.TokenCounter {
				return &modelInvocationProvider{inner: provider}
			},
		},
		{
			name: "tracing",
			wrap: func(provider model.Provider) model.TokenCounter {
				return &tracedProvider{inner: provider}
			},
		},
	} {
		for _, test := range []struct {
			name        string
			unsupported bool
			err         error
		}{
			{name: "count"},
			{name: "provider error", err: countErr},
			{name: "unsupported", unsupported: true, err: model.ErrTokenCountingUnsupported},
		} {
			t.Run(middleware.name+"/"+test.name, func(t *testing.T) {
				count := model.TokenCount{Model: "test-model", ModelClass: model.ModelClassDefault, InputTokens: 123, Exact: true}
				provider := &countingTestProvider{count: count, err: test.err}
				var raw model.Provider = provider
				if test.unsupported {
					raw = &provider.countingTestInferenceProvider
				}
				counter := middleware.wrap(raw)
				ctx := t.Context()
				request := &model.Request{ModelClass: model.ModelClassDefault}

				got, err := counter.CountTokens(ctx, request)

				require.ErrorIs(t, err, test.err)
				if test.unsupported {
					assert.Equal(t, model.TokenCount{}, got)
					assert.Zero(t, provider.calls)
				} else {
					assert.Equal(t, count, got)
					assert.Equal(t, 1, provider.calls)
					assert.Same(t, ctx, provider.ctx)
					assert.Same(t, request, provider.request)
				}
				assert.Zero(t, provider.countingTestInferenceProvider.calls)
			})
		}
	}
}

func TestRuntimeModelClientPreservesTokenCounting(t *testing.T) {
	countErr := errors.New("provider counting failed")
	for _, designated := range []bool{false, true} {
		for _, test := range []struct {
			name        string
			exact       bool
			unsupported bool
			err         error
		}{
			{name: "exact", exact: true},
			{name: "estimated"},
			{name: "provider error", err: countErr},
			{name: "unsupported", unsupported: true, err: model.ErrTokenCountingUnsupported},
		} {
			name := "ordinary/"
			if designated {
				name = "designated/"
			}
			t.Run(name+test.name, func(t *testing.T) {
				count := model.TokenCount{Model: "test-model", ModelClass: model.ModelClassHighReasoning, InputTokens: 123, Exact: test.exact}
				provider := &countingTestProvider{count: count, err: test.err}
				var raw model.Provider = provider
				if test.unsupported {
					raw = &provider.countingTestInferenceProvider
				}
				sink := &fakeModelInvocationSink{}
				tracer := &recordingTelemetryTracer{}
				preparations := 0
				rt := &Runtime{
					models: map[string]model.Client{"test": mustTestModelClient(raw)},
					tracer: tracer,
				}
				plannerContext := newAgentContext(agentContextOptions{
					runtime:     rt,
					agentID:     "test.agent",
					invocations: sink,
					cache:       CachePolicy{AfterSystem: true},
					history: func(_ context.Context, req *model.Request, _ model.TokenCounter, _ *HistorySummary) (HistoryResult, error) {
						preparations++
						return HistoryResult{Messages: req.Messages}, nil
					},
				}).(*simplePlannerContext)
				client, ok := plannerContext.configuredModelClient("test", designated)
				require.True(t, ok)
				client, err := model.WithRequestPreparation(client, func(ctx context.Context, req *model.Request) (context.Context, *model.Request, error) {
					preparations++
					return ctx, req, nil
				})
				require.NoError(t, err)
				ctx := t.Context()
				request := &model.Request{
					Model:      "test-model",
					ModelClass: model.ModelClassHighReasoning,
					Messages:   []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "test input"}}}},
					MaxTokens:  32,
					Thinking:   &model.ThinkingOptions{Enable: true, BudgetTokens: 16},
				}

				got, err := client.CountTokens(ctx, request)

				require.ErrorIs(t, err, test.err)
				if test.err != nil {
					assert.Equal(t, model.TokenCount{}, got)
				} else {
					assert.Equal(t, count, got)
				}
				if test.unsupported {
					assert.Zero(t, provider.calls)
				} else {
					assert.Equal(t, 1, provider.calls)
					assert.Same(t, ctx, provider.ctx)
					require.NotNil(t, provider.request)
					assert.NotSame(t, request, provider.request)
					assert.Equal(t, request.Model, provider.request.Model)
					assert.Equal(t, request.ModelClass, provider.request.ModelClass)
					assert.Equal(t, request.Messages, provider.request.Messages)
					assert.Equal(t, request.MaxTokens, provider.request.MaxTokens)
					assert.Equal(t, request.Thinking, provider.request.Thinking)
					assert.Nil(t, provider.request.Cache)
				}
				assert.Zero(t, provider.countingTestInferenceProvider.calls)
				assert.Zero(t, preparations)
				assert.Zero(t, sink.begins)
				assert.Empty(t, sink.finished)
				assert.Empty(t, sink.usageTotals)
				assert.Empty(t, tracer.spans)
				assert.Nil(t, request.Cache)
			})
		}
	}
}

// CountTokens records the middleware's input and returns the configured provider result.
func (p *countingTestProvider) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	p.calls++
	p.ctx, p.request = ctx, req
	return p.count, p.err
}

// Complete records unexpected inference so counting tests can reject the side effect.
func (p *countingTestInferenceProvider) Complete(context.Context, *model.Request) (*model.Response, error) {
	p.calls++
	return nil, errors.New("unexpected Complete call")
}

// Stream records unexpected inference so counting tests can reject the side effect.
func (p *countingTestInferenceProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	p.calls++
	return nil, errors.New("unexpected Stream call")
}
