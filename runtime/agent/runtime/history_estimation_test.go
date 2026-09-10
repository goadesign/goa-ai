// These tests select local history measurements explicitly and prove that the
// summary provider never receives a counting request or foreign replay data.
// Exact-count defaults and complete tool exchanges remain unchanged.
package runtime

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/transcript"
)

type (
	historyCounterFunc func(context.Context, *model.Request) (model.TokenCount, error)
)

func (f historyCounterFunc) CountTokens(ctx context.Context, req *model.Request) (model.TokenCount, error) {
	return f(ctx, req)
}

func TestCompressExplicitEstimateKeepsOpaqueHistory(t *testing.T) {
	provider := &historyCountingClient{countErr: errors.New("summary provider must not count")}
	messages := []*model.Message{
		userMsg("Inspect the current measurement"),
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Redacted: []byte("opaque-provider-reasoning"), Final: true},
		}, Meta: map[string]any{"openai_reasoning_items": "opaque-provider-replay"}},
		assistantToolUseMsg("read-1", "read_measurement"),
		toolResultMsg("read-1", "12 C"),
	}
	before := canonicalHistory(t, messages)
	estimator := model.TokenEstimator{}
	count, err := estimator.CountTokens(t.Context(), &model.Request{Messages: messages})
	require.NoError(t, err)
	require.False(t, count.Exact)
	policy := Compress(historyTestClient(t, provider), HistoryCompressionConfig{
		AllowEstimatedTokens:     true,
		CompressAtMaxInputTokens: count.InputTokens,
		KeepMaxTurns:             1,
	})
	historyResult, err := policy(t.Context(), &model.Request{Messages: messages}, estimator, nil)
	out := historyResult.Messages
	require.NoError(t, err)
	assertExactHistory(t, messages, out)
	assert.Equal(t, before, canonicalHistory(t, messages))
	assert.False(t, provider.tokenCounted)
	assert.Nil(t, provider.summarized)
	require.NoError(t, transcript.ValidatePlannerTranscript(out))
}

func TestCompressExplicitEstimateSummarizesWholeExchanges(t *testing.T) {
	provider := evidenceSummaryProvider(model.TextPart{Text: "Earlier measurements retained"})
	messages, exchanges := completeExchangeHistory(3)
	// Include foreign opaque reasoning in the summarized prefix, not only in
	// the retained tail, so counting and summary delivery are tested together.
	opaque := exchanges[0][0]
	opaque.Parts = append(opaque.Parts, model.ThinkingPart{Redacted: []byte("foreign-opaque-reasoning"), Final: true})
	opaque.Meta = map[string]any{"openai_reasoning_items": "foreign-opaque-replay"}
	before := canonicalHistory(t, messages)
	estimator := model.TokenEstimator{}
	policy := Compress(historyTestClient(t, provider), HistoryCompressionConfig{
		AllowEstimatedTokens:     true,
		CompressAtTurns:          3,
		CompressAtMaxInputTokens: 10000,
		KeepMaxTurns:             1,
		KeepMaxInputTokens:       2000,
	})
	historyResult, err := policy(t.Context(), &model.Request{Messages: messages, Tools: fitTools()}, estimator, nil)
	out := historyResult.Messages
	require.NoError(t, err)
	require.NoError(t, transcript.ValidatePlannerTranscript(out))
	require.Len(t, out, 4+len(exchanges[2]))
	assertExactHistory(t, []*model.Message{exchanges[0][len(exchanges[0])-1], exchanges[1][len(exchanges[1])-1]}, out[2:4])
	assertExactHistory(t, exchanges[2], out[4:])
	assert.Equal(t, before, canonicalHistory(t, messages))
	assert.Zero(t, provider.countCalls)
	require.Equal(t, 1, provider.completeCalls)
	assert.Equal(t, model.ModelClassSmall, provider.request.ModelClass)
	assert.Empty(t, provider.request.Tools)
	for _, message := range provider.request.Messages {
		assert.Empty(t, message.Meta)
		for _, part := range message.Parts {
			_, thinking := part.(model.ThinkingPart)
			assert.False(t, thinking)
		}
	}
	count, err := estimator.CountTokens(t.Context(), &model.Request{Messages: out, Tools: fitTools()})
	require.NoError(t, err)
	assert.LessOrEqual(t, count.InputTokens, 10000)
}

