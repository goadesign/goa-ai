// These tests exercise reusable compression with exact scripted counters.
// They distinguish reuse of old evidence from request-specific token fit and
// prove that growing history is summarized from original messages, not summaries.
package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
)

func TestCompressReusesSummaryBeforeCountingOriginalHistory(t *testing.T) {
	provider := &fitProvider{
		evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"}),
		counts: []fitCount{
			{tokens: 301}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {tokens: 180},
			{tokens: 190},
			{tokens: 201}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {tokens: 201}, {tokens: 180},
		},
	}
	client := historyTestClient(t, provider)
	policy := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})
	request := &model.Request{Messages: fitHistory()}
	before := canonicalHistory(t, request.Messages)
	first, err := policy(t.Context(), request, client, nil)
	require.NoError(t, err)
	require.NotNil(t, first.Summary)
	assert.Equal(t, 6, first.Summary.SourceMessages)
	assert.Equal(t, 2, first.Summary.ReplacedMessages)
	assert.Equal(t, 1, provider.completeCalls)
	require.Len(t, provider.requests, 5)

	request.Messages = append(request.Messages, userMsg("Another question"))
	second, err := policy(t.Context(), request, client, first.Summary)
	require.NoError(t, err)
	assert.Equal(t, first.Summary, second.Summary)
	assert.Equal(t, 1, provider.completeCalls)
	require.Len(t, provider.requests, 6)
	assert.Equal(t, canonicalHistory(t, second.Messages), canonicalHistory(t, provider.requests[5].Messages))
	assert.NotEqual(t, canonicalHistory(t, request.Messages), canonicalHistory(t, provider.requests[5].Messages))

	request.Messages = append(request.Messages, assistantTextMsg("Another answer"), userMsg("Latest question"))
	third, err := policy(t.Context(), request, client, second.Summary)
	require.NoError(t, err)
	require.NotNil(t, third.Summary)
	assert.Equal(t, 10, third.Summary.SourceMessages)
	assert.Equal(t, 6, third.Summary.ReplacedMessages)
	assert.Equal(t, 2, provider.completeCalls)
	assert.Len(t, provider.requests, 12)
	quoted := textPart(t, provider.request.Messages[1])
	assert.Contains(t, quoted, "oldest question")
	assert.Contains(t, quoted, "Another answer")
	assert.NotContains(t, quoted, "Latest question")
	assert.NotContains(t, quoted, "[Conversation Summary]")
	assert.Equal(t, before, canonicalHistory(t, request.Messages[:len(before)]))
}

func TestCompressReusedSummaryCountsActualDestinationRequest(t *testing.T) {
	provider := &fitProvider{
		evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"}),
		counts:           []fitCount{{tokens: 301}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {tokens: 180}},
	}
	client := historyTestClient(t, provider)
	policy := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})
	request := &model.Request{Messages: fitHistory()}
	first, err := policy(t.Context(), request, client, nil)
	require.NoError(t, err)
	request.Model = "another-destination"
	request.ModelClass = model.ModelClassHighReasoning
	request.Tools = fitTools()
	request.MaxTokens = 777
	request.Temperature = 0.25
	request.Thinking = &model.ThinkingOptions{Enable: true, BudgetTokens: 400}
	request.Cache = &model.CacheOptions{AfterSystem: true, AfterTools: true}
	var counted []*model.Request
	counter := historyCounterFunc(func(_ context.Context, actual *model.Request) (model.TokenCount, error) {
		counted = append(counted, actual)
		return model.TokenCount{InputTokens: 200, Exact: true}, nil
	})
	second, err := policy(t.Context(), request, counter, first.Summary)
	require.NoError(t, err)
	require.Len(t, counted, 1)
	expected := *request
	expected.Messages = second.Messages
	assert.Equal(t, expected, *counted[0])
	assert.Equal(t, first.Summary, second.Summary)
	assert.Equal(t, 1, provider.completeCalls)
}

func TestCompressReusedSummaryNeverResummarizesSameSourceToFit(t *testing.T) {
	provider := &fitProvider{
		evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"}),
		counts: []fitCount{
			{tokens: 301}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {tokens: 180},
			{tokens: 201}, {tokens: 50}, {tokens: 100}, {tokens: 150},
			{tokens: 201}, {tokens: 201}, {tokens: 201},
		},
	}
	client := historyTestClient(t, provider)
	policy := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})
	request := &model.Request{Messages: fitHistory()}
	first, err := policy(t.Context(), request, client, nil)
	require.NoError(t, err)
	before := *first.Summary
	second, err := policy(t.Context(), request, client, first.Summary)
	require.ErrorContains(t, err, "the generated summary and newest exact turns do not fit")
	assert.Equal(t, request.Messages, second.Messages)
	assert.Equal(t, before, *first.Summary)
	assert.Equal(t, 1, provider.completeCalls)
	assert.Len(t, provider.requests, 12)
}

