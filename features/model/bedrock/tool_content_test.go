// These tests inspect Converse's typed toolResult fields so error status and
// native media cannot disappear while keeping a text-only response valid.
package bedrock

import (
	"testing"

	brtypes "github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	content "goa.design/goa-ai/runtime/content"
)

func TestToolContentUsesNativeCorrelatedResult(t *testing.T) {
	t.Parallel()
	messages := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "call-1", Name: "lookup", Input: rawjson.Message(`{}`)}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call-1", Content: "rejected", IsError: true,
			Blocks: content.Blocks{
				&content.TextContent{Text: "detail"},
				&content.ImageContent{Data: "AQI=", MIMEType: "image/png"},
				&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "private://pdf", MIMEType: new("application/pdf"), Blob: "AQI="}},
			},
		}}},
	}
	encoded, _, err := encodeMessages(messages, map[string]string{"lookup": "lookup"}, false)
	require.NoError(t, err)
	require.Len(t, encoded, 2)
	result := encoded[1].Content[0].(*brtypes.ContentBlockMemberToolResult).Value
	assert.Equal(t, "call-1", *result.ToolUseId)
	assert.Equal(t, brtypes.ToolResultStatusError, result.Status)
	require.Len(t, result.Content, 5)
	assert.Equal(t, "rejected", result.Content[0].(*brtypes.ToolResultContentBlockMemberText).Value)
	assert.Equal(t, "detail", result.Content[1].(*brtypes.ToolResultContentBlockMemberText).Value)
	image := result.Content[2].(*brtypes.ToolResultContentBlockMemberImage).Value
	assert.Equal(t, brtypes.ImageFormatPng, image.Format)
	assert.Equal(t, []byte{1, 2}, image.Source.(*brtypes.ImageSourceMemberBytes).Value)
	document := result.Content[4].(*brtypes.ToolResultContentBlockMemberDocument).Value
	assert.Equal(t, brtypes.DocumentFormatPdf, document.Format)
	assert.Equal(t, []byte{1, 2}, document.Source.(*brtypes.DocumentSourceMemberBytes).Value)
}

func TestToolContentRejectsAudioBeforeProviderDispatch(t *testing.T) {
	t.Parallel()
	_, err := encodeToolContent(content.Blocks{&content.AudioContent{Data: "AQI=", MIMEType: "audio/wav"}})
	assert.ErrorIs(t, err, model.ErrToolContentUnsupported)
}

func TestToolContentRetainsSpreadsheetAsNativeDocument(t *testing.T) {
	t.Parallel()
	items, err := encodeToolContent(content.Blocks{&content.EmbeddedResource{Resource: &content.BlobResourceContents{
		URI: "doc://sheet", MIMEType: new("text/csv"), Blob: "eCx5Cg==",
	}}})
	require.NoError(t, err)
	document := items[1].(*brtypes.ToolResultContentBlockMemberDocument).Value
	assert.Equal(t, brtypes.DocumentFormatCsv, document.Format)
	assert.Equal(t, []byte("x,y\n"), document.Source.(*brtypes.DocumentSourceMemberBytes).Value)
}

func TestToolContentEncodesWithoutStructuredResult(t *testing.T) {
	t.Parallel()
	messages := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "call-1", Name: "lookup", Input: rawjson.Message(`{}`)}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call-1",
			Blocks:    content.Blocks{&content.ImageContent{Data: "AQI=", MIMEType: "image/png"}},
		}}},
	}
	encoded, _, err := encodeMessages(messages, map[string]string{"lookup": "lookup"}, false)
	require.NoError(t, err)
	result := encoded[1].Content[0].(*brtypes.ContentBlockMemberToolResult).Value
	assert.Equal(t, "call-1", *result.ToolUseId)
	require.Len(t, result.Content, 1)
	image := result.Content[0].(*brtypes.ToolResultContentBlockMemberImage).Value
	assert.Equal(t, []byte{1, 2}, image.Source.(*brtypes.ImageSourceMemberBytes).Value)
}
