package runtime

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"goa.design/goa-ai/internal/modelmetadata"
	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
)

func TestCompletedTurnMessagesPreservesConversationAndOriginals(t *testing.T) {
	messages := []*model.Message{
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.TextPart{Text: "Find the report."}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{
			model.ThinkingPart{Text: "private reasoning", Signature: "signed", Final: true},
			model.ThinkingPart{Redacted: []byte("encrypted"), Final: true},
			model.TextPart{Text: "I will look it up."},
			model.ToolUsePart{ID: "call-1", Name: "reports.find", Input: rawjson.Message(`{"query":"revenue"}`), ThoughtSignature: "tool-signature"},
		}, Meta: map[string]any{
			modelmetadata.OpenAIReasoningItems: []string{`{"type":"reasoning","encrypted_content":"opaque"}`},
			"application":                      map[string]any{"labels": []any{"keep"}},
			"openai_output_item":               "native output metadata remains unchanged",
			"openai_function_call_item":        "native call metadata remains unchanged",
			"openai_function_call_version":     "2",
			"openai_function_call_payload":     `{"query":"revenue"}`,
		}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call-1", Content: map[string]any{"value": []any{"result"}},
		}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.CitationsPart{
			Text: "Revenue increased.", Citations: []model.Citation{{SourceContent: []string{"report"},
				Location: model.CitationLocation{DocumentPage: &model.DocumentPageLocation{DocumentIndex: 0, Start: 1, End: 2}}}},
		}}},
	}
	original, err := model.CloneMessages(messages)
	require.NoError(t, err)

	got, err := completedTurnMessages(messages)
	require.NoError(t, err)
	require.Len(t, got, 4)
	assert.Equal(t, original, messages)
	expected, err := model.CloneMessages(original)
	require.NoError(t, err)
	expected[1].Parts = expected[1].Parts[2:]
	call := expected[1].Parts[1].(model.ToolUsePart)
	call.ThoughtSignature = ""
	expected[1].Parts[1] = call
	delete(expected[1].Meta, modelmetadata.OpenAIReasoningItems)
	assert.Equal(t, expected, got)

	const changed = "changed"
	got[1].Meta["application"].(map[string]any)["labels"].([]any)[0] = changed
	got[1].Parts[1].(model.ToolUsePart).Input[10] = 'X'
	got[2].Parts[0].(model.ToolResultPart).Content.(map[string]any)["value"].([]any)[0] = changed
	got[3].Parts[0].(model.CitationsPart).Citations[0].Location.DocumentPage.Start = 7
	assert.Equal(t, original, messages, "new-turn context must not modify stored/current-turn messages")
}

func TestCompletedTurnMessagesHandlesReasoningOnlyAndMetadataOnly(t *testing.T) {
	messages := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Signature: "signed", Final: true}}},
		{Role: model.ConversationRoleAssistant, Meta: map[string]any{modelmetadata.OpenAIReasoningItems: []string{"opaque"}}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ThinkingPart{Redacted: []byte("opaque"), Final: true}},
			Meta: map[string]any{"application": "keep"}},
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.TextPart{Text: "Done."}},
			Meta: map[string]any{modelmetadata.OpenAIReasoningItems: []string{"opaque"}}},
	}
	got, err := completedTurnMessages(messages)
	require.NoError(t, err)
	require.Len(t, got, 2)
	assert.Empty(t, got[0].Parts)
	assert.Equal(t, map[string]any{"application": "keep"}, got[0].Meta)
	assert.Equal(t, []model.Part{model.TextPart{Text: "Done."}}, got[1].Parts)
	assert.Empty(t, got[1].Meta)
	assert.Len(t, messages[0].Parts, 1)
	assert.Contains(t, messages[1].Meta, modelmetadata.OpenAIReasoningItems)
}

func TestCompletedTurnMessagesKeepsCanonicalBoundaryValidation(t *testing.T) {
	_, err := completedTurnMessages([]*model.Message{nil})
	require.ErrorContains(t, err, "message is nil")
	_, err = completedTurnMessages([]*model.Message{{Meta: map[string]any{"invalid": make(chan int)}}})
	require.Error(t, err)
	got, err := completedTurnMessages(nil)
	require.NoError(t, err)
	assert.Empty(t, got)
}
