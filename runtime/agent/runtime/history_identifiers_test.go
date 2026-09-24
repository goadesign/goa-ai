// Identifier fixtures exercise the actual summary request and its bounded
// insertion. Scripted output proves delivery, not the model's relevance judgment.
package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestCompressIdentifierEvidenceKeepsIndependentGroups(t *testing.T) {
	messages := identifierSummaryHistory()
	before := canonicalHistory(t, messages)
	provider := evidenceSummaryProvider(model.TextPart{Text: `The unfinished draft comparison uses drafts/Intro_Notes (introductory context) with drafts/Review_Log (review history). The unfinished reference comparison uses reference/IntroNotes (introductory context) with reference/ReviewLog (review history). These were resolved in separate groups and describe past availability. drafts/IntroNotes was rejected, not resolved.`})
	result, err := Compress(historyTestClient(t, provider), HistoryCompressionConfig{
		CompressAtTurns: 3, KeepMaxTurns: 1,
	})(t.Context(), &model.Request{Messages: messages}, provider, nil)
	require.NoError(t, err)
	require.Equal(t, 1, provider.completeCalls)
	assert.Zero(t, provider.countCalls)
	assert.Empty(t, provider.request.Tools)
	assert.Equal(t, before, canonicalHistory(t, messages))
	assert.Equal(t, historySummaryInstruction, textPart(t, provider.request.Messages[0]))
	evidence := textPart(t, provider.request.Messages[1])
	for _, literal := range []string{
		"drafts/Intro_Notes", "drafts/Review_Log",
		"reference/IntroNotes", "reference/ReviewLog",
		"draft-documents", "reference-documents", "drafts/IntroNotes",
		"archive/Finished_Outline", "archive/Withdrawn_Draft",
	} {
		assert.Contains(t, evidence, literal)
	}
	assert.Contains(t, evidence, `"tool_use_id":"draft-documents"`)
	assert.Contains(t, evidence, `"tool_use_id":"reference-documents"`)
	assert.NotContains(t, evidence, "Newest exact continuation")
	require.NotNil(t, result.Summary)
	assert.Equal(t, "[Conversation Summary]\n"+textPart(t, &provider.response.Content[0]), textPart(t, &result.Summary.Message))
	assert.NotContains(t, textPart(t, &result.Summary.Message), "archive/Finished_Outline")
	assert.NotContains(t, textPart(t, &result.Summary.Message), "archive/Withdrawn_Draft")
	assert.Same(t, messages[len(messages)-1], result.Messages[len(result.Messages)-1])
}

// identifierSummaryHistory uses synthetic document spellings, separately resolved
// groups, a rejected guess, and irrelevant completed work in the same evidence.
func identifierSummaryHistory() []*model.Message {
	return []*model.Message{
		{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "Finish two independent comparisons: draft and reference review history using introductory notes. The baseline check is complete. The archived draft review was withdrawn. Keep the two comparisons separate."}}},
		userMsg("The baseline is complete using archive/Finished_Outline. Do not continue the archive/Withdrawn_Draft investigation. Next resolve documents for the two pending comparisons."),
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "draft-documents", Name: "find_documents", Input: rawjson.Message(`{"group":"draft","collection":"drafts"}`)},
			model.ToolUsePart{ID: "reference-documents", Name: "find_documents", Input: rawjson.Message(`{"group":"reference","collection":"reference"}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "reference-documents", Content: rawjson.Message(`{"group":"reference","documents":[{"id":"reference/IntroNotes","label":"Introductory Notes","meaning":"introductory context"},{"id":"reference/ReviewLog","label":"Review Log","meaning":"review history"}]}`)},
			model.ToolResultPart{ToolUseID: "draft-documents", Content: rawjson.Message(`{"group":"draft","documents":[{"id":"drafts/Intro_Notes","label":"Introductory Notes","meaning":"introductory context"},{"id":"drafts/Review_Log","label":"Review Log","meaning":"review history"}]}`)},
		}},
		assistantTextMsg("Both comparisons remain unfinished. The draft guess drafts/IntroNotes was previously rejected; use the observed documents, not that guess. Document availability must be checked again before opening."),
		userMsg("Continue the two comparisons; do not merge their document groups."),
		assistantTextMsg("Newest exact continuation"),
	}
}
