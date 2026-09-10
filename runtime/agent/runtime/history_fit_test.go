// These tests run Compress through the validated model client with recorded
// requests and scripted exact counts. They prove complete candidate selection
// and evidence delivery, not tokenizer arithmetic or model-written relevance.
package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type (
	fitCount struct {
		tokens  int
		err     error
		inexact bool
	}

	fitProvider struct {
		*evidenceProvider
		counts   []fitCount
		requests []*model.Request
	}
)

func TestCompressFitsActualSummaryAndLongestEligibleSuffix(t *testing.T) {
	for _, tc := range []struct {
		name       string
		final      []int
		keep       int
		wantErr    string
		keepBudget int
	}{
		{name: "full eligible suffix at equality", final: []int{200}, keep: 3},
		{name: "one over then equality", final: []int{201, 200}, keep: 2},
		{name: "nonmonotonic counts", final: []int{230, 240, 190}, keep: 1},
		{name: "nothing fits", final: []int{230, 240, 201}, wantErr: "(201 > 200)"},
		{name: "older allowance equality", final: []int{201, 200}, keep: 1, keepBudget: 60},
	} {
		t.Run(tc.name, func(t *testing.T) {
			messages := fitHistory()
			before := canonicalHistory(t, messages)
			provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "A synthetic summary"}), counts: []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}}}
			for _, count := range tc.final {
				provider.counts = append(provider.counts, fitCount{tokens: count})
			}
			tools := fitTools()
			historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, CompressAtMaxInputTokens: 200, KeepMaxTurns: 3, KeepMaxInputTokens: tc.keepBudget})(t.Context(), &model.Request{Messages: messages, Tools: tools, ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
			out := historyResult.Messages
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr)
				assert.Equal(t, messages, out)
			} else {
				require.NoError(t, err)
				require.Len(t, out, 2+2*tc.keep)
				assert.Same(t, messages[0], out[0])
				for i, original := range messages[len(messages)-2*tc.keep:] {
					assert.Same(t, original, out[i+2])
				}
				assert.Equal(t, canonicalHistory(t, out), canonicalHistory(t, provider.requests[len(provider.requests)-1].Messages))
			}
			assert.Equal(t, before, canonicalHistory(t, messages))
			assert.Equal(t, 1, provider.completeCalls)
			require.Len(t, provider.requests, 3+len(tc.final))
			initialKeep := 3
			if tc.keepBudget > 0 {
				initialKeep = 2
			}
			for i, request := range provider.requests[3:] {
				assert.Len(t, request.Messages, 2+2*(initialKeep-i))
				assert.Equal(t, "[Conversation Summary]\nA synthetic summary", textPart(t, request.Messages[1]))
			}
			for _, request := range provider.requests {
				assertToolDefinitionsEqual(t, tools, request.Tools)
				assert.Equal(t, model.ModelClassSmall, request.ModelClass)
				assert.Equal(t, before[0], canonicalHistory(t, request.Messages[:1])[0])
			}
			transcript := textPart(t, provider.request.Messages[1])
			for _, text := range []string{"oldest question", "older question", "recent question"} {
				assert.Equal(t, 1, strings.Count(transcript, text))
			}
			assert.NotContains(t, transcript, "newest question")
			assert.NotContains(t, transcript, `"text":"system"`)
		})
	}
}

