// These tests keep native media inside the same Claude tool_result as its
// semantic result, error status and exact provider correlation identifier.
package anthropic

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"goa.design/goa-ai/runtime/agent/model"
	content "goa.design/goa-ai/runtime/content"
)

func TestToolContentUsesNativeCorrelatedResult(t *testing.T) {
	t.Parallel()
	result, err := encodeToolResult(model.ToolResultPart{
		ToolUseID: "call-1",
		Content:   "rejected",
		IsError:   true,
		Blocks: content.Blocks{
			&content.TextContent{Text: "detail", Meta: json.RawMessage(`{"secret":"host-value"}`)},
			&content.ImageContent{Data: "AQI=", MIMEType: "image/png"},
			&content.EmbeddedResource{Resource: &content.BlobResourceContents{URI: "private://pdf", MIMEType: new("application/pdf"), Blob: "AQI="}},
		},
	}, "provider-call-1")
	require.NoError(t, err)
	tool := result.OfToolResult
	require.NotNil(t, tool)
	assert.Equal(t, "provider-call-1", tool.ToolUseID)
	assert.True(t, tool.IsError.Value)
	require.Len(t, tool.Content, 5)
	assert.Equal(t, "rejected", tool.Content[0].OfText.Text)
	assert.Equal(t, "detail", tool.Content[1].OfText.Text)
	assert.Equal(t, "AQI=", tool.Content[2].OfImage.Source.OfBase64.Data)
	assert.Contains(t, tool.Content[3].OfText.Text, "private://pdf")
	assert.Equal(t, "AQI=", tool.Content[4].OfDocument.Source.OfBase64.Data)
	encoded, err := json.Marshal(result)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "host-value")
	assert.Contains(t, string(encoded), `"media_type":"application/pdf"`)
}

func TestToolContentRejectsAudioBeforeProviderDispatch(t *testing.T) {
	t.Parallel()
	_, err := encodeToolResult(model.ToolResultPart{ToolUseID: "call-1", Blocks: content.Blocks{
		&content.AudioContent{Data: "AQI=", MIMEType: "audio/wav"},
	}}, "provider-call-1")
	assert.ErrorIs(t, err, model.ErrToolContentUnsupported)
}

func TestToolContentRejectsUnsupportedSpreadsheet(t *testing.T) {
	t.Parallel()
	_, err := encodeToolContent(content.Blocks{&content.EmbeddedResource{Resource: &content.BlobResourceContents{
		URI: "doc://sheet", MIMEType: new("text/csv"), Blob: "eCx5Cg==",
	}}})
	assert.ErrorIs(t, err, model.ErrToolContentUnsupported)
}
