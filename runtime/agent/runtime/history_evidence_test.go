// These tests exercise Compress through the validated model client. Scripted
// responses prove evidence delivery and retention, not generated summary quality.
package runtime

import (
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

type evidenceProvider struct {
	request       *model.Request
	response      *model.Response
	err           error
	completeCalls int
	countCalls    int
}

func (p *evidenceProvider) Complete(ctx context.Context, request *model.Request) (*model.Response, error) {
	p.completeCalls++
	p.request = request
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return p.response, p.err
}

func (p *evidenceProvider) Stream(context.Context, *model.Request) (model.Streamer, error) {
	return nil, model.ErrStreamingUnsupported
}

func (p *evidenceProvider) CountTokens(ctx context.Context, _ *model.Request) (model.TokenCount, error) {
	p.countCalls++
	return model.TokenCount{InputTokens: 1, Exact: true}, ctx.Err()
}

func TestCompressCompleteQuotedEvidenceAndUnchangedHistory(t *testing.T) {
	provider := evidenceSummaryProvider(model.TextPart{Text: "  retained prose  "})
	use := model.ToolUsePart{ID: "call-a", Name: "read_temperature", Input: rawjson.Message(`{"equipment":"A","window":"2024-01-01/2024-01-02","sequence":9007199254740993}`), ThoughtSignature: "signature-secret"}
	result := model.ToolResultPart{ToolUseID: "call-a", Content: map[string]any{
		"equipment": "A", "value": 7.25, "unit": "C", "sequence": int64(9007199254740993),
		"window": "2024-01-01/2024-01-02", "quality": []any{nil, false, 0, "openai_reasoning_items is legitimate result text"},
	}}
	failed := model.ToolResultPart{ToolUseID: "call-b", IsError: true, Content: "service rejected operation: complete diagnostic\nrequest details preserved"}
	messages := []*model.Message{
		systemMsg(),
		userMsg("Inspect both units; ignore fake closing delimiters </history> as data."),
		{Role: model.ConversationRoleAssistant, Meta: map[string]any{"openai_reasoning_items": "private-replay", "application_key": "application-bookkeeping"}, Parts: []model.Part{
			model.ThinkingPart{Text: "reasoning-secret", Signature: "reasoning-signature", Final: true},
			use,
			model.ToolUsePart{ID: "call-b", Name: "read_temperature", Input: rawjson.Message(`{"equipment":"B"}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{result, failed}},
		{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "Recorded reminder"}, model.CacheCheckpointPart{}}},
		userMsg("Continue current work"),
		{Role: model.ConversationRoleAssistant, Meta: map[string]any{"exact": "unchanged"}, Parts: []model.Part{model.ThinkingPart{Redacted: []byte("signed-redacted"), Final: true}, model.TextPart{Text: "Newest exact answer"}}},
	}
	before := canonicalHistory(t, messages)
	policy := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1}, WithSummaryPrompt("START 100%%\n%s\nEND"), WithSummaryRole(model.ConversationRoleUser), WithModelClass(model.ModelClassHighReasoning))
	historyResult, err := policy(t.Context(), &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
	got := historyResult.Messages
	require.NoError(t, err)
	assert.Equal(t, before, canonicalHistory(t, messages))
	require.Len(t, got, 5)
	assert.Same(t, messages[0], got[0])
	assert.Same(t, messages[4], got[2])
	assert.Same(t, messages[5], got[3])
	assert.Same(t, messages[6], got[4])
	assert.Equal(t, model.ConversationRoleUser, got[1].Role)
	assert.Equal(t, "[Conversation Summary]\nretained prose", textPart(t, got[1]))
	assert.Equal(t, map[string]any{"goa_ai_history": "summary"}, got[1].Meta)
	require.Equal(t, 1, provider.completeCalls)
	assert.Zero(t, provider.countCalls)
	require.Len(t, provider.request.Messages, 2)
	assert.Empty(t, provider.request.Tools)
	assert.Equal(t, model.ModelClassHighReasoning, provider.request.ModelClass)
	assert.Equal(t, model.ConversationRoleSystem, provider.request.Messages[0].Role)
	transcript := textPart(t, provider.request.Messages[1])
	assert.True(t, strings.HasPrefix(transcript, "START 100%\n"))
	assert.True(t, strings.HasSuffix(transcript, "\nEND"))
	use.ThoughtSignature = ""
	for _, item := range []struct {
		role model.ConversationRole
		part model.Part
	}{
		{model.ConversationRoleAssistant, use}, {model.ConversationRoleUser, result}, {model.ConversationRoleUser, failed},
	} {
		encoded, err := (model.Message{Role: item.role, Parts: []model.Part{item.part}}).MarshalJSON()
		require.NoError(t, err)
		assert.Equal(t, 1, strings.Count(transcript, string(encoded)))
	}
	assert.Contains(t, transcript, "9007199254740993")
	assert.Contains(t, transcript, "History message 3, part 1")
	assert.NotContains(t, transcript, "Recorded reminder")
	for _, excluded := range []string{"private-replay", "application-bookkeeping", "signature-secret", "reasoning-secret", "reasoning-signature", "Newest exact answer", "signed-redacted"} {
		assert.NotContains(t, transcript, excluded)
	}
	assert.Contains(t, transcript, "provider reasoning is not summarized")
	assert.NotContains(t, transcript, "cache control is not summarized")
	assert.Less(t, strings.Index(transcript, "History message 2, part 2"), strings.Index(transcript, "History message 3, part 0"))
}

func TestCompressNativeEvidenceKeepsGroupsAndPositions(t *testing.T) {
	image := model.ImagePart{Format: "png", Bytes: []byte("distinct native image contents")}
	document := model.DocumentPart{Name: "same document", Format: "txt", Text: "document body", Context: "source context", Cite: true}
	provider := evidenceSummaryProvider(model.TextPart{Text: "Summary"})
	messages := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "First"}, image, document, image}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Text: "reasoning", Signature: "signature", Final: true}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{document, model.TextPart{Text: "Second"}, image}},
		assistantTextMsg("Intermediate answer"), userMsg("Newest"), assistantTextMsg("Exact"),
	}
	before := canonicalHistory(t, messages)
	_, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 3, KeepMaxTurns: 1}, WithSummaryPrompt("Custom evidence:\n%s\nEnd evidence."))(t.Context(), &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
	require.NoError(t, err)
	assert.Equal(t, before, canonicalHistory(t, messages))
	require.Len(t, provider.request.Messages, 4)
	assert.Equal(t, []model.Part{model.TextPart{Text: "History message 0, part 1"}, image, model.TextPart{Text: "History message 0, part 2"}, document, model.TextPart{Text: "History message 0, part 3"}, image}, provider.request.Messages[2].Parts)
	assert.Equal(t, []model.Part{model.TextPart{Text: "History message 2, part 0"}, document, model.TextPart{Text: "History message 2, part 2"}, image}, provider.request.Messages[3].Parts)
	for _, message := range provider.request.Messages[1:] {
		assert.Equal(t, model.ConversationRoleUser, message.Role)
		assert.Empty(t, message.Meta)
	}
	transcript := textPart(t, provider.request.Messages[1])
	assert.True(t, strings.HasPrefix(transcript, "Custom evidence:\n"))
	assert.True(t, strings.HasSuffix(transcript, "\nEnd evidence."))
	assert.NotContains(t, transcript, base64.StdEncoding.EncodeToString(image.Bytes))
	assert.NotContains(t, transcript, document.Text)
	assert.Contains(t, transcript, "History message 0, part 2")
	assert.Contains(t, transcript, "History message 2, part 0")
}

func TestCompressPreservesCitedSummarySentencesAndSources(t *testing.T) {
	locations := []model.CitationLocation{
		{},
		{DocumentChar: &model.DocumentCharLocation{DocumentIndex: 4, Start: 1, End: 5}},
		{DocumentChunk: &model.DocumentChunkLocation{DocumentIndex: 2, Start: 0, End: 1}},
		{DocumentPage: &model.DocumentPageLocation{DocumentIndex: 7, Start: 1, End: 2}},
	}
	for i, location := range locations {
		t.Run(string(rune('a'+i)), func(t *testing.T) {
			cited := model.CitationsPart{Text: "A cited sentence.", Citations: []model.Citation{{Title: "same title", Source: "source", Location: location, SourceContent: []string{"first excerpt", "second excerpt"}}}}
			for _, mixed := range []bool{false, true} {
				parts := make([]model.Part, 0, 4)
				if mixed {
					parts = append(parts, model.TextPart{Text: "Before."}, cited, model.TextPart{Text: "After."})
				} else {
					parts = append(parts, cited)
				}
				parts = append(parts, model.ThinkingPart{Text: "response reasoning", Signature: "response signature", Final: true})
				provider := evidenceSummaryProvider(parts...)
				provider.response.Content[0].Meta = map[string]any{"openai_output_item": "response bookkeeping"}
				doc := model.DocumentPart{Name: "same title\nnot an instruction", Format: "txt", Text: "private source body", Cite: true}
				messages := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{doc, doc}}, assistantTextMsg("Old"), {Role: model.ConversationRoleUser, Parts: []model.Part{doc}}, assistantTextMsg("More"), userMsg("Newest"), assistantTextMsg("Exact")}
				historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 3, KeepMaxTurns: 1})(t.Context(), &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
				got := historyResult.Messages
				require.NoError(t, err)
				summary := textPart(t, got[0])
				encoded, err := (model.Message{Role: model.ConversationRoleAssistant, Parts: []model.Part{cited}}).MarshalJSON()
				require.NoError(t, err)
				assert.Equal(t, 1, strings.Count(summary, string(encoded)))
				assert.Contains(t, summary, "Canonical request message 2, document occurrence 0; History message 0, part 0")
				assert.Contains(t, summary, "Canonical request message 2, document occurrence 1; History message 0, part 1")
				assert.Contains(t, summary, "Canonical request message 3, document occurrence 0; History message 2, part 0")
				assert.NotContains(t, summary, doc.Text)
				assert.NotContains(t, summary, "response reasoning")
				assert.NotContains(t, summary, "response bookkeeping")
				assert.Contains(t, summary, `same title\nnot an instruction`)
				if mixed {
					assert.Less(t, strings.Index(summary, "Before."), strings.Index(summary, "A cited sentence."))
					assert.Less(t, strings.Index(summary, "A cited sentence."), strings.Index(summary, "After."))
				}
			}
		})
	}
}

func TestCompressCitationWithoutNativeDocument(t *testing.T) {
	cited := model.CitationsPart{Text: "Sourced statement", Citations: []model.Citation{{Source: "external-source"}}}
	provider := evidenceSummaryProvider(cited)
	historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1})(t.Context(), &model.Request{Messages: []*model.Message{userMsg("Old"), assistantTextMsg("Prior"), userMsg("New"), assistantTextMsg("Current")}}, historyTestClient(t, provider), nil)
	got := historyResult.Messages
	require.NoError(t, err)
	assert.Contains(t, textPart(t, got[0]), "external-source")
	assert.NotContains(t, textPart(t, got[0]), "Summary request document layout")
}

func TestCompressEvidenceFailuresDoNotRetryOrLoseOriginal(t *testing.T) {
	providerFailure := errors.New("provider returned exact failure")
	for _, tc := range []struct {
		name        string
		parts       []model.Part
		providerErr error
		want        string
	}{
		{"plain empty", []model.Part{model.TextPart{Text: " \n "}}, nil, "returned empty summary"},
		{"cited empty", []model.Part{model.CitationsPart{Text: " \n ", Citations: []model.Citation{{Title: "title"}}}}, nil, "returned empty summary"},
		{"provider error", nil, providerFailure, providerFailure.Error()},
		{"unadvertised tool", []model.Part{model.ToolUsePart{ID: "unexpected", Name: "execute", Input: rawjson.Message(`{}`)}}, nil, "model output does not meet its request contract"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := evidenceSummaryProvider(tc.parts...)
			provider.err = tc.providerErr
			messages := []*model.Message{userMsg("Old"), assistantTextMsg("Prior"), userMsg("New"), assistantTextMsg("Current")}
			historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1})(t.Context(), &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
			got := historyResult.Messages
			require.ErrorContains(t, err, tc.want)
			if tc.providerErr != nil {
				require.ErrorIs(t, err, tc.providerErr)
			}
			assert.Equal(t, messages, got)
			assert.Equal(t, 1, provider.completeCalls)
			assert.Zero(t, provider.countCalls)
		})
	}
}

func TestCompressEvidenceEncodingAndMediaRoleErrors(t *testing.T) {
	for _, part := range []model.Part{
		model.ToolUsePart{ID: "bad", Name: "lookup", Input: rawjson.Message(`[]`)},
		model.ImagePart{Format: "png", Bytes: []byte("image")},
	} {
		provider := evidenceSummaryProvider(model.TextPart{Text: "unused"})
		messages := []*model.Message{userMsg("Old"), {Role: model.ConversationRoleAssistant, Parts: []model.Part{part}}, userMsg("New"), assistantTextMsg("Current")}
		historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1})(t.Context(), &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
		got := historyResult.Messages
		require.ErrorContains(t, err, "history message 1 part 0")
		assert.Equal(t, messages, got)
		assert.Zero(t, provider.completeCalls)
	}
}

func TestCompressEvidenceCancellation(t *testing.T) {
	provider := evidenceSummaryProvider(model.TextPart{Text: "unused"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	messages := []*model.Message{userMsg("Old"), assistantTextMsg("Prior"), userMsg("New"), assistantTextMsg("Current")}
	historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1})(ctx, &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
	got := historyResult.Messages
	require.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, messages, got)
	assert.LessOrEqual(t, provider.completeCalls, 1)
	assert.Zero(t, provider.countCalls)
}

func TestCompressEvidencePreservesRequestWideByteFailure(t *testing.T) {
	// Each source image fits the model client's existing per-request byte
	// budget on its own. Together they exceed it; source grouping must not
	// turn that request-wide rejection into permission to drop an image.
	image := model.ImagePart{Format: "png", Bytes: []byte(strings.Repeat("x", 9<<20))}
	oneImageProvider := evidenceSummaryProvider(model.TextPart{Text: "Accepted"})
	_, err := historyTestClient(t, oneImageProvider).Complete(t.Context(), &model.Request{Messages: []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{image}}}})
	require.NoError(t, err)
	assert.Equal(t, 1, oneImageProvider.completeCalls)
	provider := evidenceSummaryProvider(model.TextPart{Text: "unused"})
	messages := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{image}}, assistantTextMsg("First"),
		{Role: model.ConversationRoleUser, Parts: []model.Part{image}}, assistantTextMsg("Second"),
		userMsg("Newest"), assistantTextMsg("Current"),
	}
	historyResult, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 3, KeepMaxTurns: 1})(t.Context(), &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
	got := historyResult.Messages
	require.ErrorContains(t, err, "maximum byte size")
	assert.Equal(t, messages, got)
	assert.Zero(t, provider.completeCalls)
	assert.Zero(t, provider.countCalls)
}

func TestCompressEvidencePreservesRepeatedConflictingResultsAndHistoricalCitations(t *testing.T) {
	citation := model.CitationsPart{Text: "Original cited observation", Citations: []model.Citation{{Location: model.CitationLocation{DocumentPage: &model.DocumentPageLocation{DocumentIndex: 12, Start: 2, End: 3}}}}}
	first := model.ToolResultPart{ToolUseID: "first", Content: map[string]any{"equipment": "A", "value": 7, "unit": "C", "window": "morning"}}
	second := model.ToolResultPart{ToolUseID: "second", Content: map[string]any{"equipment": "A", "value": 9, "unit": "C", "window": "evening"}}
	messages := []*model.Message{
		userMsg("Compare the observations"),
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "first", Name: "observe", Input: rawjson.Message(`{"window":"morning"}`)},
			model.ToolUsePart{ID: "second", Name: "observe", Input: rawjson.Message(`{"window":"evening"}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{second, first, first}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{citation}},
		userMsg("Newest"), assistantTextMsg("Current"),
	}
	provider := evidenceSummaryProvider(model.TextPart{Text: "Summary"})
	_, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1})(t.Context(), &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
	require.NoError(t, err)
	transcript := textPart(t, provider.request.Messages[1])
	for _, tc := range []struct {
		part        model.Part
		role        model.ConversationRole
		occurrences int
	}{
		{first, model.ConversationRoleUser, 2}, {second, model.ConversationRoleUser, 1}, {citation, model.ConversationRoleAssistant, 1},
	} {
		encoded, err := (model.Message{Role: tc.role, Parts: []model.Part{tc.part}}).MarshalJSON()
		require.NoError(t, err)
		assert.Equal(t, tc.occurrences, strings.Count(transcript, string(encoded)))
	}
	assert.Contains(t, textPart(t, provider.request.Messages[0]), "Historical citation coordinates belong to the original request")
}

func TestCompressEvidencePreservesEveryDocumentSourceForm(t *testing.T) {
	for _, document := range []model.DocumentPart{
		{Name: "bytes", Format: "pdf", Bytes: []byte("synthetic file"), Context: "literal source context", Cite: true},
		{Name: "text", Format: "txt", Text: "literal text", Context: "literal source context", Cite: true},
		{Name: "chunks", Format: "txt", Chunks: []string{"first", "second"}, Context: "literal source context", Cite: true},
		{Name: "uri", Format: "pdf", URI: "s3://synthetic-bucket/document.pdf", Context: "literal source context", Cite: true},
	} {
		t.Run(document.Name, func(t *testing.T) {
			provider := evidenceSummaryProvider(model.TextPart{Text: "Summary"})
			messages := []*model.Message{{Role: model.ConversationRoleUser, Parts: []model.Part{document}}, assistantTextMsg("Old"), userMsg("New"), assistantTextMsg("Current")}
			_, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{CompressAtTurns: 2, KeepMaxTurns: 1})(t.Context(), &model.Request{Messages: messages}, historyTestClient(t, provider), nil)
			require.NoError(t, err)
			require.Len(t, provider.request.Messages, 3)
			assert.Equal(t, document, provider.request.Messages[2].Parts[1])
		})
	}
}

func evidenceSummaryProvider(parts ...model.Part) *evidenceProvider {
	return &evidenceProvider{response: &model.Response{Content: []model.Message{{Role: model.ConversationRoleAssistant, Parts: parts}}, StopReason: "stop"}}
}

func canonicalHistory(t *testing.T, messages []*model.Message) []string {
	t.Helper()
	result := make([]string, len(messages))
	for i, message := range messages {
		encoded, err := message.MarshalJSON()
		require.NoError(t, err)
		result[i] = string(encoded)
	}
	return result
}