func TestCompressReusedSummaryRefitsCoveredHistoryWithoutAnotherSummary(t *testing.T) {
	provider := &fitProvider{
		evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"}),
		counts: []fitCount{
			{tokens: 301}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {tokens: 180},
			{tokens: 201}, {tokens: 50}, {tokens: 100}, {tokens: 150}, {tokens: 201}, {tokens: 180},
		},
	}
	client := historyTestClient(t, provider)
	policy := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})
	request := &model.Request{Messages: fitHistory()}
	first, err := policy(t.Context(), request, client, nil)
	require.NoError(t, err)
	second, err := policy(t.Context(), request, client, first.Summary)
	require.NoError(t, err)
	assert.Equal(t, first.Summary.SourceMessages, second.Summary.SourceMessages)
	assert.Equal(t, first.Summary.Message, second.Summary.Message)
	assert.Equal(t, 4, second.Summary.ReplacedMessages)
	assert.Equal(t, 2, first.Summary.ReplacedMessages)
	assert.Equal(t, 1, provider.completeCalls)
	assert.Len(t, provider.requests, 11)
}

func TestCompressReusedSummaryKeepsCurrentInstructionsExact(t *testing.T) {
	provider := evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"})
	client := historyTestClient(t, provider)
	policy := Compress(client, HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 2})
	messages := fitHistory()
	instruction := historySystemMessage("Current answer requirements")
	messages = append(messages[:3:3], append([]*model.Message{instruction}, messages[3:]...)...)
	request := &model.Request{Messages: messages}
	first, err := policy(t.Context(), request, nil, nil)
	require.NoError(t, err)
	request.Messages[3] = historySystemMessage("Updated answer requirements")
	second, err := policy(t.Context(), request, nil, first.Summary)
	require.NoError(t, err)
	assert.Equal(t, first.Summary, second.Summary)
	assert.Same(t, request.Messages[3], second.Messages[2])
	assert.Equal(t, "Updated answer requirements", textPart(t, second.Messages[2]))
	assert.Equal(t, 1, provider.completeCalls)
}

func TestCompressReusedSummaryPropagatesCountFailure(t *testing.T) {
	provider := evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"})
	client := historyTestClient(t, provider)
	initial := Compress(client, HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 2})
	request := &model.Request{Messages: fitHistory()}
	first, err := initial(t.Context(), request, nil, nil)
	require.NoError(t, err)
	want := errors.New("destination counter unavailable: full diagnostic")
	counter := historyCounterFunc(func(context.Context, *model.Request) (model.TokenCount, error) {
		return model.TokenCount{}, want
	})
	policy := Compress(client, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 2})
	second, err := policy(t.Context(), request, counter, first.Summary)
	require.ErrorIs(t, err, want)
	assert.Equal(t, request.Messages, second.Messages)
	assert.Equal(t, 1, provider.completeCalls)
}

func TestCompressSummaryFingerprintOwnsContentContractNotModelSelection(t *testing.T) {
	for _, tc := range []struct {
		name   string
		option CompressOption
		calls  int
	}{
		{"summary model changes", WithModelClass(model.ModelClassHighReasoning), 1},
		{"summary prompt changes", WithSummaryPrompt("Keep measured observations: %s"), 2},
		{"summary role changes", WithSummaryRole(model.ConversationRoleUser), 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := evidenceSummaryProvider(model.TextPart{Text: "Earlier observations"})
			client := historyTestClient(t, provider)
			cfg := HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 2}
			request := &model.Request{Messages: fitHistory()}
			first, err := Compress(client, cfg)(t.Context(), request, nil, nil)
			require.NoError(t, err)
			second, err := Compress(client, cfg, tc.option)(t.Context(), request, nil, first.Summary)
			require.NoError(t, err)
			assert.Equal(t, tc.calls, provider.completeCalls)
			if tc.calls == 1 {
				assert.Equal(t, first.Summary, second.Summary)
			} else {
				assert.NotEqual(t, first.Summary.PolicyFingerprint, second.Summary.PolicyFingerprint)
			}
		})
	}
}

func TestHistorySummaryRejectsIncompleteOrNewestCoverage(t *testing.T) {
	for _, tc := range []struct {
		name             string
		source, replaced int
	}{
		{"empty source", 0, 0},
		{"replacement exceeds source", 2, 4},
		{"partial source turn", 3, 2},
		{"partial replaced turn", 4, 1},
		{"newest source turn", 8, 2},
		{"newest replaced turn", 8, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := historySummaryMessages(fitHistory(), &HistorySummary{
				SourceMessages: tc.source, ReplacedMessages: tc.replaced,
				PolicyFingerprint: "test-contract", Message: *historySystemMessage("Earlier observations"),
			})
			require.Error(t, err)
		})
	}
}