func TestCompressActualFitStopsAtFirstCountFailure(t *testing.T) {
	countErr := errors.New("counter unavailable: complete diagnostic")
	for _, tc := range []struct {
		name   string
		counts []fitCount
		calls  int
		want   string
	}{
		{"newest too large", []fitCount{{tokens: 201}}, 0, "newest history turn cannot fit"},
		{"newest count error", []fitCount{{err: countErr}}, 0, countErr.Error()},
		{"first final count error", []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}, {err: countErr}}, 1, countErr.Error()},
		{"later final count error", []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}, {tokens: 201}, {err: countErr}}, 1, countErr.Error()},
		{"inexact final count", []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}, {inexact: true}}, 1, "requires exact token counts"},
		{"unsupported final count", []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}, {err: model.ErrTokenCountingUnsupported}}, 1, "requires a model provider with token counting"},
		{"cancelled final count", []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}, {err: context.Canceled}}, 1, context.Canceled.Error()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Summary"}), counts: tc.counts}
			messages := fitHistory()
			before := canonicalHistory(t, messages)
			historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})(t.Context(), &model.Request{Messages: messages, ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
			out := historyResult.Messages
			require.ErrorContains(t, err, tc.want)
			if errors.Is(tc.counts[len(tc.counts)-1].err, countErr) {
				require.ErrorIs(t, err, countErr)
			}
			assert.Equal(t, messages, out)
			assert.Equal(t, before, canonicalHistory(t, messages))
			assert.Equal(t, tc.calls, provider.completeCalls)
			assert.Len(t, provider.requests, len(tc.counts))
		})
	}
}

func TestCompressActualFitNewestOnlyHasOneFinalCount(t *testing.T) {
	for _, count := range []int{200, 201} {
		provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Summary"}), counts: []fitCount{{tokens: 100}, {tokens: count}}}
		messages := fitHistory()
		historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, CompressAtMaxInputTokens: 200, KeepMaxTurns: 1})(t.Context(), &model.Request{Messages: messages, ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
		out := historyResult.Messages
		if count == 200 {
			require.NoError(t, err)
			assert.Len(t, out, 4)
		} else {
			require.ErrorContains(t, err, "(201 > 200)")
			assert.Equal(t, messages, out)
		}
		assert.Equal(t, 1, provider.completeCalls)
		assert.Len(t, provider.requests, 2)
	}
}

func TestCompressCustomPromptScopeFollowsTotalCeiling(t *testing.T) {
	for _, tc := range []struct {
		name    string
		ceiling int
		budget  int
		counts  []fitCount
	}{
		{name: "turn only"},
		{name: "older token budget only", budget: 60, counts: []fitCount{{tokens: 100}, {tokens: 160}}},
		{name: "turn triggered with positive ceiling", ceiling: 200, budget: 60, counts: []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 200}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Focused summary"}), counts: tc.counts}
			messages := fitHistory()
			historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, KeepMaxTurns: 2, KeepMaxInputTokens: tc.budget, CompressAtMaxInputTokens: tc.ceiling}, WithSummaryPrompt("Keep dates 100%%\n%s\nEnd focus"), WithSummaryRole(model.ConversationRoleUser), WithModelClass(model.ModelClassHighReasoning))(t.Context(), &model.Request{Messages: messages, ModelClass: model.ModelClassHighReasoning}, historyTestClient(t, provider), nil)
			out := historyResult.Messages
			require.NoError(t, err)
			require.Len(t, out, 6)
			assert.Equal(t, model.ConversationRoleUser, out[1].Role)
			assert.Equal(t, map[string]any{"goa_ai_history": "summary"}, out[1].Meta)
			for i, original := range messages[5:] {
				assert.Same(t, original, out[i+2])
			}
			transcript := textPart(t, provider.request.Messages[1])
			assert.True(t, strings.HasPrefix(transcript, "Keep dates 100%\n"))
			assert.True(t, strings.HasSuffix(transcript, "\nEnd focus"))
			assert.Contains(t, transcript, "oldest question")
			assert.Contains(t, transcript, "older question")
			if tc.ceiling > 0 {
				assert.Contains(t, transcript, "recent question")
			} else {
				assert.NotContains(t, transcript, "recent question")
			}
			assert.NotContains(t, transcript, "newest question")
			assert.Len(t, provider.requests, len(tc.counts))
			assert.Equal(t, model.ModelClassHighReasoning, provider.request.ModelClass)
			for _, request := range provider.requests {
				assert.Equal(t, model.ModelClassHighReasoning, request.ModelClass)
			}
		})
	}
}