func TestCompressExplicitCounterRejectsInvalidMeasurement(t *testing.T) {
	measurementErr := errors.New("measurement failed")
	for _, tc := range []struct {
		name  string
		count int
		err   error
		want  string
	}{
		{name: "negative", count: -1, want: "negative input token count"},
		{name: "error", err: measurementErr, want: "measurement failed"},
		{name: "unsupported with details", err: fmt.Errorf("destination counter unavailable: %w", model.ErrTokenCountingUnsupported), want: "destination counter unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &historyCountingClient{}
			counter := historyCounterFunc(func(context.Context, *model.Request) (model.TokenCount, error) {
				return model.TokenCount{InputTokens: tc.count}, tc.err
			})
			messages := []*model.Message{userMsg("Question"), assistantTextMsg("Answer")}
			policy := Compress(historyTestClient(t, provider), HistoryCompressionConfig{
				CompressAtMaxInputTokens: 100,
				KeepMaxTurns:             1,
			})
			historyResult, err := policy(t.Context(), &model.Request{Messages: messages}, counter, nil)
			out := historyResult.Messages
			require.ErrorContains(t, err, tc.want)
			if tc.err != nil {
				assert.ErrorIs(t, err, tc.err)
			}
			assertExactHistory(t, messages, out)
			assert.False(t, provider.tokenCounted)
			assert.Nil(t, provider.summarized)
		})
	}
}

// Every candidate uses the actual request options, including the final summary
// plus retained messages. The separate summary call keeps its own model class.
func TestCompressCountsCompleteRequestAtEveryRetentionBoundary(t *testing.T) {
	provider := &fitProvider{
		evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Earlier facts"}),
		counts:           []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}, {tokens: 201}, {tokens: 200}},
	}
	client := historyTestClient(t, provider)
	req := &model.Request{
		Model:       "destination-model",
		ModelClass:  model.ModelClassDefault,
		Messages:    fitHistory(),
		Tools:       fitTools(),
		MaxTokens:   500,
		Temperature: 0.4,
		Thinking:    &model.ThinkingOptions{Enable: true, BudgetTokens: 80},
		Cache:       &model.CacheOptions{AfterSystem: true, AfterTools: true},
	}
	before := canonicalHistory(t, req.Messages)
	policy := Compress(client, HistoryCompressionConfig{CompressAtTurns: 4, CompressAtMaxInputTokens: 200, KeepMaxTurns: 3}, WithModelClass(model.ModelClassSmall))
	historyResult, err := policy(t.Context(), req, client, nil)
	out := historyResult.Messages
	require.NoError(t, err)
	require.Len(t, provider.requests, 5)
	for _, counted := range provider.requests {
		assert.Equal(t, req.Model, counted.Model)
		assert.Equal(t, req.ModelClass, counted.ModelClass)
		assert.Equal(t, req.MaxTokens, counted.MaxTokens)
		assert.Equal(t, req.Temperature, counted.Temperature)
		assert.Equal(t, req.Thinking, counted.Thinking)
		assert.Equal(t, req.Cache, counted.Cache)
		require.Len(t, counted.Tools, 1)
		assert.Equal(t, req.Tools[0].Input.Contract(), counted.Tools[0].Input.Contract())
	}
	assert.Equal(t, canonicalHistory(t, out), canonicalHistory(t, provider.requests[4].Messages))
	assert.Equal(t, before, canonicalHistory(t, req.Messages))
	assert.Empty(t, provider.request.Model)
	assert.Equal(t, model.ModelClassSmall, provider.request.ModelClass)
	assert.Nil(t, provider.request.Thinking)
	assert.Nil(t, provider.request.Cache)
	assert.Empty(t, provider.request.Tools)
}
