// These tests preserve function correlation and ordered references to native
// media without overwriting a tool's semantic fields or copying host metadata.
package vertex

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	"goa.design/goa-ai/runtime/agent/rawjson"
	content "goa.design/goa-ai/runtime/content"
)

func TestToolContentUsesNativeCorrelatedFunctionResponse(t *testing.T) {
	t.Parallel()
	semantic := map[string]any{"content": "domain field"}
	messages := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "call-1", Name: "lookup", Input: rawjson.Message(`{}`)}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call-1", Content: semantic,
			Blocks: content.Blocks{
				&content.TextContent{Text: "detail", Meta: json.RawMessage(`{"secret":"host-value"}`)},
				&content.ImageContent{Data: "AQI=", MIMEType: "image/png"},
				&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "private://pdf", MIMEType: new("application/pdf"), Blob: "AQI="}},
			},
		}}},
	}
	_, encoded, err := encodeContents(messages, map[string]string{"lookup": "lookup"})
	require.NoError(t, err)
	result := encoded[1].Parts[0].FunctionResponse
	assert.Equal(t, "call-1", result.ID)
	assert.Equal(t, "lookup", result.Name)
	assert.Equal(t, semantic, result.Response["result"])
	ordered := result.Response["content"].([]any)
	require.Len(t, ordered, 4)
	assert.Equal(t, map[string]any{"text": "detail"}, ordered[0])
	assert.Equal(t, map[string]any{"$ref": "tool-content-1"}, ordered[1])
	assert.Equal(t, map[string]any{"$ref": "tool-content-3"}, ordered[3])
	require.Len(t, result.Parts, 2)
	assert.Equal(t, "image/png", result.Parts[0].InlineData.MIMEType)
	assert.Equal(t, "tool-content-1", result.Parts[0].InlineData.DisplayName)
	assert.Equal(t, []byte{1, 2}, result.Parts[0].InlineData.Data)
	assert.Equal(t, "application/pdf", result.Parts[1].InlineData.MIMEType)
	encodedJSON, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(encodedJSON), "host-value")
	assert.Equal(t, map[string]any{"content": "domain field"}, semantic)
}

func TestToolContentRejectsUnsupportedFunctionMedia(t *testing.T) {
	t.Parallel()
	for _, block := range []content.ContentBlock{
		&content.AudioContent{Data: "AQI=", MIMEType: "audio/wav"},
		&content.ImageContent{Data: "AQI=", MIMEType: "image/gif"},
	} {
		_, _, err := encodeToolContent(model.ToolResultPart{ToolUseID: "call-1", Blocks: content.Blocks{block}}, nil)
		assert.ErrorIs(t, err, model.ErrToolContentUnsupported)
	}
}

func TestToolContentRejectsUnsupportedSpreadsheet(t *testing.T) {
	t.Parallel()
	_, _, err := encodeToolContent(model.ToolResultPart{ToolUseID: "call-1", Blocks: content.Blocks{
		&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "doc://sheet", MIMEType: new("text/csv"), Blob: "eCx5Cg=="}},
	}}, nil)
	assert.ErrorIs(t, err, model.ErrToolContentUnsupported)
}

func TestToolContentPreservesFailedFunctionResponse(t *testing.T) {
	t.Parallel()
	messages := []*model.Message{
		{Role: model.ConversationRoleAssistant, Parts: []model.Part{model.ToolUsePart{ID: "call-1", Name: "lookup", Input: rawjson.Message(`{}`)}}},
		{Role: model.ConversationRoleUser, Parts: []model.Part{model.ToolResultPart{
			ToolUseID: "call-1", Content: "rejected", IsError: true,
			Blocks: content.Blocks{&content.ImageContent{Data: "AQI=", MIMEType: "image/png"}},
		}}},
	}
	_, encoded, err := encodeContents(messages, map[string]string{"lookup": "lookup"})
	require.NoError(t, err)
	result := encoded[1].Parts[0].FunctionResponse
	assert.Equal(t, "call-1", result.ID)
	assert.Equal(t, "lookup", result.Name)
	assert.Equal(t, map[string]any{"output": "rejected", "error": true}, result.Response["error"])
	require.Len(t, result.Parts, 1)
	assert.Equal(t, []byte{1, 2}, result.Parts[0].InlineData.Data)
}