func TestCompressActualFitPreservesToolEvidenceAndNewestParallelStep(t *testing.T) {
	olderResult := model.ToolResultPart{ToolUseID: "old-reading", Content: rawjson.Message(`{"equipment":"A","value":7.25,"unit":"C","window":"morning","sequence":9007199254740993}`)}
	newerResult := model.ToolResultPart{ToolUseID: "new-reading", Content: rawjson.Message(`{"equipment":"A","value":8.0,"unit":"C","window":"evening"}`)}
	failed := model.ToolResultPart{ToolUseID: "failed-reading", IsError: true, Content: "service refused measurement: complete original diagnostic"}
	messages := []*model.Message{
		systemMsg(), userMsg("Compare measurements"), assistantToolUseMsg("old-reading", "lookup"),
		{Role: model.ConversationRoleUser, Parts: []model.Part{olderResult}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "new-reading", Name: "lookup", Input: rawjson.Message(`{"id":"A","window":"evening"}`)},
			model.ToolUsePart{ID: "failed-reading", Name: "lookup", Input: rawjson.Message(`{"id":"B"}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{newerResult, failed}},
		{Role: model.ConversationRoleAssistant, Meta: map[string]any{"provider_replay": "retain exactly"}, Parts: []model.Part{
			model.ThinkingPart{Text: "signed thought", Signature: "thinking signature", Final: true},
			model.ToolUsePart{ID: "alarm-a", Name: "lookup", Input: rawjson.Message(`{"id":"A","sequence":9007199254740993}`), ThoughtSignature: "call signature"},
			model.ToolUsePart{ID: "alarm-b", Name: "lookup", Input: rawjson.Message(`{"id":"B"}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "alarm-b", Content: rawjson.Message(`{"equipment":"B","alarms":0}`)},
			model.ToolResultPart{ToolUseID: "alarm-a", Content: rawjson.Message(`{"equipment":"A","alarms":2}`)},
		}},
		{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "Newest reminder"}}},
	}
	before := canonicalHistory(t, messages)
	provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Synthetic summary, not semantic proof"}), counts: []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 201}, {tokens: 200}}}
	historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 3, KeepMaxTurns: 2, KeepMaxInputTokens: 60, CompressAtMaxInputTokens: 200})(t.Context(), &model.Request{Messages: messages, Tools: fitTools(), ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
	out := historyResult.Messages
	require.NoError(t, err)
	require.Len(t, out, 5)
	for i, original := range messages[6:] {
		assert.Same(t, original, out[i+2])
	}
	assert.Equal(t, before, canonicalHistory(t, messages))
	assert.Equal(t, before[6:], canonicalHistory(t, out[2:]))
	transcript := textPart(t, provider.request.Messages[1])
	for _, part := range []model.Part{olderResult, newerResult, failed} {
		encoded, err := (model.Message{Role: model.ConversationRoleUser, Parts: []model.Part{part}}).MarshalJSON()
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(transcript, string(encoded)))
	}
	assert.Contains(t, transcript, "9007199254740993")
	assert.NotContains(t, transcript, "alarm-a")
	assert.NotContains(t, transcript, "Newest reminder")
	assert.Equal(t, 1, provider.completeCalls)
	assert.Len(t, provider.requests, 4)
}

