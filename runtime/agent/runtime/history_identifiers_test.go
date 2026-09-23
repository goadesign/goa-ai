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
	provider := evidenceSummaryProvider(model.TextPart{Text: `The unfinished north comparison uses line_1.HMI_TrackingActive (tracking exposure) with line_1.Run_Status (stops). The unfinished south comparison uses line_2.HMITrackingActive (tracking exposure) with line_2.RunStatus (stops). These were resolved in separate groups and describe past availability. line_1.HMITrackingActive was rejected, not resolved.`})
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
		"line_1.HMI_TrackingActive", "line_1.Run_Status",
		"line_2.HMITrackingActive", "line_2.RunStatus",
		"north-sources", "south-sources", "line_1.HMITrackingActive",
		"completed_1.Average", "retired_1.Tracking",
	} {
		assert.Contains(t, evidence, literal)
	}
	assert.Contains(t, evidence, `"tool_use_id":"north-sources"`)
	assert.Contains(t, evidence, `"tool_use_id":"south-sources"`)
	assert.NotContains(t, evidence, "Newest exact continuation")
	require.NotNil(t, result.Summary)
	assert.Equal(t, "[Conversation Summary]\n"+textPart(t, &provider.response.Content[0]), textPart(t, &result.Summary.Message))
	assert.NotContains(t, textPart(t, &result.Summary.Message), "completed_1.Average")
	assert.NotContains(t, textPart(t, &result.Summary.Message), "retired_1.Tracking")
	assert.Same(t, messages[len(messages)-1], result.Messages[len(result.Messages)-1])
}

// identifierSummaryHistory keeps exact source spellings, separately resolved
// groups, a rejected guess, and irrelevant completed work in the same evidence.
func identifierSummaryHistory() []*model.Message {
	return []*model.Message{
		{Role: model.ConversationRoleSystem, Parts: []model.Part{model.TextPart{Text: "Finish two independent comparisons: north and south stops during tracking-active exposure. The baseline check is complete. The retired line investigation was withdrawn. Keep the two comparisons separate."}}},
		userMsg("The baseline is complete using completed_1.Average. Do not continue the retired_1.Tracking investigation. Next resolve sources for the two pending comparisons."),
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ToolUsePart{ID: "north-sources", Name: "resolve_sources", Input: rawjson.Message(`{"group":"north","aliases":["line_1"]}`)},
			model.ToolUsePart{ID: "south-sources", Name: "resolve_sources", Input: rawjson.Message(`{"group":"south","aliases":["line_2"]}`)},
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{
			model.ToolResultPart{ToolUseID: "south-sources", Content: rawjson.Message(`{"group":"south","sources":[{"id":"line_2.HMITrackingActive","label":"Tracking Active","meaning":"tracking exposure"},{"id":"line_2.RunStatus","label":"Run Status","meaning":"stops"}]}`)},
			model.ToolResultPart{ToolUseID: "north-sources", Content: rawjson.Message(`{"group":"north","sources":[{"id":"line_1.HMI_TrackingActive","label":"Tracking Active","meaning":"tracking exposure"},{"id":"line_1.Run_Status","label":"Run Status","meaning":"stops"}]}`)},
		}},
		assistantTextMsg("Both comparisons remain unfinished. The north guess line_1.HMITrackingActive was previously rejected; use the observed sources, not that guess. Source availability must be checked again when reading."),
		userMsg("Continue the two comparisons; do not merge their source groups."),
		assistantTextMsg("Newest exact continuation"),
	}
}