func TestCompressActualFitCountsRenderedCitationsAndPreservesNativeGroups(t *testing.T) {
	image := model.ImagePart{Format: "png", Bytes: []byte("synthetic image")}
	document := model.DocumentPart{Name: "measurements", Format: "txt", Text: "synthetic document", Cite: true}
	cited := model.CitationsPart{Text: "The older measurement was 7.25 C.", Citations: []model.Citation{{Title: "measurements", SourceContent: []string{"7.25 C"}, Location: model.CitationLocation{DocumentPage: &model.DocumentPageLocation{DocumentIndex: 0, Start: 1, End: 2}}}}}
	provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(cited), counts: []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 201}, {tokens: 200}}}
	messages := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{image, document}}, assistantTextMsg("First"),
		{Role: model.ConversationRoleUser, Parts: []model.Part{document, image}}, assistantTextMsg("Second"),
		userMsg("Newest"), assistantTextMsg("Current"),
	}
	historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 3, KeepMaxTurns: 2, CompressAtMaxInputTokens: 200})(t.Context(), &model.Request{Messages: messages, ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
	out := historyResult.Messages
	require.NoError(t, err)
	require.Len(t, provider.request.Messages, 4)
	assert.Equal(t, []model.Part{model.TextPart{Text: "History message 0, part 0"}, image, model.TextPart{Text: "History message 0, part 1"}, document}, provider.request.Messages[2].Parts)
	assert.Equal(t, []model.Part{model.TextPart{Text: "History message 2, part 0"}, document, model.TextPart{Text: "History message 2, part 1"}, image}, provider.request.Messages[3].Parts)
	encoded, err := (model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{cited}}).MarshalJSON()
	require.NoError(t, err)
	for _, request := range provider.requests[2:] {
		summary := textPart(t, request.Messages[0])
		assert.Contains(t, summary, string(encoded))
		assert.Contains(t, summary, "Summary request document layout")
		assert.Contains(t, summary, "History message 2, part 0")
	}
	assert.Equal(t, canonicalHistory(t, out), canonicalHistory(t, provider.requests[3].Messages))
	assert.Equal(t, 1, provider.completeCalls)
}

func TestCompressActualFitPreservesUncompressedPaths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		messages []*model.Message
		config   HistoryCompressionConfig
		counts   []fitCount
	}{
		{"empty", nil, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 1}, nil},
		{"system only", []*model.Message{systemMsg()}, HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 1}, nil},
		{"not triggered at equality", fitHistory(), HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 1}, []fitCount{{tokens: 200}}},
		{"no excluded prefix", fitHistory(), HistoryCompressionConfig{CompressAtMaxInputTokens: 200, KeepMaxTurns: 4}, []fitCount{{tokens: 201}, {tokens: 100}, {tokens: 120}, {tokens: 140}, {tokens: 180}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "must not summarize"}), counts: tc.counts}
			historyResult, err := Compress(historyTestClient(t, provider), tc.config)(t.Context(), &model.Request{Messages: tc.messages, ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
			out := historyResult.Messages
			require.NoError(t, err)
			assert.Equal(t, tc.messages, out)
			assert.Zero(t, provider.completeCalls)
			assert.Len(t, provider.requests, len(tc.counts))
		})
	}
}

func TestCompressActualFitSummaryFailureDoesNotTrySmallerHistory(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parts []model.Part
		err   error
		want  string
	}{
		{"provider failure", nil, errors.New("summary provider rejected request: full diagnostic"), "summary provider rejected request: full diagnostic"},
		{"empty summary", []model.Part{model.TextPart{Text: " \n "}}, nil, "returned empty summary"},
		{"invalid output", []model.Part{model.ToolUsePart{ID: "unexpected", Name: "lookup", Input: rawjson.Message(`{}`)}}, nil, "model output does not meet its request contract"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(tc.parts...), counts: []fitCount{{tokens: 100}, {tokens: 160}, {tokens: 190}}}
			provider.err = tc.err
			messages := fitHistory()
			historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, CompressAtMaxInputTokens: 200, KeepMaxTurns: 3})(t.Context(), &model.Request{Messages: messages, ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
			out := historyResult.Messages
			require.ErrorContains(t, err, tc.want)
			if tc.err != nil {
				require.ErrorIs(t, err, tc.err)
			}
			assert.Equal(t, messages, out)
			assert.Equal(t, 1, provider.completeCalls)
			assert.Len(t, provider.requests, 3)
		})
	}
}

func TestCompressActualFitRecountsEachInvocationAndPreservesPriorSummary(t *testing.T) {
	prior := &model.Message{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "[Conversation Summary]\nPrevious summary"}}, Meta: map[string]any{"goa_ai_history": "summary"}}
	provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "New synthetic summary"}), counts: []fitCount{
		{tokens: 100}, {tokens: 160}, {tokens: 200},
		{tokens: 100}, {tokens: 160}, {tokens: 201}, {tokens: 200},
	}}
	policy := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, CompressAtMaxInputTokens: 200, KeepMaxTurns: 2})
	for i := range 2 {
		messages := append([]*model.Message{prior}, fitHistory()...)
		tools := fitTools()
		if i == 1 {
			messages[len(messages)-1] = assistantTextMsg("Newly received evidence")
			tools = append(tools, &model.ToolDefinition{Name: "other", Input: mustRuntimeToolInput(rawjson.Message(`{"type":"object"}`))})
		}
		historyResult, err := policy(t.Context(), &model.Request{Messages: messages, Tools: tools, ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
		out := historyResult.Messages
		require.NoError(t, err)
		assert.Same(t, prior, out[0])
		assert.Same(t, messages[1], out[1])
		assert.Same(t, messages[len(messages)-1], out[len(out)-1])
		assert.Equal(t, canonicalHistory(t, out), canonicalHistory(t, provider.requests[len(provider.requests)-1].Messages))
		assertToolDefinitionsEqual(t, tools, provider.requests[len(provider.requests)-1].Tools)
		assert.NotContains(t, textPart(t, provider.request.Messages[1]), "Previous summary")
		if i == 0 {
			assert.Len(t, out, 7)
		} else {
			assert.Len(t, out, 5)
		}
	}
	assert.Len(t, provider.requests, 7)
	assert.Equal(t, 2, provider.completeCalls)
}

func TestCompressActualFitWithoutStepCapUsesFiniteEligibleCandidates(t *testing.T) {
	provider := &fitProvider{evidenceProvider: evidenceSummaryProvider(model.TextPart{Text: "Summary"}), counts: []fitCount{
		{tokens: 100}, {tokens: 120}, {tokens: 140}, {tokens: 161},
		{tokens: 201}, {tokens: 230}, {tokens: 200},
	}}
	messages := fitHistory()
	historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 4, CompressAtMaxInputTokens: 200, KeepMaxInputTokens: 60})(t.Context(), &model.Request{Messages: messages, ModelClass: model.ModelClassSmall}, historyTestClient(t, provider), nil)
	out := historyResult.Messages
	require.NoError(t, err)
	assert.Len(t, out, 4)
	assert.Len(t, provider.requests, 7)
	assert.Equal(t, 1, provider.completeCalls)
	for i, request := range provider.requests[4:] {
		assert.Len(t, request.Messages, 2+2*(3-i))
	}
}

func (p *fitProvider) CountTokens(ctx context.Context, request *model.Request) (model.TokenCount, error) {
	index := len(p.requests)
	p.requests = append(p.requests, request)
	if err := ctx.Err(); err != nil {
		return model.TokenCount{}, err
	}
	if index >= len(p.counts) {
		return model.TokenCount{}, errors.New("unexpected count call")
	}
	count := p.counts[index]
	return model.TokenCount{InputTokens: count.tokens, Exact: !count.inexact, Model: "synthetic-history", ModelClass: request.ModelClass}, count.err
}

func fitHistory() []*model.Message {
	return []*model.Message{systemMsg(), userMsg("oldest question"), assistantTextMsg("oldest answer"), userMsg("older question"), assistantTextMsg("older answer"), userMsg("recent question"), assistantTextMsg("recent answer"), userMsg("newest question"), assistantTextMsg("newest answer")}
}

func fitTools() []*model.ToolDefinition {
	return []*model.ToolDefinition{{Name: "lookup", Description: "Looks up synthetic measurements.", Input: mustRuntimeToolInput(rawjson.Message(`{"type":"object","properties":{"id":{"type":"string"}}}`))}}
}
